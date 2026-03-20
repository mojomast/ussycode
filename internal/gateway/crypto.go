// Package gateway implements the metadata service and gateway proxies
// available inside VMs at http://169.254.169.254/.
package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// KeyEncryptor handles encryption/decryption of API keys using AES-256-GCM.
// The encryption key is derived from a server-side secret using SHA-256.
type KeyEncryptor struct {
	aead cipher.AEAD
}

// NewKeyEncryptor creates a new encryptor from a server-side secret.
// The secret is hashed with SHA-256 to produce a 32-byte AES-256 key.
func NewKeyEncryptor(secret string) (*KeyEncryptor, error) {
	if secret == "" {
		return nil, fmt.Errorf("encryption secret must not be empty")
	}

	// Derive a 32-byte key from the secret using SHA-256
	hash := sha256.Sum256([]byte(secret))

	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	return &KeyEncryptor{aead: aead}, nil
}

// Encrypt encrypts plaintext and returns a base64-encoded ciphertext.
// Each call generates a random nonce, so encrypting the same plaintext
// produces different ciphertext.
func (e *KeyEncryptor) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends the ciphertext to nonce, so result = nonce + ciphertext + tag
	ciphertext := e.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decodes base64 ciphertext and decrypts it.
func (e *KeyEncryptor) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	nonceSize := e.aead.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]

	plaintext, err := e.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
