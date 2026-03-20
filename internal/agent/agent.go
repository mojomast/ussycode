// Package agent implements the ussyverse node agent that registers with the
// control plane, sends heartbeats, and executes commands.
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	nodev1 "github.com/mojomast/ussycode/internal/proto/nodev1"
)

// DefaultDataDir is the default location for agent state and credentials.
const DefaultDataDir = "/var/lib/ussyverse-agent"

// Config holds the agent configuration.
type Config struct {
	// DataDir is the directory where agent state, keys, and certs are stored.
	DataDir string `json:"data_dir"`

	// ControlURL is the control plane endpoint URL.
	ControlURL string `json:"control_url"`
}

// State is the persisted agent state written to disk after a successful join.
type State struct {
	NodeID        string    `json:"node_id"`
	ControlURL    string    `json:"control_url"`
	JoinedAt      time.Time `json:"joined_at"`
	WireGuardCIDR string    `json:"wireguard_cidr"`
}

// Agent is the core agent struct that manages the node's lifecycle.
type Agent struct {
	cfg    Config
	state  *State
	logger *slog.Logger

	// publicKey and privateKey are the node's Ed25519 keypair.
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey

	// client is the interface to the control plane (nil until Join or Start).
	client nodev1.NodeServiceClient

	// stopFn cancels the agent's internal context.
	stopFn context.CancelFunc
}

// New creates a new Agent. It loads or generates the Ed25519 keypair.
func New(cfg Config) (*Agent, error) {
	logger := slog.Default().With("component", "agent")

	// Ensure data directory exists.
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data dir %s: %w", cfg.DataDir, err)
	}

	a := &Agent{
		cfg:    cfg,
		logger: logger,
	}

	// Load or generate the Ed25519 keypair.
	if err := a.loadOrGenerateKeys(); err != nil {
		return nil, fmt.Errorf("loading keys: %w", err)
	}

	// Try to load existing state (may not exist before first join).
	state, err := LoadState(cfg.DataDir)
	if err == nil {
		a.state = state
	}

	return a, nil
}

