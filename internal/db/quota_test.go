package db

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func setupQuotaTestDB(t *testing.T) *DB {
	t.Helper()
	f, err := os.CreateTemp("", "ussycode-quota-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
	})

	database, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

func TestGetTrustLimits(t *testing.T) {
	tests := []struct {
		level     string
		vmLimit   int
		cpuLimit  int
		ramLimit  int
		diskLimit int
	}{
		{"newbie", 3, 1, 2048, 5120},
		{"citizen", 10, 4, 8192, 25600},
		{"operator", 25, 8, 16384, 102400},
		{"admin", -1, -1, -1, -1},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			limits := GetTrustLimits(tt.level)
			if limits.VMLimit != tt.vmLimit {
				t.Errorf("VMLimit: got %d, want %d", limits.VMLimit, tt.vmLimit)
			}
			if limits.CPULimit != tt.cpuLimit {
				t.Errorf("CPULimit: got %d, want %d", limits.CPULimit, tt.cpuLimit)
			}
			if limits.RAMLimit != tt.ramLimit {
				t.Errorf("RAMLimit: got %d, want %d", limits.RAMLimit, tt.ramLimit)
			}
			if limits.DiskLimit != tt.diskLimit {
				t.Errorf("DiskLimit: got %d, want %d", limits.DiskLimit, tt.diskLimit)
			}
		})
	}

	// Unknown level should return newbie defaults
	t.Run("unknown", func(t *testing.T) {
		limits := GetTrustLimits("unknown")
		if limits.VMLimit != 3 {
			t.Errorf("unknown level VMLimit: got %d, want 3", limits.VMLimit)
		}
	})
}

func TestIsValidTrustLevel(t *testing.T) {
	valid := []string{"newbie", "citizen", "operator", "admin"}
	for _, level := range valid {
		if !IsValidTrustLevel(level) {
			t.Errorf("expected %q to be valid", level)
		}
	}

	invalid := []string{"", "root", "superadmin", "ADMIN", "Newbie"}
	for _, level := range invalid {
		if IsValidTrustLevel(level) {
			t.Errorf("expected %q to be invalid", level)
		}
	}
}

func TestGetUserTrustLevel(t *testing.T) {
	db := setupQuotaTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "trusttest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Default should be newbie
	level, err := db.GetUserTrustLevel(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserTrustLevel: %v", err)
	}
	if level != "newbie" {
		t.Errorf("expected 'newbie', got %q", level)
	}
}

func TestSetUserTrustLevel(t *testing.T) {
	db := setupQuotaTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "trustset")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Upgrade to citizen
	if err := db.SetUserTrustLevel(ctx, user.ID, "citizen"); err != nil {
		t.Fatalf("SetUserTrustLevel: %v", err)
	}

	level, err := db.GetUserTrustLevel(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserTrustLevel: %v", err)
	}
	if level != "citizen" {
		t.Errorf("expected 'citizen', got %q", level)
	}

	// Verify quotas were updated to match
	quotas, err := db.GetUserQuotas(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserQuotas: %v", err)
	}
	citizenLimits := GetTrustLimits("citizen")
	if quotas.VMLimit != citizenLimits.VMLimit {
		t.Errorf("VMLimit: got %d, want %d", quotas.VMLimit, citizenLimits.VMLimit)
	}
	if quotas.CPULimit != citizenLimits.CPULimit {
		t.Errorf("CPULimit: got %d, want %d", quotas.CPULimit, citizenLimits.CPULimit)
	}

	// Upgrade to admin
	if err := db.SetUserTrustLevel(ctx, user.ID, "admin"); err != nil {
		t.Fatalf("SetUserTrustLevel admin: %v", err)
	}
	quotas, err = db.GetUserQuotas(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserQuotas admin: %v", err)
	}
	if quotas.VMLimit != -1 {
		t.Errorf("admin VMLimit: got %d, want -1 (unlimited)", quotas.VMLimit)
	}

	// Invalid level should error
	if err := db.SetUserTrustLevel(ctx, user.ID, "superuser"); err == nil {
		t.Error("expected error for invalid trust level")
	}
}

