// Package api implements the ussycode HTTPS API endpoints.
//
// The API provides:
//   - POST /exec   -- execute SSH commands via HTTPS
//   - GET  /health -- health check
//   - GET  /version -- version info
//
// Authentication uses Bearer tokens in the Authorization header.
// Token formats:
//   - usy0.<base64url_permissions>.<base64url_ssh_signature> (stateless)
//   - usy1.<opaque_token_id> (short token, DB-backed)
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/mojomast/ussycode/internal/db"
	"github.com/mojomast/ussycode/internal/gateway"
	"github.com/mojomast/ussycode/internal/proxy"
	"github.com/mojomast/ussycode/internal/telemetry"
	"github.com/mojomast/ussycode/internal/vm"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/crypto/ssh"
)

// Version is set at build time via ldflags.
var Version = "dev"

// CommandExecutor executes SSH-style commands programmatically.
// This interface decouples the API from the SSH shell implementation.
type CommandExecutor interface {
	// Execute runs a command as the given user and returns the output.
	Execute(ctx context.Context, user *db.User, command string, args []string) (output string, exitCode int, err error)
}

// TokenPermissions is the permissions JSON embedded in usy0 tokens.
type TokenPermissions struct {
	Exp  int64    `json:"exp"`            // expiry unix timestamp
	Nbf  int64    `json:"nbf"`            // not-before unix timestamp
	Cmds []string `json:"cmds,omitempty"` // allowed commands (empty = all)
	Ctx  string   `json:"ctx,omitempty"`  // optional context
}

// KeyResolver looks up SSH public keys for a given user ID.
type KeyResolver func(ctx context.Context, userID int64) ([]ssh.PublicKey, error)

// Handler implements the ussycode HTTPS API.
type Handler struct {
	db          *db.DB
	exec        CommandExecutor
	keyResolver KeyResolver
	limiter     *RateLimiter
	logger      *slog.Logger
	internalKey string
	domain      string

	// Optional VM control dependencies (nil when VM support is disabled).
	vm       *vm.Manager
	metadata *gateway.Server
	proxy    *proxy.Manager
}

// Config holds API handler configuration.
type Config struct {
	RatePerMinute float64 // requests per minute per fingerprint (default: 60)
	Burst         int     // max burst (default: 10)
	InternalKey   string  // shared secret for internal routussy->ussycode API calls
	Domain        string  // base domain for VM public URLs

	// Optional VM control dependencies.
	VM       *vm.Manager
	Metadata *gateway.Server
	Proxy    *proxy.Manager
}

// NewHandler creates a new API handler.
func NewHandler(database *db.DB, executor CommandExecutor, resolver KeyResolver, logger *slog.Logger, cfg *Config) *Handler {
	if cfg == nil {
		cfg = &Config{}
	}
	rate := cfg.RatePerMinute
	if rate <= 0 {
		rate = 60
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = 10
	}

	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		domain = "ussy.host"
	}

	return &Handler{
		db:          database,
		exec:        executor,
		keyResolver: resolver,
		limiter:     NewRateLimiter(rate, burst),
		logger:      logger,
		internalKey: cfg.InternalKey,
		domain:      domain,
		vm:          cfg.VM,
		metadata:    cfg.Metadata,
		proxy:       cfg.Proxy,
	}
}

// Routes registers API routes on the given mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /exec", h.handleExec)
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.HandleFunc("GET /version", h.handleVersion)
	mux.HandleFunc("GET /internal/quota", h.handleInternalQuota)
	mux.HandleFunc("GET /internal/trust-tiers", h.handleInternalTrustTiers)
	mux.HandleFunc("POST /internal/trust", h.handleInternalTrust)
	mux.HandleFunc("GET /internal/vms", h.handleInternalVMs)
	mux.HandleFunc("POST /internal/vm/start", h.handleInternalVMStart)
	mux.HandleFunc("POST /internal/vm/stop", h.handleInternalVMStop)
}

// --- Exec Endpoint ---

// ExecRequest is the JSON request body for POST /exec.
type ExecRequest struct {
	Command string `json:"command"`
}

// ExecResponse is the JSON response for a successful exec.
type ExecResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// ErrorResponse is the JSON error response.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

