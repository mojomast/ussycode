// Package auth implements SSH key-based authentication and
// stateless token signing/verification for ussycode.
//
// Tokens use the format: base64url(JSON_payload).base64url(ssh_wire_signature)
// Signed with SSH private keys, verified with SSH public keys.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// TokenPayload is the claims embedded in a signed token.
type TokenPayload struct {
	Subject   string   `json:"sub"`             // user handle
	IssuedAt  int64    `json:"iat"`             // unix timestamp
	ExpiresAt int64    `json:"exp"`             // unix timestamp
	NotBefore int64    `json:"nbf"`             // unix timestamp
	Perms     []string `json:"perms,omitempty"` // permission scopes
	Nonce     string   `json:"nonce"`           // replay prevention
}

// SignToken creates a signed token string using an SSH private key.
// Format: base64url(json_payload).base64url(ssh_signature)
func SignToken(signer ssh.Signer, subject string, ttl time.Duration, perms []string) (string, error) {
	now := time.Now().UTC()

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	payload := TokenPayload{
		Subject:   subject,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		NotBefore: now.Unix(),
		Perms:     perms,
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce),
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	sig, err := signer.Sign(rand.Reader, payloadJSON)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	sigBytes := ssh.Marshal(sig)
	sigB64 := base64.RawURLEncoding.EncodeToString(sigBytes)

	return payloadB64 + "." + sigB64, nil
}

// VerifyToken verifies a signed token against a set of trusted public keys.
// Returns the payload if valid, or an error if verification fails.
func VerifyToken(token string, trustedKeys []ssh.PublicKey) (*TokenPayload, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid token format: expected payload.signature")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	var sig ssh.Signature
	if err := ssh.Unmarshal(sigBytes, &sig); err != nil {
		return nil, fmt.Errorf("unmarshal signature: %w", err)
	}

	// Try each trusted key
	var verified bool
	for _, key := range trustedKeys {
		if err := key.Verify(payloadJSON, &sig); err == nil {
			verified = true
			break
		}
	}
	if !verified {
		return nil, fmt.Errorf("signature verification failed: no matching key")
	}

	var payload TokenPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	// Check temporal validity
	now := time.Now().UTC().Unix()
	if now < payload.NotBefore {
		return nil, fmt.Errorf("token not yet valid (nbf: %d, now: %d)", payload.NotBefore, now)
	}
	if now > payload.ExpiresAt {
		return nil, fmt.Errorf("token expired (exp: %d, now: %d)", payload.ExpiresAt, now)
	}

	return &payload, nil
}

// GenerateHandle creates a random short handle for token storage.
// Returns a 22-character base64url string (16 bytes of entropy).
func GenerateHandle() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// FingerprintKey returns the SHA256 fingerprint of an SSH public key.
func FingerprintKey(key ssh.PublicKey) string {
	return ssh.FingerprintSHA256(key)
}

// ParsePublicKey parses an SSH public key from authorized_keys format.
func ParsePublicKey(authorizedKey string) (ssh.PublicKey, string, error) {
	key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return nil, "", fmt.Errorf("parse public key: %w", err)
	}
	return key, comment, nil
}
