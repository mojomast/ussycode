package vm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sync"
)

// NetworkBackend is the interface contract for VM network management.
// Implementations handle TAP device creation, IP allocation, and NAT rules.
type NetworkBackend interface {
	SetupBridge() error
	AllocateNetwork(vmID string) (*NetworkConfig, error)
	ReleaseNetwork(vmID, tapDevice string) error
}

// NetworkManager handles TAP device creation, IP address allocation,
// and nftables NAT rules for microVM networking.
type NetworkManager struct {
	bridge    string // bridge interface name (e.g., "ussy0")
	subnet    *net.IPNet
	gateway   net.IP            // first usable IP in subnet (host side)
	allocated map[string]string // vmID -> IP
	nextIP    uint32
	mu        sync.Mutex
	logger    *slog.Logger
	firewall  FirewallManager // nftables-based firewall manager
}

// NetworkConfig holds the network configuration assigned to a VM.
type NetworkConfig struct {
	TapDevice  string
	GuestIP    string // IP assigned to the VM guest
	GatewayIP  string // IP of the host (gateway for the guest)
	MacAddress string
	SubnetMask string
}

// NewNetworkManager creates a new network manager for the given bridge and subnet.
// If firewall is nil, a default NftablesManager is used.
func NewNetworkManager(bridge, subnetCIDR string, logger *slog.Logger) (*NetworkManager, error) {
	_, subnet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("parse subnet %q: %w", subnetCIDR, err)
	}

	// Gateway is the first usable IP (e.g., 10.0.0.1 for 10.0.0.0/24)
	gateway := make(net.IP, 4)
	copy(gateway, subnet.IP.To4())
	gateway[3]++

	// Start allocating from gateway+1 (e.g., 10.0.0.2)
	startIP := binary.BigEndian.Uint32(gateway.To4()) + 1

	return &NetworkManager{
		bridge:    bridge,
		subnet:    subnet,
		gateway:   gateway,
		allocated: make(map[string]string),
		nextIP:    startIP,
		logger:    logger,
		firewall:  NewNftablesManager(nil, logger.With("component", "nftables")),
	}, nil
}

// SetupBridge creates the bridge interface and assigns the gateway IP.
// This is idempotent -- safe to call if the bridge already exists.
func (nm *NetworkManager) SetupBridge() error {
	nm.logger.Info("setting up bridge", "bridge", nm.bridge, "gateway", nm.gateway.String())

	// Create bridge if it doesn't exist
	if err := runCmd("ip", "link", "add", nm.bridge, "type", "bridge"); err != nil {
		nm.logger.Debug("bridge may already exist", "error", err)
	}

	// Assign gateway IP
	ones, _ := nm.subnet.Mask.Size()
	gatewayWithMask := fmt.Sprintf("%s/%d", nm.gateway.String(), ones)
	if err := runCmd("ip", "addr", "add", gatewayWithMask, "dev", nm.bridge); err != nil {
		nm.logger.Debug("gateway IP may already be assigned", "error", err)
	}

	// Bring bridge up
	if err := runCmd("ip", "link", "set", nm.bridge, "up"); err != nil {
		return fmt.Errorf("bring up bridge: %w", err)
	}

	// Expose the metadata service IP on the bridge so VMs can reach it.
	// Clear any stale loopback assignment first so the bridge owns the address.
	_ = runCmd("ip", "addr", "del", "169.254.169.254/32", "dev", "lo")
	if err := runCmd("ip", "addr", "add", "169.254.169.254/32", "dev", nm.bridge); err != nil {
		nm.logger.Debug("metadata IP may already be assigned", "error", err)
	}

	// Enable IP forwarding
	if err := runCmd("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}

	// Setup NAT via nftables (replaces legacy iptables masquerade)
	ones, _ = nm.subnet.Mask.Size()
	subnetStr := fmt.Sprintf("%s/%d", nm.subnet.IP.String(), ones)

	ctx := context.Background()
	if err := nm.firewall.SetupNAT(ctx, nm.bridge, subnetStr); err != nil {
		return fmt.Errorf("setup nftables NAT: %w", err)
	}

	return nil
}

// AllocateNetwork creates a TAP device and assigns an IP for a new VM.
func (nm *NetworkManager) AllocateNetwork(vmID string) (*NetworkConfig, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Allocate next IP
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, nm.nextIP)
	nm.nextIP++

	// Check if IP is still within subnet
	if !nm.subnet.Contains(ip) {
		return nil, fmt.Errorf("subnet exhausted: no more IPs available")
	}

	// Generate a unique TAP device name
	shortID := vmID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	tapName := fmt.Sprintf("tap-%s", shortID)
	if len(tapName) > 15 {
		tapName = tapName[:15] // Linux interface name limit
	}

	// Generate a locally-administered MAC address
	mac := generateMAC()

	// Create TAP device
	if err := runCmd("ip", "tuntap", "add", tapName, "mode", "tap"); err != nil {
		return nil, fmt.Errorf("create tap device: %w", err)
	}

	// Attach TAP to bridge
	if err := runCmd("ip", "link", "set", tapName, "master", nm.bridge); err != nil {
		// Cleanup on failure
		runCmd("ip", "link", "del", tapName)
		return nil, fmt.Errorf("attach tap to bridge: %w", err)
	}

	// Bring TAP up
	if err := runCmd("ip", "link", "set", tapName, "up"); err != nil {
		runCmd("ip", "link", "del", tapName)
		return nil, fmt.Errorf("bring up tap: %w", err)
	}

	nm.allocated[vmID] = ip.String()

	ones, _ := nm.subnet.Mask.Size()

	config := &NetworkConfig{
		TapDevice:  tapName,
		GuestIP:    ip.String(),
		GatewayIP:  nm.gateway.String(),
		MacAddress: mac,
		SubnetMask: fmt.Sprintf("%d", ones),
	}

	nm.logger.Info("allocated network",
		"vm", vmID,
		"tap", tapName,
		"ip", ip.String(),
		"mac", mac,
	)

	return config, nil
}

// ReleaseNetwork tears down the TAP device and frees the IP allocation.
func (nm *NetworkManager) ReleaseNetwork(vmID, tapDevice string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if tapDevice != "" {
		if err := runCmd("ip", "link", "del", tapDevice); err != nil {
			nm.logger.Warn("failed to delete tap device", "tap", tapDevice, "error", err)
		}
	}

	delete(nm.allocated, vmID)
	nm.logger.Info("released network", "vm", vmID)
	return nil
}

// generateMAC generates a random locally-administered unicast MAC address.
func generateMAC() string {
	buf := make([]byte, 6)
	rand.Read(buf)
	// Set locally administered bit, clear multicast bit
	buf[0] = (buf[0] & 0xfe) | 0x02
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5])
}

// runCmd runs a command and returns an error if it fails.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %s: %w", name, args, string(out), err)
	}
	return nil
}
