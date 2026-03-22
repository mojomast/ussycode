// Package gateway implements the metadata service available inside VMs
// at http://169.254.169.254/. It identifies VMs by source IP and serves
// instance metadata, user info, and proxies to LLM/email gateways.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
)

// VMMetadata holds the metadata for a single VM, registered when the VM starts.
type VMMetadata struct {
	InstanceID string            `json:"instance-id"`
	LocalIPv4  string            `json:"local-ipv4"`
	Hostname   string            `json:"hostname"`
	UserID     int64             `json:"user-id"`
	UserHandle string            `json:"user-handle"`
	VMName     string            `json:"vm-name"`
	Image      string            `json:"image"`
	UserData   string            `json:"user-data,omitempty"`
	SSHKeys    []string          `json:"ssh-keys,omitempty"` // authorized_keys lines
	Gateway    string            `json:"gateway,omitempty"`
	EnvVars    map[string]string `json:"env,omitempty"` // environment variables to inject
}

// LLMConfig holds configuration for an LLM backend proxy.
type LLMConfig struct {
	Provider string // anthropic, openai, fireworks, ollama
	BaseURL  string // upstream URL
	APIKey   string // API key (empty for self-hosted)
}

// Server is the metadata HTTP server that VMs query at 169.254.169.254.
type Server struct {
	mu          sync.RWMutex
	vms         map[string]*VMMetadata // source IP -> metadata
	llm         map[string]*LLMConfig  // provider name -> config
	llmGateway  LLMGateway             // real LLM proxy (nil = stub mode)
	emailSender *EmailSender           // outbound email sender (nil = stub mode)
	listenAddr  string
	logger      *slog.Logger
}

// NewServer creates a new metadata server.
func NewServer(listenAddr string, logger *slog.Logger) *Server {
	return &Server{
		vms:        make(map[string]*VMMetadata),
		llm:        make(map[string]*LLMConfig),
		listenAddr: listenAddr,
		logger:     logger,
	}
}

// RegisterVM adds metadata for a VM. Must be called before the VM starts.
func (s *Server) RegisterVM(ip string, meta *VMMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vms[ip] = meta
	s.logger.Info("registered VM metadata", "ip", ip, "vm", meta.VMName, "user", meta.UserHandle)
}

// UnregisterVM removes metadata for a VM. Called when the VM stops.
func (s *Server) UnregisterVM(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta, ok := s.vms[ip]; ok {
		s.logger.Info("unregistered VM metadata", "ip", ip, "vm", meta.VMName)
	}
	delete(s.vms, ip)
}

// AddLLMProvider registers an LLM backend.
func (s *Server) AddLLMProvider(name string, cfg *LLMConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llm[name] = cfg
}

// SetLLMGateway sets the real LLM gateway proxy implementation.
// When set, LLM proxy requests are forwarded through it instead of the stub.
func (s *Server) SetLLMGateway(gw LLMGateway) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmGateway = gw
}

// SetEmailSender sets the outbound email sender.
// When set, email send requests are handled by it instead of the stub.
func (s *Server) SetEmailSender(sender *EmailSender) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emailSender = sender
}

// metadataForRequest resolves the calling VM's metadata from its source IP.
func (s *Server) metadataForRequest(r *http.Request) (*VMMetadata, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil, fmt.Errorf("parse remote addr: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	meta, ok := s.vms[host]
	if !ok {
		return nil, fmt.Errorf("unknown VM IP: %s", host)
	}
	return meta, nil
}

// Handler returns the HTTP handler for the metadata service.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Instance metadata
	mux.HandleFunc("/latest/meta-data/", s.handleMetadata)
	mux.HandleFunc("/metadata/vm", s.handleVMMetadata)
	mux.HandleFunc("/metadata/user", s.handleUserMetadata)

	// VM boot endpoints (called by init-ussycode.sh)
	mux.HandleFunc("/ssh-keys", s.handleSSHKeys)
	mux.HandleFunc("/hostname", s.handleHostname)
	mux.HandleFunc("/env", s.handleEnv)
	mux.HandleFunc("/gateway", s.handleGateway)

	// LLM gateway proxy
	mux.HandleFunc("/gateway/llm/", s.handleLLMProxy)

	// Email gateway
	mux.HandleFunc("/gateway/email/send", s.handleEmailSend)

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return mux
}

// Start starts the metadata HTTP server. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	if strings.HasPrefix(s.listenAddr, ":") {
		return s.startRoutedServer(ctx)
	}

	srv := &http.Server{
		Addr:    s.listenAddr,
		Handler: s.Handler(),
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	s.logger.Info("metadata server starting", "addr", s.listenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("metadata server: %w", err)
	}
	return nil
}

