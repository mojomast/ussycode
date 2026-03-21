// Package ssh implements the ussycode SSH gateway server.
// Users SSH into this server and interact with a custom shell,
// not a system shell. The gateway identifies users by their SSH
// public key fingerprint and routes them to registration or
// the main shell accordingly.
package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gssh "github.com/gliderlabs/ssh"
	"github.com/mojomast/ussycode/internal/db"
	"github.com/mojomast/ussycode/internal/gateway"
	"github.com/mojomast/ussycode/internal/proxy"
	"github.com/mojomast/ussycode/internal/vm"
	gossh "golang.org/x/crypto/ssh"
)

// Context key types to avoid collisions.
type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyFingerprint
	ctxKeyPublicKey
)

// Gateway is the SSH gateway server.
type Gateway struct {
	DB          *db.DB
	VM          *vm.Manager
	Metadata    *gateway.Server
	Proxy       *proxy.Manager
	LLMGateway  gateway.LLMGateway
	server      *gssh.Server
	hostSigner  gossh.Signer
	hostKeyPath string
	domain      string

	// RoutussyURL is the base URL of the Routussy proxy for SSH key validation
	// and API key lookup. If empty, all SSH keys are accepted (legacy behavior).
	RoutussyURL string

	// RoutussyInternalKey is the shared secret for authenticating to Routussy
	// internal API endpoints. Sent as Bearer token.
	RoutussyInternalKey string
}

// New creates a new SSH gateway. If the host key file doesn't exist,
// it generates a new ed25519 key.
func New(database *db.DB, vmManager *vm.Manager, metaSrv *gateway.Server, proxyMgr *proxy.Manager, hostKeyPath, addr, domain string) (*Gateway, error) {
	g := &Gateway{
		DB:          database,
		VM:          vmManager,
		Metadata:    metaSrv,
		Proxy:       proxyMgr,
		hostKeyPath: hostKeyPath,
		domain:      domain,
	}

	if domain == "" {
		g.domain = "ussy.host"
	}

	// Load or generate host key
	signer, err := g.loadOrGenerateHostKey()
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	g.hostSigner = signer

	g.server = &gssh.Server{
		Addr:             addr,
		Handler:          g.sessionHandler,
		PublicKeyHandler: g.publicKeyHandler,
		HostSigners:      []gssh.Signer{signer},
	}

	return g, nil
}

func (g *Gateway) loadOrGenerateHostKey() (gssh.Signer, error) {
	data, err := os.ReadFile(g.hostKeyPath)
	if err == nil {
		signer, err := gossh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse host key: %w", err)
		}
		log.Printf("loaded host key from %s", g.hostKeyPath)
		return signer, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read host key: %w", err)
	}

	// Generate new ed25519 key
	log.Printf("generating new ed25519 host key at %s", g.hostKeyPath)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	// Marshal to OpenSSH PEM format
	pemBlock, err := gossh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}

	pemData := pem.EncodeToMemory(pemBlock)
	if err := os.WriteFile(g.hostKeyPath, pemData, 0600); err != nil {
		return nil, fmt.Errorf("write host key: %w", err)
	}

	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("create signer: %w", err)
	}

	return signer, nil
}

// publicKeyHandler is called for each SSH public key auth attempt.
//
// Behavior depends on whether Routussy integration is configured:
//
//  1. If RoutussyURL is set and the connection is NOT from a Tailscale IP
//     (100.x.y.z), the fingerprint is validated against Routussy's authorized
//     key database. Unknown keys are rejected.
//
//  2. If the connection IS from a Tailscale IP, or RoutussyURL is not configured,
//     all keys are accepted (legacy/internal behavior). Unknown users will see
//     the registration flow in the session handler.
func (g *Gateway) publicKeyHandler(ctx gssh.Context, key gssh.PublicKey) bool {
	fingerprint := gossh.FingerprintSHA256(key)
	remoteAddr := ctx.RemoteAddr().String()
	remoteIP, _, _ := net.SplitHostPort(remoteAddr)

	log.Printf("[auth] publicKeyHandler called for fingerprint=%s remote=%s", fingerprint, remoteIP)

	// Look up user in ussycode's local DB first
	user, err := g.DB.UserByFingerprint(context.Background(), fingerprint)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[auth] error looking up user by fingerprint %s: %v", fingerprint, err)
		} else {
			log.Printf("[auth] no user found for fingerprint=%s (new user)", fingerprint)
		}
	} else {
		log.Printf("[auth] found existing user: %s (id=%d) for fingerprint=%s", user.Handle, user.ID, fingerprint)
		ctx.SetValue(ctxKeyUser, user)
	}

	ctx.SetValue(ctxKeyFingerprint, fingerprint)
	ctx.SetValue(ctxKeyPublicKey, key)

	// If routussy integration is configured, validate non-tailscale connections
	if g.RoutussyURL != "" && !isTailscaleIP(remoteIP) {
		if user != nil {
			// Known local user — allow
			return true
		}

		// Unknown user from non-tailscale IP: check with routussy
		authorized := g.checkRoutussyFingerprint(fingerprint)
		if !authorized {
			log.Printf("[auth] REJECTED: fingerprint=%s from non-tailscale IP=%s not in routussy", fingerprint, remoteIP)
			return false
		}
		log.Printf("[auth] routussy authorized fingerprint=%s from IP=%s", fingerprint, remoteIP)
	}

	return true
}