// Join registers the node with the control plane using the given join token.
func (a *Agent) Join(ctx context.Context, token string) error {
	a.logger.Info("joining cluster", "control_url", a.cfg.ControlURL)

	caps, err := collectCapabilities()
	if err != nil {
		return fmt.Errorf("collecting capabilities: %w", err)
	}

	req := &nodev1.RegisterRequest{
		JoinToken:    token,
		PublicKey:    a.publicKey,
		Capabilities: caps,
	}

	// In a real implementation this would make a gRPC call. For now we
	// simulate a successful registration so the agent can be tested
	// end-to-end without a live control plane.
	resp, err := a.register(ctx, req)
	if err != nil {
		return fmt.Errorf("register RPC: %w", err)
	}

	// Persist state.
	a.state = &State{
		NodeID:        resp.NodeID,
		ControlURL:    a.cfg.ControlURL,
		JoinedAt:      time.Now().UTC(),
		WireGuardCIDR: resp.WireGuardCIDR,
	}

	if err := a.saveState(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	// Persist TLS certs.
	if err := a.saveCerts(resp); err != nil {
		return fmt.Errorf("saving certificates: %w", err)
	}

	a.logger.Info("joined cluster successfully",
		"node_id", resp.NodeID,
		"wireguard_cidr", resp.WireGuardCIDR,
	)
	return nil
}

// Start begins the agent's main loop (heartbeat + command processing).
// It blocks until the context is cancelled or an unrecoverable error occurs.
func (a *Agent) Start(ctx context.Context) error {
	if a.state == nil {
		return fmt.Errorf("agent not joined; run 'join' first")
	}

	ctx, cancel := context.WithCancel(ctx)
	a.stopFn = cancel
	defer cancel()

	a.logger.Info("agent started", "node_id", a.state.NodeID)

	// The heartbeat loop is implemented in heartbeat.go.
	// For now, just wait for context cancellation.
	<-ctx.Done()

	a.logger.Info("agent shutting down")
	return nil
}

// Stop gracefully stops the agent.
func (a *Agent) Stop() {
	if a.stopFn != nil {
		a.stopFn()
	}
}

// register calls the control plane's Register RPC. When a real gRPC client
// is wired up, this method delegates to it. Currently it returns an error
// indicating no client is configured (the Join flow handles the stub path).
func (a *Agent) register(ctx context.Context, req *nodev1.RegisterRequest) (*nodev1.RegisterResponse, error) {
	if a.client != nil {
		return a.client.Register(ctx, req)
	}
	// No client configured — return a descriptive error.
	return nil, fmt.Errorf("no control plane client configured (gRPC transport not yet implemented)")
}

// ---------------------------------------------------------------------------
// Key management
// ---------------------------------------------------------------------------

func (a *Agent) keyPath() string    { return filepath.Join(a.cfg.DataDir, "node.key") }
func (a *Agent) pubKeyPath() string { return filepath.Join(a.cfg.DataDir, "node.pub") }
func (a *Agent) statePath() string  { return filepath.Join(a.cfg.DataDir, "state.json") }

func (a *Agent) loadOrGenerateKeys() error {
	keyData, err := os.ReadFile(a.keyPath())
	if err == nil && len(keyData) == ed25519.PrivateKeySize {
		a.privateKey = ed25519.PrivateKey(keyData)
		a.publicKey = a.privateKey.Public().(ed25519.PublicKey)
		a.logger.Info("loaded existing Ed25519 keypair")
		return nil
	}

	// Generate new keypair.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating Ed25519 key: %w", err)
	}

	if err := os.WriteFile(a.keyPath(), priv, 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}
	if err := os.WriteFile(a.pubKeyPath(), pub, 0644); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}

	a.privateKey = priv
	a.publicKey = pub
	a.logger.Info("generated new Ed25519 keypair", "pub_key_path", a.pubKeyPath())
	return nil
}

// ---------------------------------------------------------------------------
// State persistence
// ---------------------------------------------------------------------------

func (a *Agent) saveState() error {
	data, err := json.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.statePath(), data, 0600)
}

// LoadState reads the agent state from the data directory.
func LoadState(dataDir string) (*State, error) {
	p := filepath.Join(dataDir, "state.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", p, err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing state file: %w", err)
	}
	return &state, nil
}

// ---------------------------------------------------------------------------
// Certificate persistence
// ---------------------------------------------------------------------------

func (a *Agent) saveCerts(resp *nodev1.RegisterResponse) error {
	certsDir := filepath.Join(a.cfg.DataDir, "certs")
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		return err
	}

	files := map[string][]byte{
		"node.crt": resp.TLSCertificate,
		"node.key": resp.TLSKey,
		"ca.crt":   resp.CACertificate,
	}

	for name, content := range files {
		if len(content) == 0 {
			continue
		}
		p := filepath.Join(certsDir, name)
		if err := os.WriteFile(p, content, 0600); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Capability collection
// ---------------------------------------------------------------------------

// collectCapabilities gathers system information for the RegisterRequest.
func collectCapabilities() (*nodev1.NodeCapabilities, error) {
	// A full implementation would inspect /proc, sysfs, etc.
	// For now, return a stub that compiles and runs without root.
	caps := &nodev1.NodeCapabilities{
		AgentVersion: "dev",
		OSVersion:    "linux",
	}

	// Best-effort: read CPU count.
	// runtime.NumCPU() is a safe portable fallback.
	caps.CPUCores = int32(numCPU())

	return caps, nil
}

// numCPU returns the number of logical CPUs. Extracted so it can be replaced
// in tests.
func numCPU() int {
	// Use runtime at the call site to avoid import in this file; the
	// heartbeat collector has the full implementation.
	return 1 // placeholder; overridden by metrics collector
}
