// Package pki provides Ed25519-based certificate authority functionality for
// the ussyverse server pool. It generates root and intermediate CAs, issues
// short-lived node certificates, and creates signed join tokens.
package pki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// CA (Certificate Authority)
// ---------------------------------------------------------------------------

// CA is an Ed25519-based certificate authority that can issue node certs and
// join tokens. It holds a root CA and an intermediate CA for signing.
type CA struct {
	mu sync.Mutex

	rootCert    *x509.Certificate
	rootKey     ed25519.PrivateKey
	rootCertPEM []byte

	interCert    *x509.Certificate
	interKey     ed25519.PrivateKey
	interCertPEM []byte

	// usedTokens tracks single-use join tokens that have been consumed.
	usedTokens map[string]bool

	logger *slog.Logger
}

// NewCA generates a fresh root CA and intermediate CA.
func NewCA() (*CA, error) {
	logger := slog.Default().With("component", "pki")

	// Generate root CA.
	rootCert, rootKey, rootPEM, err := generateRootCA()
	if err != nil {
		return nil, fmt.Errorf("generating root CA: %w", err)
	}
	logger.Info("generated root CA", "subject", rootCert.Subject.CommonName)

	// Generate intermediate CA signed by root.
	interCert, interKey, interPEM, err := generateIntermediateCA(rootCert, rootKey)
	if err != nil {
		return nil, fmt.Errorf("generating intermediate CA: %w", err)
	}
	logger.Info("generated intermediate CA", "subject", interCert.Subject.CommonName)

	return &CA{
		rootCert:     rootCert,
		rootKey:      rootKey,
		rootCertPEM:  rootPEM,
		interCert:    interCert,
		interKey:     interKey,
		interCertPEM: interPEM,
		usedTokens:   make(map[string]bool),
		logger:       logger,
	}, nil
}

// LoadCA creates a CA from existing PEM-encoded root and intermediate
// certificates and keys.
func LoadCA(rootCertPEM, rootKeyPEM, interCertPEM, interKeyPEM []byte) (*CA, error) {
	rootCert, err := parseCertPEM(rootCertPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing root cert: %w", err)
	}
	rootKey, err := parseEd25519KeyPEM(rootKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing root key: %w", err)
	}
	interCert, err := parseCertPEM(interCertPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing intermediate cert: %w", err)
	}
	interKey, err := parseEd25519KeyPEM(interKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing intermediate key: %w", err)
	}

	return &CA{
		rootCert:     rootCert,
		rootKey:      rootKey,
		rootCertPEM:  rootCertPEM,
		interCert:    interCert,
		interKey:     interKey,
		interCertPEM: interCertPEM,
		usedTokens:   make(map[string]bool),
		logger:       slog.Default().With("component", "pki"),
	}, nil
}

// RootCertPEM returns the PEM-encoded root CA certificate.
func (ca *CA) RootCertPEM() []byte { return ca.rootCertPEM }

// IntermediateCertPEM returns the PEM-encoded intermediate CA certificate.
func (ca *CA) IntermediateCertPEM() []byte { return ca.interCertPEM }

// ---------------------------------------------------------------------------
// Node certificate issuance
// ---------------------------------------------------------------------------

// NodeCertificate holds the PEM-encoded certificate and key for a node.
type NodeCertificate struct {
	CertPEM []byte
	KeyPEM  []byte
	CAPEM   []byte // intermediate + root chain
}

