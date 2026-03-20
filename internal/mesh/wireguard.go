// Package mesh provides WireGuard mesh networking for the ussyverse server
// pool. It defines interfaces for WireGuard management and a stub
// implementation that can be used in tests and development without real
// WireGuard infrastructure.
//
// NOTE: The production implementation using tailscale magicsock/DERP is
// planned but not yet integrated. See PROGRESS-B.md for details.
package mesh

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// WireGuardPeer describes a WireGuard peer to add to an interface.
type WireGuardPeer struct {
	PublicKey string
	Endpoint  string // host:port (empty for peers behind NAT)
	AllowedIP net.IPNet
}

// WireGuardConfig holds the configuration for a WireGuard interface.
type WireGuardConfig struct {
	InterfaceName string
	ListenPort    int
	PrivateKey    string
	Address       net.IPNet // this node's address in the mesh
}

// WireGuardManager defines the interface for managing WireGuard tunnels.
// Implementations may use the kernel WireGuard module, userspace wireguard-go,
// or tailscale's magicsock.
type WireGuardManager interface {
	// CreateInterface sets up a WireGuard interface with the given config.
	CreateInterface(cfg WireGuardConfig) error

	// DeleteInterface tears down the WireGuard interface.
	DeleteInterface(name string) error

	// AddPeer adds a peer to the WireGuard interface.
	AddPeer(ifaceName string, peer WireGuardPeer) error

	// RemovePeer removes a peer from the WireGuard interface by public key.
	RemovePeer(ifaceName string, publicKey string) error

	// ListPeers returns all peers on the given interface.
	ListPeers(ifaceName string) ([]WireGuardPeer, error)
}

// ---------------------------------------------------------------------------
// Stub implementation (for development and testing)
// ---------------------------------------------------------------------------

// StubWireGuardManager is an in-memory mock of WireGuardManager.
// It does NOT create real WireGuard interfaces.
type StubWireGuardManager struct {
	mu         sync.Mutex
	interfaces map[string]*stubInterface
	logger     *slog.Logger
}

type stubInterface struct {
	config WireGuardConfig
	peers  map[string]WireGuardPeer // keyed by public key
}

// NewStubWireGuardManager creates a stub WireGuard manager for testing.
func NewStubWireGuardManager() *StubWireGuardManager {
	return &StubWireGuardManager{
		interfaces: make(map[string]*stubInterface),
		logger:     slog.Default().With("component", "wireguard-stub"),
	}
}

func (s *StubWireGuardManager) CreateInterface(cfg WireGuardConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.interfaces[cfg.InterfaceName]; exists {
		return fmt.Errorf("interface %s already exists", cfg.InterfaceName)
	}

	s.interfaces[cfg.InterfaceName] = &stubInterface{
		config: cfg,
		peers:  make(map[string]WireGuardPeer),
	}

	s.logger.Info("created stub WireGuard interface",
		"name", cfg.InterfaceName,
		"address", cfg.Address.String(),
		"port", cfg.ListenPort,
	)
	return nil
}

func (s *StubWireGuardManager) DeleteInterface(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.interfaces[name]; !exists {
		return fmt.Errorf("interface %s not found", name)
	}

	delete(s.interfaces, name)
	s.logger.Info("deleted stub WireGuard interface", "name", name)
	return nil
}

func (s *StubWireGuardManager) AddPeer(ifaceName string, peer WireGuardPeer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	iface, exists := s.interfaces[ifaceName]
	if !exists {
		return fmt.Errorf("interface %s not found", ifaceName)
	}

	iface.peers[peer.PublicKey] = peer
	s.logger.Info("added stub peer",
		"interface", ifaceName,
		"public_key", peer.PublicKey[:8]+"...",
		"allowed_ip", peer.AllowedIP.String(),
	)
	return nil
}

func (s *StubWireGuardManager) RemovePeer(ifaceName string, publicKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	iface, exists := s.interfaces[ifaceName]
	if !exists {
		return fmt.Errorf("interface %s not found", ifaceName)
	}

	if _, ok := iface.peers[publicKey]; !ok {
		return fmt.Errorf("peer %s not found on %s", publicKey, ifaceName)
	}

	delete(iface.peers, publicKey)
	s.logger.Info("removed stub peer", "interface", ifaceName, "public_key", publicKey[:8]+"...")
	return nil
}

func (s *StubWireGuardManager) ListPeers(ifaceName string) ([]WireGuardPeer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	iface, exists := s.interfaces[ifaceName]
	if !exists {
		return nil, fmt.Errorf("interface %s not found", ifaceName)
	}

	result := make([]WireGuardPeer, 0, len(iface.peers))
	for _, p := range iface.peers {
		result = append(result, p)
	}
	return result, nil
}

// Verify StubWireGuardManager implements WireGuardManager.
var _ WireGuardManager = (*StubWireGuardManager)(nil)
