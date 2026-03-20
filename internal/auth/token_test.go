package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSignAndVerifyToken(t *testing.T) {
	// Generate an ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	// Sign a token
	token, err := SignToken(signer, "testuser", 5*time.Minute, []string{"read", "write"})
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	if token == "" {
		t.Fatal("empty token")
	}

	// Verify with correct key
	payload, err := VerifyToken(token, []ssh.PublicKey{sshPub})
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}

	if payload.Subject != "testuser" {
		t.Errorf("expected subject 'testuser', got %q", payload.Subject)
	}
	if len(payload.Perms) != 2 || payload.Perms[0] != "read" || payload.Perms[1] != "write" {
		t.Errorf("unexpected perms: %v", payload.Perms)
	}
	if payload.Nonce == "" {
		t.Error("expected non-empty nonce")
	}

	// Verify with wrong key should fail
	_, wrongPriv, _ := ed25519.GenerateKey(rand.Reader)
	wrongSigner, _ := ssh.NewSignerFromKey(wrongPriv)
	wrongPub := wrongSigner.PublicKey()

	_, err = VerifyToken(token, []ssh.PublicKey{wrongPub})
	if err == nil {
		t.Fatal("expected verification to fail with wrong key")
	}
}

func TestExpiredToken(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	pub := signer.PublicKey()

	// Sign a token that expires immediately (negative TTL won't work, but 0 duration)
	token, err := SignToken(signer, "testuser", -1*time.Minute, nil)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	_, err = VerifyToken(token, []ssh.PublicKey{pub})
	if err == nil {
		t.Fatal("expected expired token to fail verification")
	}
}

func TestGenerateHandle(t *testing.T) {
	handle, err := GenerateHandle()
	if err != nil {
		t.Fatalf("GenerateHandle: %v", err)
	}

	if len(handle) == 0 {
		t.Fatal("empty handle")
	}

	// Should be unique
	handle2, _ := GenerateHandle()
	if handle == handle2 {
		t.Error("expected unique handles")
	}
}

func TestFingerprintKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)

	fp := FingerprintKey(sshPub)
	if fp == "" {
		t.Error("empty fingerprint")
	}
	if len(fp) < 10 {
		t.Errorf("fingerprint too short: %q", fp)
	}
}