func (s *Server) startRoutedServer(ctx context.Context) error {
	// In production we expose the metadata service internally on a local port
	// (usually :8083) and rely on nftables to redirect guest traffic destined
	// for 169.254.169.254:80 to that port.
	//
	// We intentionally do not bind 169.254.169.254:80 on the host. On GCP, that
	// address is reserved for the platform metadata service and host DNS
	// resolution; binding or assigning it locally interferes with external DNS
	// and breaks outbound operations like ACME certificate issuance.
	routedSrv := &http.Server{Addr: s.listenAddr, Handler: s.Handler()}

	go func() {
		<-ctx.Done()
		_ = routedSrv.Shutdown(context.Background())
	}()

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("metadata server starting", "addr", routedSrv.Addr, "mode", "nft-redirect")
		if err := routedSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metadata server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// handleMetadata serves AWS-style metadata paths.
func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		s.logger.Warn("metadata request from unknown VM", "error", err, "remote", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Strip prefix to get the metadata key
	path := strings.TrimPrefix(r.URL.Path, "/latest/meta-data/")
	path = strings.TrimSuffix(path, "/")

	var value string
	switch path {
	case "":
		// List available keys
		value = "instance-id\nlocal-ipv4\nhostname\nuser-id\nuser-handle\nvm-name\nimage"
	case "instance-id":
		value = meta.InstanceID
	case "local-ipv4":
		value = meta.LocalIPv4
	case "hostname":
		value = meta.Hostname
	case "user-id":
		value = fmt.Sprintf("%d", meta.UserID)
	case "user-handle":
		value = meta.UserHandle
	case "vm-name":
		value = meta.VMName
	case "image":
		value = meta.Image
	default:
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(value))
}

// handleVMMetadata returns VM metadata as JSON.
// EnvVars are excluded to prevent leaking secrets like API keys.
// Use the /env endpoint for environment variables instead.
func (s *Server) handleVMMetadata(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Return metadata without sensitive fields
	safe := struct {
		InstanceID string   `json:"instance-id"`
		LocalIPv4  string   `json:"local-ipv4"`
		Hostname   string   `json:"hostname"`
		UserID     int64    `json:"user-id"`
		UserHandle string   `json:"user-handle"`
		VMName     string   `json:"vm-name"`
		Image      string   `json:"image"`
		UserData   string   `json:"user-data,omitempty"`
		SSHKeys    []string `json:"ssh-keys,omitempty"`
		Gateway    string   `json:"gateway,omitempty"`
	}{
		InstanceID: meta.InstanceID,
		LocalIPv4:  meta.LocalIPv4,
		Hostname:   meta.Hostname,
		UserID:     meta.UserID,
		UserHandle: meta.UserHandle,
		VMName:     meta.VMName,
		Image:      meta.Image,
		UserData:   meta.UserData,
		SSHKeys:    meta.SSHKeys,
		Gateway:    meta.Gateway,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(safe)
}

// handleUserMetadata returns user info as JSON.
func (s *Server) handleUserMetadata(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	info := map[string]any{
		"user_id": meta.UserID,
		"handle":  meta.UserHandle,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleSSHKeys returns the user's SSH public keys (one per line, authorized_keys format).
// Called by init-ussycode.sh at VM boot to populate ~/.ssh/authorized_keys.
func (s *Server) handleSSHKeys(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	for _, key := range meta.SSHKeys {
		fmt.Fprintln(w, key)
	}
}

// handleHostname returns the VM's hostname.
// Called by init-ussycode.sh at VM boot.
func (s *Server) handleHostname(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(meta.Hostname))
}

// handleEnv returns environment variables as KEY=VALUE lines.
// Called by init-ussycode.sh at VM boot.
func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	// Always provide these base env vars
	fmt.Fprintf(w, "USSYCODE_USER=%s\n", meta.UserHandle)
	fmt.Fprintf(w, "USSYCODE_VM=%s\n", meta.VMName)
	fmt.Fprintf(w, "USSYCODE_IMAGE=%s\n", meta.Image)

	// Custom env vars from metadata
	for k, v := range meta.EnvVars {
		fmt.Fprintf(w, "%s=%s\n", k, v)
	}
}

func (s *Server) handleGateway(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(meta.Gateway))
}

// handleLLMProxy proxies requests to configured LLM backends.
func (s *Server) handleLLMProxy(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadataForRequest(r)
	if err != nil {
		s.logger.Warn("LLM proxy request from unknown VM", "error", err, "remote", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Extract provider from path: /gateway/llm/{provider}/...
	path := strings.TrimPrefix(r.URL.Path, "/gateway/llm/")
	provider := strings.SplitN(path, "/", 2)[0]

	if provider == "" {
		http.Error(w, "provider required: /gateway/llm/{provider}", http.StatusBadRequest)
		return
	}

	// If the real LLM gateway is configured, use it
	s.mu.RLock()
	gw := s.llmGateway
	s.mu.RUnlock()

	if gw != nil {
		// Inject user ID into request context for the LLM gateway
		ctx := WithLLMUserID(r.Context(), meta.UserID)
		gw.Proxy(w, r.WithContext(ctx), provider)
		return
	}

	// Fallback: check old-style static config
	s.mu.RLock()
	cfg, ok := s.llm[provider]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("unknown LLM provider: %s", provider), http.StatusNotFound)
		return
	}

	// Stub response when no real gateway is configured
	s.logger.Info("LLM proxy request (stub mode)",
		"provider", provider,
		"vm", meta.VMName,
		"user", meta.UserHandle,
		"upstream", cfg.BaseURL,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "proxy_not_implemented",
		"provider": provider,
		"upstream": cfg.BaseURL,
	})
}

// handleEmailSend handles email send requests from VMs.
func (s *Server) handleEmailSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	meta, err := s.metadataForRequest(r)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// If the real email sender is configured, use it
	s.mu.RLock()
	sender := s.emailSender
	s.mu.RUnlock()

	if sender != nil {
		sender.HandleEmailSend(w, r, meta)
		return
	}

	// Stub response when no email sender is configured
	s.logger.Info("email send request (stub mode)",
		"vm", meta.VMName,
		"user", meta.UserHandle,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "email_not_implemented",
	})
}
