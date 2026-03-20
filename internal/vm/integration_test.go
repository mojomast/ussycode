package vm

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// Integration test stubs for VM lifecycle operations.
// These tests require real system resources (firecracker, bridge, etc.)
// and are skipped in normal CI runs. Run with:
//
//	go test -tags=integration ./internal/vm/...
//
// Or manually with:
//
//	USSYCODE_INTEGRATION=1 go test -run TestIntegration ./internal/vm/...

func skipIfNotIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("USSYCODE_INTEGRATION") == "" {
		t.Skip("skipping integration test (set USSYCODE_INTEGRATION=1 to run)")
	}
}

func TestIntegration_NetworkSetupBridge(t *testing.T) {
	skipIfNotIntegration(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	nm, err := NewNetworkManager("ussy-test0", "10.99.0.0/24", logger)
	if err != nil {
		t.Fatalf("NewNetworkManager() error = %v", err)
	}

	if err := nm.SetupBridge(); err != nil {
		t.Fatalf("SetupBridge() error = %v", err)
	}

	// Verify bridge exists
	// TODO: Check via netlink or ip link show
	t.Log("bridge setup succeeded")
}

func TestIntegration_NetworkAllocateRelease(t *testing.T) {
	skipIfNotIntegration(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	nm, err := NewNetworkManager("ussy-test0", "10.99.0.0/24", logger)
	if err != nil {
		t.Fatalf("NewNetworkManager() error = %v", err)
	}

	cfg, err := nm.AllocateNetwork("test-vm-1")
	if err != nil {
		t.Fatalf("AllocateNetwork() error = %v", err)
	}

	t.Logf("allocated: tap=%s ip=%s gw=%s mac=%s", cfg.TapDevice, cfg.GuestIP, cfg.GatewayIP, cfg.MacAddress)

	if err := nm.ReleaseNetwork("test-vm-1", cfg.TapDevice); err != nil {
		t.Fatalf("ReleaseNetwork() error = %v", err)
	}
	t.Log("allocate/release cycle succeeded")
}

func TestIntegration_NftablesNAT(t *testing.T) {
	skipIfNotIntegration(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	nft := NewNftablesManager(nil, logger)

	ctx := context.Background()

	// Setup NAT
	if err := nft.SetupNAT(ctx, "ussy-test0", "10.99.0.0/24"); err != nil {
		t.Fatalf("SetupNAT() error = %v", err)
	}

	// Verify table exists via nft list
	// TODO: Parse nft list table inet ussycode

	// Add VM rules
	if err := nft.AddVMRules(ctx, "integration-test", "tap-inttest", "10.99.0.2", "ussy-test0"); err != nil {
		t.Fatalf("AddVMRules() error = %v", err)
	}

	// Remove VM rules
	if err := nft.RemoveVMRules(ctx, "integration-test", "tap-inttest", "ussy-test0"); err != nil {
		t.Fatalf("RemoveVMRules() error = %v", err)
	}

	// Cleanup
	if err := nft.CleanupNAT(ctx, "ussy-test0", "10.99.0.0/24"); err != nil {
		t.Fatalf("CleanupNAT() error = %v", err)
	}

	t.Log("nftables integration test succeeded")
}

func TestIntegration_VMCreateAndStart(t *testing.T) {
	skipIfNotIntegration(t)

	// This test requires:
	// - firecracker binary installed
	// - kernel image at /var/lib/ussycode/vmlinux
	// - ussyuntu rootfs image available
	// - bridge interface configured
	// - SQLite database

	// TODO: Set up full VM manager with real database and firecracker,
	// create a VM, verify it starts, then stop and destroy.
	t.Log("VM create-and-start integration test stub (needs real firecracker)")
	t.Skip("not yet implemented — requires firecracker + kernel + rootfs")
}