func (h *Handler) handleExec(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Start(r.Context(), "api.exec",
		attribute.String("http.method", r.Method),
		attribute.String("http.route", "/exec"),
	)
	defer span.End()
	started := time.Now()
	result := "ok"
	cmdName := ""
	defer func() {
		telemetry.RecordExec(ctx, cmdName, result, time.Since(started))
	}()

	// Authenticate the request
	user, perms, fingerprint, err := h.authenticate(ctx, r)
	if err != nil {
		result = "auth_failed"
		h.logger.Debug("authentication failed", "error", err, "remote", r.RemoteAddr)
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	// Rate limit by fingerprint
	if fingerprint != "" && !h.limiter.Allow(fingerprint) {
		result = "rate_limited"
		retryAfter := h.limiter.RetryAfter(fingerprint)
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()+1))
		h.writeError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	if h.exec == nil {
		result = "executor_unavailable"
		h.logger.Error("API executor is not configured")
		h.writeError(w, "API executor unavailable", http.StatusServiceUnavailable)
		return
	}

	// Read the command from the request body
	command, err := h.readCommand(r)
	if err != nil {
		result = "bad_request"
		h.writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if command == "" {
		result = "empty_command"
		h.writeError(w, "empty command", http.StatusBadRequest)
		return
	}

	// Parse command into name + args
	parts := strings.Fields(command)
	cmdName = parts[0]
	cmdArgs := parts[1:]

	// Check command permissions (if restricted)
	if perms != nil && len(perms.Cmds) > 0 {
		allowed := false
		for _, c := range perms.Cmds {
			if c == cmdName {
				allowed = true
				break
			}
		}
		if !allowed {
			result = "forbidden"
			h.writeError(w, fmt.Sprintf("command %q not permitted by token", cmdName), http.StatusForbidden)
			return
		}
	}

	// Execute the command with a timeout
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	h.logger.Info("executing command",
		"user", user.Handle,
		"command", cmdName,
		"args", cmdArgs,
		"remote", r.RemoteAddr,
	)

	output, exitCode, err := h.exec.Execute(execCtx, user, cmdName, cmdArgs)
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			result = "timeout"
			h.writeError(w, "command timed out", http.StatusGatewayTimeout)
			return
		}
		result = "command_failed"
		h.logger.Error("command execution failed",
			"user", user.Handle,
			"command", cmdName,
			"error", err,
		)
		h.writeError(w, "command failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	h.writeJSON(w, http.StatusOK, ExecResponse{
		Output:   output,
		ExitCode: exitCode,
	})
}

// --- Health & Version ---

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	info := map[string]string{
		"version": Version,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		info["go"] = bi.GoVersion
	}

	h.writeJSON(w, http.StatusOK, info)
}

type internalQuotaResponse struct {
	Handle       string `json:"handle"`
	TrustLevel   string `json:"trust_level"`
	VMCount      int    `json:"vm_count"`
	TotalDiskGB  int    `json:"total_disk_gb"`
	VMLimit      int    `json:"vm_limit"`
	CPULimit     int    `json:"cpu_limit"`
	RAMLimitMB   int    `json:"ram_limit_mb"`
	DiskLimitMB  int    `json:"disk_limit_mb"`
}