// isTailscaleIP checks if an IP is in the Tailscale CGNAT range (100.64.0.0/10).
func isTailscaleIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	// Tailscale uses 100.64.0.0/10 (CGNAT range)
	_, tailscaleNet, _ := net.ParseCIDR("100.64.0.0/10")
	return tailscaleNet.Contains(parsed)
}

// routussyUserResponse is the response from routussy's /ussycode/user-by-fingerprint endpoint.
type routussyUserResponse struct {
	UserID       string `json:"user_id"`
	DiscordID    string `json:"discord_id"`
	BudgetCents  int    `json:"budget_cents"`
	SpentCents   int    `json:"spent_cents"`
	SSHPubkey    string `json:"ssh_pubkey"`
	APIKeyPrefix string `json:"api_key_prefix"`
}

// checkRoutussyFingerprint queries the Routussy API to verify an SSH fingerprint.
func (g *Gateway) checkRoutussyFingerprint(fingerprint string) bool {
	reqURL := fmt.Sprintf("%s/ussycode/user-by-fingerprint?fingerprint=%s",
		strings.TrimRight(g.RoutussyURL, "/"), url.QueryEscape(fingerprint))

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		log.Printf("[auth] routussy request error: %v", err)
		return false
	}

	if g.RoutussyInternalKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.RoutussyInternalKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[auth] routussy request failed: %v", err)
		// On error, fail open for now to avoid locking out all users
		// if routussy is temporarily down
		return true
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("[auth] routussy returned status %d: %s", resp.StatusCode, string(body))
		// Fail open on unexpected errors
		return true
	}

	return true
}

// LookupRoutussyUser queries routussy for a user by SSH fingerprint and returns
// their metadata. Used to inject API keys into VM environments.
func (g *Gateway) LookupRoutussyUser(fingerprint string) (*routussyUserResponse, error) {
	if g.RoutussyURL == "" {
		return nil, fmt.Errorf("routussy URL not configured")
	}

	reqURL := fmt.Sprintf("%s/ussycode/user-by-fingerprint?fingerprint=%s",
		strings.TrimRight(g.RoutussyURL, "/"), url.QueryEscape(fingerprint))

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if g.RoutussyInternalKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.RoutussyInternalKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var user routussyUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &user, nil
}

// sessionHandler is the main entry point for each SSH session.
func (g *Gateway) sessionHandler(session gssh.Session) {
	defer session.Exit(0)

	user, _ := session.Context().Value(ctxKeyUser).(*db.User)
	fingerprint, _ := session.Context().Value(ctxKeyFingerprint).(string)

	log.Printf("[session] new session: fingerprint=%s, hasUser=%v", fingerprint, user != nil)

	// If no user found, run registration
	if user == nil {
		var err error
		user, err = g.handleRegistration(session)
		if err != nil {
			log.Printf("[session] registration failed: %v", err)
			fmt.Fprintf(session, "\r\nerror during registration: %v\r\n", err)
			return
		}
		if user == nil {
			// User cancelled registration
			fmt.Fprintf(session, "\r\ngoodbye.\r\n")
			return
		}
		log.Printf("[session] registration succeeded: user=%s (id=%d)", user.Handle, user.ID)
	}

	// Handle non-interactive commands (ssh ussy.host <command>)
	cmd := session.Command()
	if len(cmd) > 0 {
		shell := &Shell{
			gw:      g,
			session: session,
			user:    user,
		}
		shell.execCommand(cmd)
		return
	}

	// Interactive session: launch the shell
	shell := &Shell{
		gw:      g,
		session: session,
		user:    user,
	}

	if err := shell.Run(); err != nil {
		log.Printf("shell error for %s: %v", user.Handle, err)
	}
}

// ListenAndServe starts the SSH server.
func (g *Gateway) ListenAndServe() error {
	log.Printf("SSH gateway listening on %s", g.server.Addr)
	return g.server.ListenAndServe()
}

// Shutdown gracefully shuts down the SSH server.
func (g *Gateway) Shutdown(ctx context.Context) error {
	return g.server.Shutdown(ctx)
}
