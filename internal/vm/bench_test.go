package vm

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
)

// BenchmarkNetworkAllocate benchmarks the IP and TAP allocation logic.
// Note: This only benchmarks the in-memory allocation, not the actual
// TAP device creation (which requires root and real network interfaces).
func BenchmarkNetworkAllocate(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Use a mock executor so we don't try to run real ip/nft commands
	mockExec := newMockExecutor()
	nft := NewNftablesManager(mockExec, logger)

	_, subnet, _ := net.ParseCIDR("10.0.0.0/16")

	nm := &NetworkManager{
		bridge:    "bench0",
		gateway:   net.IP{10, 0, 0, 1},
		allocated: make(map[string]string),
		nextIP:    0x0a000002, // 10.0.0.2
		logger:    logger,
		firewall:  nft,
		subnet:    subnet,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vmID := fmt.Sprintf("bench-vm-%08d", i)
		// We can't call AllocateNetwork because it runs real ip commands.
		// Instead, benchmark the IP allocation logic directly.
		nm.mu.Lock()
		ip := make([]byte, 4)
		ip[0] = byte(nm.nextIP >> 24)
		ip[1] = byte(nm.nextIP >> 16)
		ip[2] = byte(nm.nextIP >> 8)
		ip[3] = byte(nm.nextIP)
		nm.nextIP++
		nm.allocated[vmID] = fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
		nm.mu.Unlock()
	}
}

// BenchmarkMACGeneration benchmarks MAC address generation.
func BenchmarkMACGeneration(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = generateMAC()
	}
}

// BenchmarkCIDRMaskConversion benchmarks the CIDR mask to dotted decimal
// netmask conversion used in Firecracker guest boot args.
func BenchmarkCIDRMaskConversion(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = cidrMaskToNetmask("24")
	}
}