// IssueNodeCert creates a short-lived TLS certificate for a node.
// The certificate is valid for the given duration (typically 24h) and is
// signed by the intermediate CA.
func (ca *CA) IssueNodeCert(nodeID string, pubKey ed25519.PublicKey, validity time.Duration) (*NodeCertificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	if validity <= 0 {
		validity = 24 * time.Hour
	}

	serialNumber, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("node-%s", nodeID),
			Organization: []string{"ussyverse"},
		},
		NotBefore:             now.Add(-5 * time.Minute), // clock skew tolerance
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.interCert, pubKey, ca.interKey)
	if err != nil {
		return nil, fmt.Errorf("creating node certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Build the CA chain: intermediate + root.
	caPEM := append([]byte{}, ca.interCertPEM...)
	caPEM = append(caPEM, ca.rootCertPEM...)

	ca.logger.Info("issued node certificate",
		"node_id", nodeID,
		"serial", serialNumber.String(),
		"expires", now.Add(validity).Format(time.RFC3339),
	)

	return &NodeCertificate{
		CertPEM: certPEM,
		CAPEM:   caPEM,
	}, nil
}

// VerifyNodeCert verifies a PEM-encoded node certificate against the CA chain.
func (ca *CA) VerifyNodeCert(certPEM []byte) (*x509.Certificate, error) {
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate: %w", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.rootCert)

	intermediates := x509.NewCertPool()
	intermediates.AddCert(ca.interCert)

	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	if _, err := cert.Verify(opts); err != nil {
		return nil, fmt.Errorf("certificate verification failed: %w", err)
	}

	return cert, nil
}

// ---------------------------------------------------------------------------
// Join tokens
// ---------------------------------------------------------------------------

// JoinToken is a time-limited, single-use token for node registration.
type JoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Signature []byte    `json:"signature"`
}

// GenerateJoinToken creates a signed, time-limited join token. The token is
// valid for the specified duration and can only be used once.
func (ca *CA) GenerateJoinToken(validity time.Duration) (*JoinToken, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generating random token: %w", err)
	}

	token := fmt.Sprintf("%x", tokenBytes)
	expiresAt := time.Now().UTC().Add(validity)

	// Sign: token + expiry as the message.
	message := []byte(fmt.Sprintf("%s:%d", token, expiresAt.Unix()))
	sig := ed25519.Sign(ca.rootKey, message)

	ca.logger.Info("generated join token",
		"expires_at", expiresAt.Format(time.RFC3339),
	)

	return &JoinToken{
		Token:     token,
		ExpiresAt: expiresAt,
		Signature: sig,
	}, nil
}

// ValidateJoinToken verifies a join token's signature, expiry, and single-use
// status. Returns nil if the token is valid and marks it as consumed.
func (ca *CA) ValidateJoinToken(jt *JoinToken) error {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	// Check single-use.
	if ca.usedTokens[jt.Token] {
		return fmt.Errorf("join token already used")
	}

	// Check expiry.
	if time.Now().UTC().After(jt.ExpiresAt) {
		return fmt.Errorf("join token expired at %s", jt.ExpiresAt.Format(time.RFC3339))
	}

	// Verify signature.
	message := []byte(fmt.Sprintf("%s:%d", jt.Token, jt.ExpiresAt.Unix()))
	if !ed25519.Verify(ca.rootKey.Public().(ed25519.PublicKey), message, jt.Signature) {
		return fmt.Errorf("join token signature invalid")
	}

	// Mark as used.
	ca.usedTokens[jt.Token] = true

	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func generateRootCA() (*x509.Certificate, ed25519.PrivateKey, []byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "ussyverse Root CA",
			Organization: []string{"ussyverse"},
		},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return nil, nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, priv, certPEM, nil
}

func generateIntermediateCA(rootCert *x509.Certificate, rootKey ed25519.PrivateKey) (*x509.Certificate, ed25519.PrivateKey, []byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "ussyverse Intermediate CA",
			Organization: []string{"ussyverse"},
		},
		NotBefore:             now,
		NotAfter:              now.Add(5 * 365 * 24 * time.Hour), // 5 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, rootCert, pub, rootKey)
	if err != nil {
		return nil, nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, priv, certPEM, nil
}

func randomSerial() (*big.Int, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialLimit)
}

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("no CERTIFICATE PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseEd25519KeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PKCS8 key: %w", err)
	}

	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not Ed25519, got %T", key)
	}
	return edKey, nil
}

// EncodePEM helpers for persistence.

// EncodePrivateKeyPEM encodes an Ed25519 private key to PEM.
func EncodePrivateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ExportRootKeyPEM returns the PEM-encoded root CA private key (for backup).
func (ca *CA) ExportRootKeyPEM() ([]byte, error) {
	return EncodePrivateKeyPEM(ca.rootKey)
}

// ExportIntermediateKeyPEM returns the PEM-encoded intermediate CA private key.
func (ca *CA) ExportIntermediateKeyPEM() ([]byte, error) {
	return EncodePrivateKeyPEM(ca.interKey)
}

// RootPublicKey returns the root CA's public key.
func (ca *CA) RootPublicKey() crypto.PublicKey {
	return ca.rootKey.Public()
}
