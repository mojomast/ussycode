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
	"github.com/mojomast/ussycode/internal/telemetry"
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
}

// Config holds API handler configuration.
type Config struct {
	RatePerMinute float64 // requests per minute per fingerprint (default: 60)
	Burst         int     // max burst (default: 10)
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

	return &Handler{
		db:          database,
		exec:        executor,
		keyResolver: resolver,
		limiter:     NewRateLimiter(rate, burst),
		logger:      logger,
	}
}

// Routes registers API routes on the given mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /exec", h.handleExec)
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.HandleFunc("GET /version", h.handleVersion)
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