func TestGetUserVMCount(t *testing.T) {
	db := setupQuotaTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "vmcounttest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	count, err := db.GetUserVMCount(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserVMCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Create some VMs
	for i := 0; i < 3; i++ {
		_, err := db.CreateVM(ctx, user.ID, fmt.Sprintf("vm-%d", i), "ussyuntu", 1, 512, 5)
		if err != nil {
			t.Fatalf("CreateVM %d: %v", i, err)
		}
	}

	count, err = db.GetUserVMCount(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserVMCount after create: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestQuotaEnforcement(t *testing.T) {
	// Test the quota logic that cmdNew uses (without the SSH shell).
	// This verifies the data layer correctly supports quota checks.
	db := setupQuotaTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "quotauser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Default is newbie with VM limit of 3
	limits := GetTrustLimits(user.TrustLevel)
	if limits.VMLimit != 3 {
		t.Fatalf("expected newbie VM limit 3, got %d", limits.VMLimit)
	}

	// Create VMs up to the limit
	for i := 0; i < limits.VMLimit; i++ {
		_, err := db.CreateVM(ctx, user.ID, fmt.Sprintf("quota-vm-%d", i), "ussyuntu", 1, 512, 5)
		if err != nil {
			t.Fatalf("CreateVM %d: %v", i, err)
		}
	}

	// Verify count matches limit
	count, err := db.GetUserVMCount(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserVMCount: %v", err)
	}
	if count != limits.VMLimit {
		t.Fatalf("expected count %d, got %d", limits.VMLimit, count)
	}

	// Simulating quota check: should reject
	if limits.VMLimit >= 0 && count >= limits.VMLimit {
		t.Log("correctly detected: VM limit reached")
	} else {
		t.Error("quota check failed: should have been at limit")
	}

	// Upgrade to citizen and verify quota expands
	if err := db.SetUserTrustLevel(ctx, user.ID, "citizen"); err != nil {
		t.Fatalf("SetUserTrustLevel: %v", err)
	}
	citizenLimits := GetTrustLimits("citizen")
	if count < citizenLimits.VMLimit {
		t.Log("correctly detected: citizen has room for more VMs")
	} else {
		t.Error("citizen should have a higher VM limit than current count")
	}

	// Admin has unlimited (-1)
	if err := db.SetUserTrustLevel(ctx, user.ID, "admin"); err != nil {
		t.Fatalf("SetUserTrustLevel admin: %v", err)
	}
	adminLimits := GetTrustLimits("admin")
	if adminLimits.VMLimit < 0 {
		t.Log("correctly detected: admin has unlimited VMs")
	} else {
		t.Error("admin should have unlimited VM limit")
	}

	// Delete a VM, verify count decreases
	vms, err := db.VMsByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("VMsByUser: %v", err)
	}
	if err := db.DeleteVM(ctx, vms[0].ID); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}

	count, err = db.GetUserVMCount(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserVMCount after delete: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 after delete, got %d", count)
	}
}

func TestGetUserQuotas(t *testing.T) {
	db := setupQuotaTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "quotastest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	quotas, err := db.GetUserQuotas(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserQuotas: %v", err)
	}

	// Should match newbie defaults
	if quotas.Level != "newbie" {
		t.Errorf("Level: got %q, want 'newbie'", quotas.Level)
	}
	if quotas.VMLimit != 3 {
		t.Errorf("VMLimit: got %d, want 3", quotas.VMLimit)
	}
	if quotas.CPULimit != 1 {
		t.Errorf("CPULimit: got %d, want 1", quotas.CPULimit)
	}
	if quotas.RAMLimit != 2048 {
		t.Errorf("RAMLimit: got %d, want 2048", quotas.RAMLimit)
	}
	if quotas.DiskLimit != 5120 {
		t.Errorf("DiskLimit: got %d, want 5120", quotas.DiskLimit)
	}
}
