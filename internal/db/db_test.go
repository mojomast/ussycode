package db

import (
	"context"
	"os"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	// Create a temp file for the database
	f, err := os.CreateTemp("", "ussycode-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	defer os.Remove(path + "-wal")
	defer os.Remove(path + "-shm")

	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Create a user
	user, err := database.CreateUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Handle != "testuser" {
		t.Errorf("expected handle 'testuser', got %q", user.Handle)
	}
	if user.TrustLevel != "newbie" {
		t.Errorf("expected trust_level 'newbie', got %q", user.TrustLevel)
	}

	// Check HandleExists
	exists, err := database.HandleExists(ctx, "testuser")
	if err != nil {
		t.Fatalf("HandleExists: %v", err)
	}
	if !exists {
		t.Error("expected handle to exist")
	}

	notExists, err := database.HandleExists(ctx, "nobody")
	if err != nil {
		t.Fatalf("HandleExists (not found): %v", err)
	}
	if notExists {
		t.Error("expected handle to not exist")
	}

	// Add SSH key
	key, err := database.AddSSHKey(ctx, user.ID, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI test@example.com", "SHA256:abc123", "test")
	if err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}
	if key.Fingerprint != "SHA256:abc123" {
		t.Errorf("expected fingerprint 'SHA256:abc123', got %q", key.Fingerprint)
	}

	// Lookup user by fingerprint
	found, err := database.UserByFingerprint(ctx, "SHA256:abc123")
	if err != nil {
		t.Fatalf("UserByFingerprint: %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("expected user ID %d, got %d", user.ID, found.ID)
	}

	// Get fingerprint by user
	fp, err := database.FingerprintByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("FingerprintByUser: %v", err)
	}
	if fp != "SHA256:abc123" {
		t.Errorf("expected fingerprint 'SHA256:abc123', got %q", fp)
	}

	// Create a VM
	vm, err := database.CreateVM(ctx, user.ID, "test-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if vm.Name != "test-vm" {
		t.Errorf("expected VM name 'test-vm', got %q", vm.Name)
	}
	if vm.Status != "stopped" {
		t.Errorf("expected VM status 'stopped', got %q", vm.Status)
	}

	// List VMs
	vms, err := database.VMsByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("VMsByUser: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(vms))
	}

	// VM count
	count, err := database.VMCountByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("VMCountByUser: %v", err)
	}
	if count != 1 {
		t.Errorf("expected VM count 1, got %d", count)
	}

	// Running count (should be 0)
	running, err := database.RunningVMCountByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("RunningVMCountByUser: %v", err)
	}
	if running != 0 {
		t.Errorf("expected 0 running VMs, got %d", running)
	}

	// Look up by name
	found2, err := database.VMByUserAndName(ctx, user.ID, "test-vm")
	if err != nil {
		t.Fatalf("VMByUserAndName: %v", err)
	}
	if found2.ID != vm.ID {
		t.Errorf("expected VM ID %d, got %d", vm.ID, found2.ID)
	}

	// Tags
	if err := database.AddTag(ctx, vm.ID, "dev"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if err := database.AddTag(ctx, vm.ID, "test"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}

	tags, err := database.TagsByVM(ctx, vm.ID)
	if err != nil {
		t.Fatalf("TagsByVM: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	// Delete VM
	if err := database.DeleteVM(ctx, vm.ID); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}

	count, err = database.VMCountByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("VMCountByUser after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 VMs after delete, got %d", count)
	}
}