type internalTrustTierResponse struct {
	Key            string `json:"key"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	VMLimit        int    `json:"vm_limit"`
	CPULimit       int    `json:"cpu_limit"`
	RAMLimitMB     int    `json:"ram_limit_mb"`
	DiskLimitMB    int    `json:"disk_limit_mb"`
	CanAccessAdmin bool   `json:"can_access_admin"`
	Requestable    bool   `json:"requestable"`
}

type internalTrustRequest struct {
	Handle     string `json:"handle"`
	TrustLevel string `json:"trust_level"`
}

// internalVMNodeInfo describes the node hosting a VM.
// Today this is always the local control-plane node.
// When multi-node lands, this will be populated from the nodes table.
type internalVMNodeInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type internalVMResponse struct {
	ID        int64               `json:"id"`
	Name      string              `json:"name"`
	Status    string              `json:"status"`
	Image     string              `json:"image"`
	VCPU      int                 `json:"vcpu"`
	MemoryMB  int                 `json:"memory_mb"`
	DiskGB    int                 `json:"disk_gb"`
	IPAddress *string             `json:"ip_address"`
	PublicURL string              `json:"public_url"`
	Node      *internalVMNodeInfo `json:"node,omitempty"`
	CreatedAt string              `json:"created_at"`
	UpdatedAt string              `json:"updated_at"`
}

func (h *Handler) requireInternalAuth(r *http.Request) error {
	if h.internalKey == "" {
		return fmt.Errorf("internal API disabled")
	}
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return fmt.Errorf("invalid authorization scheme")
	}
	if strings.TrimPrefix(authHeader, "Bearer ") != h.internalKey {
		return fmt.Errorf("invalid internal API key")
	}
	return nil
}

func (h *Handler) handleInternalQuota(w http.ResponseWriter, r *http.Request) {
	if err := h.requireInternalAuth(r); err != nil {
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	handle := strings.TrimSpace(r.URL.Query().Get("handle"))
	if handle == "" {
		h.writeError(w, "missing handle", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	user, err := h.db.UserByHandle(ctx, handle)
	if err != nil {
		h.writeError(w, "unknown user", http.StatusNotFound)
		return
	}
	quotas, err := h.db.GetUserQuotas(ctx, user.ID)
	if err != nil {
		h.writeError(w, "failed to load quotas", http.StatusInternalServerError)
		return
	}
	vmCount, err := h.db.GetUserVMCount(ctx, user.ID)
	if err != nil {
		h.writeError(w, "failed to load vm count", http.StatusInternalServerError)
		return
	}
	totalDiskGB, err := h.db.GetUserTotalDiskGB(ctx, user.ID)
	if err != nil {
		h.writeError(w, "failed to load disk usage", http.StatusInternalServerError)
		return
	}

	h.writeJSON(w, http.StatusOK, internalQuotaResponse{
		Handle:      user.Handle,
		TrustLevel:  quotas.Level,
		VMCount:     vmCount,
		TotalDiskGB: totalDiskGB,
		VMLimit:     quotas.VMLimit,
		CPULimit:    quotas.CPULimit,
		RAMLimitMB:  quotas.RAMLimit,
		DiskLimitMB: quotas.DiskLimit,
	})
}

func (h *Handler) handleInternalTrustTiers(w http.ResponseWriter, r *http.Request) {
	if err := h.requireInternalAuth(r); err != nil {
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	tiers := db.ListTrustTiers()
	resp := make([]internalTrustTierResponse, 0, len(tiers))
	for _, tier := range tiers {
		resp = append(resp, internalTrustTierResponse{
			Key:            tier.Key,
			DisplayName:    tier.DisplayName,
			Description:    tier.Description,
			VMLimit:        tier.VMLimit,
			CPULimit:       tier.CPULimit,
			RAMLimitMB:     tier.RAMLimitMB,
			DiskLimitMB:    tier.DiskLimitMB,
			CanAccessAdmin: tier.CanAccessAdmin,
			Requestable:    tier.Requestable,
		})
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"tiers": resp})
}

func (h *Handler) handleInternalTrust(w http.ResponseWriter, r *http.Request) {
	if err := h.requireInternalAuth(r); err != nil {
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		h.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req internalTrustRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		h.writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Handle) == "" || strings.TrimSpace(req.TrustLevel) == "" {
		h.writeError(w, "missing handle or trust_level", http.StatusBadRequest)
		return
	}
	if !db.IsValidTrustLevel(req.TrustLevel) {
		h.writeError(w, "invalid trust level", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	user, err := h.db.UserByHandle(ctx, req.Handle)
	if err != nil {
		h.writeError(w, "unknown user", http.StatusNotFound)
		return
	}
	if err := h.db.SetUserTrustLevel(ctx, user.ID, req.TrustLevel); err != nil {
		h.writeError(w, "failed to update trust level", http.StatusInternalServerError)
		return
	}

	quotas := db.GetTrustLimits(req.TrustLevel)
	h.writeJSON(w, http.StatusOK, internalQuotaResponse{
		Handle:      user.Handle,
		TrustLevel:  quotas.Level,
		VMCount:     0,
		TotalDiskGB: 0,
		VMLimit:     quotas.VMLimit,
		CPULimit:    quotas.CPULimit,
		RAMLimitMB:  quotas.RAMLimit,
		DiskLimitMB: quotas.DiskLimit,
	})
}

func (h *Handler) handleInternalVMs(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Start(r.Context(), "api.internal_vms")
	defer span.End()

	if err := h.requireInternalAuth(r); err != nil {
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	handle := strings.TrimSpace(r.URL.Query().Get("handle"))
	if handle == "" {
		h.writeError(w, "missing handle", http.StatusBadRequest)
		return
	}

	user, err := h.db.UserByHandle(ctx, handle)
	if err != nil {
		h.logger.Warn("internal VM list unknown user", "handle", handle, "error", err)
		h.writeError(w, "unknown user", http.StatusNotFound)
		return
	}

	vms, err := h.db.VMsByUser(ctx, user.ID)
	if err != nil {
		h.logger.Error("failed to load VMs", "handle", handle, "user_id", user.ID, "error", err)
		h.writeError(w, "failed to load VMs", http.StatusInternalServerError)
		return
	}

	resp := make([]internalVMResponse, 0, len(vms))
	for _, vm := range vms {
		var ipAddr *string
		if vm.IPAddress.Valid && strings.TrimSpace(vm.IPAddress.String) != "" {
			value := vm.IPAddress.String
			ipAddr = &value
		}

		entry := internalVMResponse{
			ID:        vm.ID,
			Name:      vm.Name,
			Status:    vm.Status,
			Image:     vm.Image,
			VCPU:      vm.VCPU,
			MemoryMB:  vm.MemoryMB,
			DiskGB:    vm.DiskGB,
			IPAddress: ipAddr,
			PublicURL: fmt.Sprintf("https://%s.%s", vm.Name, h.domain),
			CreatedAt: vm.CreatedAt.Time.UTC().Format(time.RFC3339),
			UpdatedAt: vm.UpdatedAt.Time.UTC().Format(time.RFC3339),
		}

		resp = append(resp, entry)
	}

	h.logger.Info("listed internal VMs", "handle", handle, "user_id", user.ID, "count", len(resp))
	h.writeJSON(w, http.StatusOK, map[string]any{"vms": resp})
}

// internalVMActionRequest is the JSON body for internal VM start/stop.
type internalVMActionRequest struct {
	Handle string `json:"handle"`
	VMName string `json:"vm_name"`
}

func (h *Handler) handleInternalVMStop(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Start(r.Context(), "api.internal_vm_stop")
	defer span.End()

	if err := h.requireInternalAuth(r); err != nil {
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var req internalVMActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.Handle = strings.TrimSpace(req.Handle)
	req.VMName = strings.TrimSpace(req.VMName)
	if req.Handle == "" || req.VMName == "" {
		h.writeError(w, "handle and vm_name are required", http.StatusBadRequest)
		return
	}

	user, err := h.db.UserByHandle(ctx, req.Handle)
	if err != nil {
		h.logger.Warn("internal VM stop unknown user", "handle", req.Handle, "error", err)
		h.writeError(w, "unknown user", http.StatusNotFound)
		return
	}

	vmRecord, err := h.db.VMByUserAndName(ctx, user.ID, req.VMName)
	if err != nil {
		h.logger.Warn("internal VM stop unknown vm", "handle", req.Handle, "vm_name", req.VMName, "error", err)
		h.writeError(w, "unknown vm", http.StatusNotFound)
		return
	}

	if vmRecord.Status != "running" {
		h.writeError(w, fmt.Sprintf("vm is %s, not running", vmRecord.Status), http.StatusConflict)
		return
	}

	// Unregister metadata
	if h.metadata != nil && vmRecord.IPAddress.Valid {
		h.metadata.UnregisterVM(vmRecord.IPAddress.String)
	}

	// Remove proxy route
	if h.proxy != nil {
		if err := h.proxy.RemoveRoute(ctx, req.VMName); err != nil {
			h.logger.Warn("failed to remove proxy route on stop", "vm", req.VMName, "error", err)
		}
	}

	// Stop the VM
	if h.vm != nil {
		if err := h.vm.Stop(ctx, vmRecord.ID); err != nil {
			h.logger.Error("internal VM stop failed", "handle", req.Handle, "vm_name", req.VMName, "error", err)
			h.writeError(w, "failed to stop vm: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		_ = h.db.UpdateVMStatus(ctx, vmRecord.ID, "stopped", nil, nil, nil, nil)
	}

	h.logger.Info("internal VM stopped", "handle", req.Handle, "vm_name", req.VMName, "vm_id", vmRecord.ID)
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "stopped", "vm_name": req.VMName})
}

func (h *Handler) handleInternalVMStart(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Start(r.Context(), "api.internal_vm_start")
	defer span.End()

	if err := h.requireInternalAuth(r); err != nil {
		h.writeError(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var req internalVMActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.Handle = strings.TrimSpace(req.Handle)
	req.VMName = strings.TrimSpace(req.VMName)
	if req.Handle == "" || req.VMName == "" {
		h.writeError(w, "handle and vm_name are required", http.StatusBadRequest)
		return
	}

	user, err := h.db.UserByHandle(ctx, req.Handle)
	if err != nil {
		h.logger.Warn("internal VM start unknown user", "handle", req.Handle, "error", err)
		h.writeError(w, "unknown user", http.StatusNotFound)
		return
	}

	vmRecord, err := h.db.VMByUserAndName(ctx, user.ID, req.VMName)
	if err != nil {
		h.logger.Warn("internal VM start unknown vm", "handle", req.Handle, "vm_name", req.VMName, "error", err)
		h.writeError(w, "unknown vm", http.StatusNotFound)
		return
	}

	if vmRecord.Status == "running" {
		h.writeError(w, "vm is already running", http.StatusConflict)
		return
	}

	// Collect SSH keys for the user
	var sshKeys []string
	keys, err := h.db.SSHKeysByUser(ctx, user.ID)
	if err == nil {
		for _, k := range keys {
			sshKeys = append(sshKeys, k.PublicKey)
		}
	}
	// Include per-user gateway key if VM manager is available
	if h.vm != nil {
		if pubKey, err := h.vm.UserPublicKey(user.ID); err == nil && pubKey != "" {
			sshKeys = append(sshKeys, pubKey)
		}
	}

	// Start the VM
	if h.vm != nil {
		if err := h.vm.Start(ctx, vmRecord.ID, vmRecord.Name, vmRecord.Image, vmRecord.VCPU, vmRecord.MemoryMB, sshKeys); err != nil {
			h.logger.Error("internal VM start failed", "handle", req.Handle, "vm_name", req.VMName, "error", err)
			h.writeError(w, "failed to start vm: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Re-read VM to get the assigned IP for metadata/proxy registration
		updatedVM, err := h.db.VMByUserAndName(ctx, user.ID, req.VMName)
		if err == nil && updatedVM.IPAddress.Valid {
			ip := updatedVM.IPAddress.String

			// Register metadata (without routussy env vars — those require SSH session context)
			if h.metadata != nil {
				h.metadata.RegisterVM(ip, &gateway.VMMetadata{
					InstanceID: fmt.Sprintf("vm-%d", vmRecord.ID),
					LocalIPv4:  ip,
					Hostname:   req.VMName,
					UserID:     user.ID,
					UserHandle: user.Handle,
					VMName:     req.VMName,
					Image:      vmRecord.Image,
					SSHKeys:    sshKeys,
					Gateway:    "10.0.0.1",
				})
			}

			// Register proxy route
			if h.proxy != nil {
				if err := h.proxy.UpdateRoute(ctx, req.VMName, ip, 8080); err != nil {
					h.logger.Warn("failed to add proxy route on start", "vm", req.VMName, "ip", ip, "error", err)
				}
			}
		}
	} else {
		_ = h.db.UpdateVMStatus(ctx, vmRecord.ID, "running", nil, nil, nil, nil)
	}

	h.logger.Info("internal VM started", "handle", req.Handle, "vm_name", req.VMName, "vm_id", vmRecord.ID)
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "running", "vm_name": req.VMName})
}

// --- Authentication ---

// authenticate extracts and verifies the Bearer token from the request.
// Returns the authenticated user, optional permissions, the SSH fingerprint, and any error.
func (h *Handler) authenticate(ctx context.Context, r *http.Request) (*db.User, *TokenPermissions, string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, nil, "", fmt.Errorf("missing Authorization header")
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, nil, "", fmt.Errorf("invalid authorization scheme (expected Bearer)")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Dispatch based on token prefix
	switch {
	case strings.HasPrefix(token, "usy0."):
		return h.authenticateStateless(ctx, token)
	case strings.HasPrefix(token, "usy1."):
		return h.authenticateShortToken(ctx, token)
	default:
		return nil, nil, "", fmt.Errorf("unrecognized token format")
	}
}

// authenticateStateless verifies a usy0 (stateless) token.
// Format: usy0.<base64url_permissions_json>.<base64url_ssh_signature>
func (h *Handler) authenticateStateless(ctx context.Context, token string) (*db.User, *TokenPermissions, string, error) {
	// Strip "usy0." prefix
	raw := strings.TrimPrefix(token, "usy0.")
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return nil, nil, "", fmt.Errorf("invalid usy0 token format")
	}

	// Decode permissions JSON
	permsJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid token payload encoding")
	}

	var perms TokenPermissions
	if err := json.Unmarshal(permsJSON, &perms); err != nil {
		return nil, nil, "", fmt.Errorf("invalid token permissions")
	}

	// Check temporal validity
	now := time.Now().UTC().Unix()
	if now < perms.Nbf {
		return nil, nil, "", fmt.Errorf("token not yet valid")
	}
	if now > perms.Exp {
		return nil, nil, "", fmt.Errorf("token expired")
	}

	// Decode signature
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid token signature encoding")
	}

	var sig ssh.Signature
	if err := ssh.Unmarshal(sigBytes, &sig); err != nil {
		return nil, nil, "", fmt.Errorf("invalid token signature")
	}

	// We need to try all users' keys since the stateless token doesn't
	// embed the user identity. In practice, we'd need a subject claim.
	// For now, we'll require the ctx field to contain the user handle.
	if perms.Ctx == "" {
		return nil, nil, "", fmt.Errorf("usy0 token missing ctx (user handle)")
	}

	user, err := h.db.UserByHandle(ctx, perms.Ctx)
	if err != nil {
		return nil, nil, "", fmt.Errorf("unknown user")
	}

	if h.keyResolver == nil {
		return nil, nil, "", fmt.Errorf("stateless token verification unavailable")
	}

	// Resolve the user's SSH keys
	keys, err := h.keyResolver(ctx, user.ID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to resolve keys")
	}

	// Verify signature against user's keys
	verified := false
	var fingerprint string
	for _, key := range keys {
		if err := key.Verify(permsJSON, &sig); err == nil {
			verified = true
			fingerprint = ssh.FingerprintSHA256(key)
			break
		}
	}
	if !verified {
		return nil, nil, "", fmt.Errorf("signature verification failed")
	}

	return user, &perms, fingerprint, nil
}

// authenticateShortToken verifies a usy1 (short/opaque) token from the DB.
// Format: usy1.<token_id>
func (h *Handler) authenticateShortToken(ctx context.Context, token string) (*db.User, *TokenPermissions, string, error) {
	tokenID := strings.TrimPrefix(token, "usy1.")
	if tokenID == "" {
		return nil, nil, "", fmt.Errorf("empty token ID")
	}

	// Look up in database
	apiToken, err := h.db.APITokenByID(ctx, tokenID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid token")
	}

	if apiToken.Revoked {
		return nil, nil, "", fmt.Errorf("token revoked")
	}

	// Parse the embedded full token to get permissions
	var perms *TokenPermissions
	if apiToken.FullToken != "" {
		// The full_token field stores the permissions JSON
		var p TokenPermissions
		if err := json.Unmarshal([]byte(apiToken.FullToken), &p); err == nil {
			// Check temporal validity
			now := time.Now().UTC().Unix()
			if p.Exp > 0 && now > p.Exp {
				return nil, nil, "", fmt.Errorf("token expired")
			}
			perms = &p
		}
	}

	// Look up user
	user, err := h.db.UserByID(ctx, apiToken.UserID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("user not found")
	}

	// Get fingerprint for rate limiting
	fingerprint, _ := h.db.FingerprintByUser(ctx, user.ID)

	// Update last used timestamp (fire-and-forget)
	go func() {
		_ = h.db.TouchAPIToken(context.Background(), tokenID)
	}()

	return user, perms, fingerprint, nil
}

// --- Helpers ---

// readCommand extracts the command from the request body.
// Supports both text/plain and application/json.
func (h *Handler) readCommand(r *http.Request) (string, error) {
	// Limit request body size (64KB max)
	const maxBody = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return "", fmt.Errorf("failed to read request body")
	}
	if len(body) > maxBody {
		return "", &httpError{
			message: "request body too large (max 64KB)",
			code:    http.StatusRequestEntityTooLarge,
		}
	}

	ct := r.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(ct, "application/json"):
		var req ExecRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return "", fmt.Errorf("invalid JSON: %w", err)
		}
		return strings.TrimSpace(req.Command), nil

	case strings.HasPrefix(ct, "text/plain"), ct == "":
		return strings.TrimSpace(string(body)), nil

	default:
		return "", fmt.Errorf("unsupported Content-Type %q (use application/json or text/plain)", ct)
	}
}

// writeJSON writes a JSON response with the given status code.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to write JSON response", "error", err)
	}
}

// writeError writes a JSON error response.
func (h *Handler) writeError(w http.ResponseWriter, message string, code int) {
	h.writeJSON(w, code, ErrorResponse{
		Error: message,
		Code:  code,
	})
}

// httpError is an error that carries an HTTP status code.
type httpError struct {
	message string
	code    int
}

func (e *httpError) Error() string {
	return e.message
}
