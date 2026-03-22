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
	"sync"
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
	// and API key lookup. If empty, unknown users are rejected entirely.
	RoutussyURL string

	// RoutussyInternalKey is the shared secret for authenticating to Routussy
	// internal API endpoints. Sent as Bearer token.
	RoutussyInternalKey string

	// fpCache caches successful routussy fingerprint lookups
	fpCache *fingerprintCache
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

	g.fpCache = newFingerprintCache()

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
// Behavior:
//   - Known local-DB users are always allowed.
//   - Unknown users are verified against routussy if RoutussyURL is configured.
//   - If RoutussyURL is not configured, unknown users are rejected entirely.
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

	// Known local user — always allow
	if user != nil {
		return true
	}

	// Unknown user: must be authorized by routussy
	if g.RoutussyURL == "" {
		log.Printf("[auth] REJECTED: fingerprint=%s no routussy configured, unknown users not allowed", fingerprint)
		return false
	}

	authorized := g.checkRoutussyFingerprint(fingerprint)
	if !authorized {
		log.Printf("[auth] REJECTED: fingerprint=%s from IP=%s not authorized by routussy", fingerprint, remoteIP)
		return false
	}

	provisionedUser, err := g.ensureLocalUserForRoutussy(context.Background(), fingerprint, key)
	if err != nil {
		log.Printf("[auth] REJECTED: fingerprint=%s authorized by routussy but local provisioning failed: %v", fingerprint, err)
		return false
	}

	ctx.SetValue(ctxKeyUser, provisionedUser)
	log.Printf("[auth] routussy authorized fingerprint=%s from IP=%s -> local user=%s (id=%d)", fingerprint, remoteIP, provisionedUser.Handle, provisionedUser.ID)
	return true
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

// fingerprintCache caches successful routussy fingerprint lookups to survive
// brief outages. Unknown fingerprints are never cached — they always fail.
type fingerprintCache struct {
	mu      sync.RWMutex
	entries map[string]time.Time // fingerprint -> expiry time
}

func newFingerprintCache() *fingerprintCache {
	return &fingerprintCache{
		entries: make(map[string]time.Time),
	}
}

// get returns true if the fingerprint is cached and not expired.
func (c *fingerprintCache) get(fingerprint string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	expiry, ok := c.entries[fingerprint]
	if !ok {
		return false
	}
	return time.Now().Before(expiry)
}

// set caches a successful fingerprint lookup with a TTL.
func (c *fingerprintCache) set(fingerprint string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[fingerprint] = time.Now().Add(ttl)
}

// checkRoutussyFingerprint queries the Routussy API to verify an SSH fingerprint.
// Fails closed: if Routussy is unreachable, only previously-cached fingerprints
// are allowed through. Unknown fingerprints are always rejected.
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
		log.Printf("[auth] routussy unreachable: %v", err)
		// Fail closed: only allow cached fingerprints
		if g.fpCache.get(fingerprint) {
			log.Printf("[auth] routussy down but fingerprint=%s found in cache, allowing", fingerprint)
			return true
		}
		log.Printf("[auth] routussy down and fingerprint=%s not cached, rejecting", fingerprint)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("[auth] routussy returned status %d: %s", resp.StatusCode, string(body))
		// Fail closed on unexpected status codes, check cache
		if g.fpCache.get(fingerprint) {
			log.Printf("[auth] routussy error but fingerprint=%s found in cache, allowing", fingerprint)
			return true
		}
		return false
	}

	// Success — cache this fingerprint for 10 minutes
	g.fpCache.set(fingerprint, 10*time.Minute)
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

func (g *Gateway) ensureLocalUserForRoutussy(ctx context.Context, fingerprint string, key gssh.PublicKey) (*db.User, error) {
	user, err := g.DB.UserByFingerprint(ctx, fingerprint)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lookup local user by fingerprint: %w", err)
	}

	rUser, err := g.LookupRoutussyUser(fingerprint)
	if err != nil {
		return nil, fmt.Errorf("lookup routussy user: %w", err)
	}

	handle := sanitizeHandle(rUser.DiscordID)
	if handle == "" {
		handle = "user"
	}
	handle, err = g.uniqueHandle(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("allocate handle: %w", err)
	}

	created, err := g.DB.CreateUser(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("create local user: %w", err)
	}

	pubKeyStr := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key)))
	comment := fmt.Sprintf("routussy:%s", rUser.DiscordID)
	if _, err := g.DB.AddSSHKey(ctx, created.ID, pubKeyStr, fingerprint, comment); err != nil {
		if existing, lookupErr := g.DB.UserByFingerprint(ctx, fingerprint); lookupErr == nil {
			return existing, nil
		}
		return nil, fmt.Errorf("add local ssh key: %w", err)
	}

	log.Printf("[auth] provisioned local user from routussy: handle=%s fingerprint=%s discord_id=%s", created.Handle, fingerprint, rUser.DiscordID)
	return created, nil
}

func sanitizeHandle(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	var b strings.Builder
	lastHyphen := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastHyphen = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	handle := strings.Trim(b.String(), "-")
	if handle == "" {
		return ""
	}
	if handle[0] < 'a' || handle[0] > 'z' {
		handle = "u-" + handle
	}
	if len(handle) > 20 {
		handle = strings.Trim(handle[:20], "-")
	}
	if handle == "" {
		return "user"
	}
	if last := handle[len(handle)-1]; !((last >= 'a' && last <= 'z') || (last >= '0' && last <= '9')) {
		handle = strings.TrimRight(handle, "-")
	}
	if len(handle) < 2 {
		handle = handle + "1"
	}
	return handle
}

func (g *Gateway) uniqueHandle(ctx context.Context, base string) (string, error) {
	candidate := base
	for i := 0; i < 1000; i++ {
		exists, err := g.DB.HandleExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		suffix := fmt.Sprintf("-%d", i+1)
		trimmed := base
		if len(trimmed)+len(suffix) > 20 {
			trimmed = strings.TrimRight(trimmed[:20-len(suffix)], "-")
		}
		candidate = trimmed + suffix
	}
	return "", fmt.Errorf("could not allocate unique handle for %q", base)
}

// sessionHandler is the main entry point for each SSH session.
func (g *Gateway) sessionHandler(session gssh.Session) {
	defer session.Exit(0)

	user, _ := session.Context().Value(ctxKeyUser).(*db.User)
	fingerprint, _ := session.Context().Value(ctxKeyFingerprint).(string)

	log.Printf("[session] new session: fingerprint=%s, hasUser=%v", fingerprint, user != nil)

	// If no user found, reject — registration is handled externally
	if user == nil {
		log.Printf("[session] rejected: no local account for fingerprint=%s", fingerprint)
		fmt.Fprintf(session, "\r\n  access denied.\r\n")
		fmt.Fprintf(session, "  register your SSH key at https://discord.gg/ussyverse\r\n")
		fmt.Fprintf(session, "  then try again.\r\n\r\n")
		return
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
