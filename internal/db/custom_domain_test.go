package db

import (
	"context"
	"database/sql"
	"testing"
)

func setupCustomDomainTestDB(t *testing.T) *DB {
	t.Helper()
	return setupQuotaTestDB(t) // reuse the same setup
}

func TestCreateCustomDomain(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "domainuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := db.CreateVM(ctx, user.ID, "domain-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Create a custom domain
	err = db.CreateCustomDomain(ctx, vm.ID, "myapp.example.com", "verify-token-123")
	if err != nil {
		t.Fatalf("CreateCustomDomain: %v", err)
	}

	// Verify it was created
	cd, err := db.GetCustomDomain(ctx, "myapp.example.com")
	if err != nil {
		t.Fatalf("GetCustomDomain: %v", err)
	}
	if cd.Domain != "myapp.example.com" {
		t.Errorf("Domain: got %q, want %q", cd.Domain, "myapp.example.com")
	}
	if cd.VMID != vm.ID {
		t.Errorf("VMID: got %d, want %d", cd.VMID, vm.ID)
	}
	if cd.Verified {
		t.Error("expected Verified to be false")
	}
	if cd.VerificationToken.String != "verify-token-123" {
		t.Errorf("VerificationToken: got %q, want %q", cd.VerificationToken.String, "verify-token-123")
	}
}

func TestCreateCustomDomain_Duplicate(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "dupuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := db.CreateVM(ctx, user.ID, "dup-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Create first domain
	err = db.CreateCustomDomain(ctx, vm.ID, "dup.example.com", "token-1")
	if err != nil {
		t.Fatalf("CreateCustomDomain first: %v", err)
	}

	// Try to create duplicate — should fail (UNIQUE constraint)
	err = db.CreateCustomDomain(ctx, vm.ID, "dup.example.com", "token-2")
	if err == nil {
		t.Error("expected error for duplicate domain, got nil")
	}
}

func TestVerifyCustomDomain(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "verifyuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := db.CreateVM(ctx, user.ID, "verify-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	err = db.CreateCustomDomain(ctx, vm.ID, "verified.example.com", "verify-token")
	if err != nil {
		t.Fatalf("CreateCustomDomain: %v", err)
	}

	// Verify the domain
	err = db.VerifyCustomDomain(ctx, "verified.example.com")
	if err != nil {
		t.Fatalf("VerifyCustomDomain: %v", err)
	}

	// Check it's now verified
	cd, err := db.GetCustomDomain(ctx, "verified.example.com")
	if err != nil {
		t.Fatalf("GetCustomDomain: %v", err)
	}
	if !cd.Verified {
		t.Error("expected Verified to be true")
	}
	if cd.VerifiedAt.Time.IsZero() {
		t.Error("expected VerifiedAt to be set")
	}
}

func TestListCustomDomains(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "listuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := db.CreateVM(ctx, user.ID, "list-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Create multiple domains
	domains := []string{"a.example.com", "b.example.com", "c.example.com"}
	for i, d := range domains {
		err := db.CreateCustomDomain(ctx, vm.ID, d, "token-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("CreateCustomDomain %s: %v", d, err)
		}
	}

	// List them
	listed, err := db.ListCustomDomains(ctx, vm.ID)
	if err != nil {
		t.Fatalf("ListCustomDomains: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 domains, got %d", len(listed))
	}

	// List for a different VM should return empty
	vm2, err := db.CreateVM(ctx, user.ID, "other-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM other: %v", err)
	}
	listed2, err := db.ListCustomDomains(ctx, vm2.ID)
	if err != nil {
		t.Fatalf("ListCustomDomains other: %v", err)
	}
	if len(listed2) != 0 {
		t.Errorf("expected 0 domains for other VM, got %d", len(listed2))
	}
}

func TestDeleteCustomDomain(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "deleteuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := db.CreateVM(ctx, user.ID, "del-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	err = db.CreateCustomDomain(ctx, vm.ID, "delete-me.example.com", "token-del")
	if err != nil {
		t.Fatalf("CreateCustomDomain: %v", err)
	}

	// Verify it exists
	_, err = db.GetCustomDomain(ctx, "delete-me.example.com")
	if err != nil {
		t.Fatalf("GetCustomDomain before delete: %v", err)
	}

	// Delete it
	err = db.DeleteCustomDomain(ctx, "delete-me.example.com")
	if err != nil {
		t.Fatalf("DeleteCustomDomain: %v", err)
	}

	// Verify it's gone
	_, err = db.GetCustomDomain(ctx, "delete-me.example.com")
	if err == nil {
		t.Error("expected error after deleting domain, got nil")
	}
}

func TestGetCustomDomain_NotFound(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	_, err := db.GetCustomDomain(ctx, "nonexistent.example.com")
	if err == nil {
		t.Error("expected error for nonexistent domain, got nil")
	}
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestCustomDomain_CascadeDelete(t *testing.T) {
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "cascadeuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := db.CreateVM(ctx, user.ID, "cascade-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	err = db.CreateCustomDomain(ctx, vm.ID, "cascade.example.com", "token-cascade")
	if err != nil {
		t.Fatalf("CreateCustomDomain: %v", err)
	}

	// Delete the VM — custom domain should be cascade-deleted
	err = db.DeleteVM(ctx, vm.ID)
	if err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}

	// The custom domain should be gone
	_, err = db.GetCustomDomain(ctx, "cascade.example.com")
	if err == nil {
		t.Error("expected custom domain to be cascade-deleted with VM")
	}
}

func TestIsValidDomainLogic(t *testing.T) {
	// Test the domain validation helper logic directly.
	// Note: isValidDomain is in commands.go (ssh package), so we test
	// the DB layer constraints instead. This test validates the
	// domain UNIQUE constraint works as expected.
	db := setupCustomDomainTestDB(t)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "validuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm1, err := db.CreateVM(ctx, user.ID, "valid-vm-one", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM 1: %v", err)
	}
	vm2, err := db.CreateVM(ctx, user.ID, "valid-vm-two", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM 2: %v", err)
	}

	// Same domain on different VMs should fail
	err = db.CreateCustomDomain(ctx, vm1.ID, "shared.example.com", "token-1")
	if err != nil {
		t.Fatalf("CreateCustomDomain first: %v", err)
	}

	err = db.CreateCustomDomain(ctx, vm2.ID, "shared.example.com", "token-2")
	if err == nil {
		t.Error("expected UNIQUE constraint error for same domain on different VMs")
	}
}
