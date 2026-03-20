// Package ssh implements the exedevussy SSH gateway server.
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
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"

	gssh "github.com/gliderlabs/ssh"
	"github.com/mojomast/exedevussy/internal/db"
	"github.com/mojomast/exedevussy/internal/gateway"
	"github.com/mojomast/exedevussy/internal/proxy"
	"github.com/mojomast/exedevussy/internal/vm"
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
	server      *gssh.Server
	hostKeyPath string
	domain      string
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
// We always accept the key — user lookup and registration happens
// in the session handler. This allows new users to connect.
func (g *Gateway) publicKeyHandler(ctx gssh.Context, key gssh.PublicKey) bool {
	fingerprint := gossh.FingerprintSHA256(key)
	log.Printf("[auth] publicKeyHandler called for fingerprint=%s", fingerprint)

	user, err := g.DB.UserByFingerprint(context.Background(), fingerprint)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[auth] error looking up user by fingerprint %s: %v", fingerprint, err)
		} else {
			log.Printf("[auth] no user found for fingerprint=%s (new user)", fingerprint)
		}
		// Not found or error — still accept the key so we can show
		// registration flow or error in the session handler.
	} else {
		log.Printf("[auth] found existing user: %s (id=%d) for fingerprint=%s", user.Handle, user.ID, fingerprint)
		ctx.SetValue(ctxKeyUser, user)
	}

	ctx.SetValue(ctxKeyFingerprint, fingerprint)
	ctx.SetValue(ctxKeyPublicKey, key)
	return true
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
