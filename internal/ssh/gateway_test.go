package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mojomast/ussycode/internal/db"
	gossh "golang.org/x/crypto/ssh"
)

// setupTestDB creates a temp database for testing.
func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	f, err := os.CreateTemp("", "ussycode-ssh-test-*.db")
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

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

// generateTestKey creates an ed25519 keypair for testing.
func generateTestKey(t *testing.T) (gossh.Signer, gossh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.PublicKey()
}

// TestRegistrationPersistence verifies the hardened auth flow:
// unknown users are rejected when routussy is not configured, while
// pre-registered users can reconnect and are recognized normally.
func TestRegistrationPersistence(t *testing.T) {
	database := setupTestDB(t)

	// Create a temp host key
	hostKeyFile, err := os.CreateTemp("", "ussycode-hostkey-*")
	if err != nil {
		t.Fatal(err)
	}
	hostKeyPath := hostKeyFile.Name()
	hostKeyFile.Close()
	os.Remove(hostKeyPath) // Gateway will generate it
	t.Cleanup(func() { os.Remove(hostKeyPath) })

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Create gateway (no VM manager, no metadata server)
	gw, err := New(database, nil, nil, nil, hostKeyPath, addr, "test.local")
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	// Start gateway in background
	go func() {
		if err := gw.ListenAndServe(); err != nil {
			// Ignore errors after shutdown
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gw.Shutdown(ctx)
	})

	// Wait for server to be ready
	time.Sleep(200 * time.Millisecond)

	// Generate test client key
	clientSigner, clientPubKey := generateTestKey(t)
	fingerprint := gossh.FingerprintSHA256(clientPubKey)
	t.Logf("client fingerprint: %s", fingerprint)

	config := &gossh.ClientConfig{
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Unknown users should be rejected when routussy is not configured.
	if _, err := gossh.Dial("tcp", addr, config); err == nil {
		t.Fatal("expected unknown user to be rejected")
	} else {
		t.Logf("unknown user rejected as expected: %v", err)
	}

	// Seed a local account with the same SSH key.
	handle := "testuser123"
	user, err := database.CreateUser(context.Background(), handle)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := database.AddSSHKey(context.Background(), user.ID, strings.TrimSpace(string(gossh.MarshalAuthorizedKey(clientPubKey))), fingerprint, "test"); err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}

	// --- Connection 2: Should be recognized ---
	conn, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("dial (known user): %v", err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		t.Fatalf("new session (known user): %v", err)
	}
	defer session.Close()

	modes := gossh.TerminalModes{gossh.ECHO: 0}
	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		t.Fatalf("request pty (known user): %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe (known user): %v", err)
	}

	if err := session.Shell(); err != nil {
		t.Fatalf("shell (known user): %v", err)
	}

	buf := make([]byte, 8192)
	var output strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			goto doneReading
		default:
		}
		n, err := stdout.Read(buf)
		if err != nil {
			break
		}
		output.Write(buf[:n])
		if strings.Contains(output.String(), "welcome back") || strings.Contains(output.String(), "ussy>") {
			time.Sleep(300 * time.Millisecond)
			n, _ = stdout.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			break
		}
	}
doneReading:
	reconnectOutput := output.String()
	t.Logf("known-user output:\n%s", reconnectOutput)

	if strings.Contains(reconnectOutput, "welcome to the ussyverse") || strings.Contains(reconnectOutput, "new here") {
		t.Fatal("known user was incorrectly shown registration flow")
	}
	if !strings.Contains(reconnectOutput, "welcome back") {
		t.Fatalf("expected welcome-back output, got:\n%s", reconnectOutput)
	}
}

// TestDBWriteReadCrossConnection verifies that writes on the writer
// connection are visible from the reader connection.
func TestDBWriteReadCrossConnection(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	// Write a user
	user, err := database.CreateUser(ctx, "crosstest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Logf("created user: id=%d handle=%s", user.ID, user.Handle)

	// Add SSH key
	key, err := database.AddSSHKey(ctx, user.ID, "ssh-ed25519 AAAA test", "SHA256:crosstest123", "test")
	if err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}
	t.Logf("added key: id=%d fingerprint=%s", key.ID, key.Fingerprint)

	// Read back via UserByFingerprint (uses reader connection)
	found, err := database.UserByFingerprint(ctx, "SHA256:crosstest123")
	if err != nil {
		t.Fatalf("UserByFingerprint: %v (this means reader can't see writer's commits)", err)
	}
	if found.ID != user.ID {
		t.Errorf("expected user ID %d, got %d", user.ID, found.ID)
	}
	t.Logf("read back user: id=%d handle=%s — cross-connection reads work!", found.ID, found.Handle)
}

func TestEnsureLocalUserForRoutussy(t *testing.T) {
	database := setupTestDB(t)

	hostKeyFile, err := os.CreateTemp("", "ussycode-hostkey-*")
	if err != nil {
		t.Fatal(err)
	}
	hostKeyPath := hostKeyFile.Name()
	hostKeyFile.Close()
	os.Remove(hostKeyPath)
	t.Cleanup(func() { os.Remove(hostKeyPath) })

	_, clientPubKey := generateTestKey(t)
	fingerprint := gossh.FingerprintSHA256(clientPubKey)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/ussycode/user-by-fingerprint" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":        "12345",
			"discord_id":     "Test User#0001",
			"budget_cents":   1000,
			"spent_cents":    0,
			"ssh_pubkey":     strings.TrimSpace(string(gossh.MarshalAuthorizedKey(clientPubKey))),
			"api_key_prefix": "ussycode",
		})
	}))
	defer server.Close()

	gw, err := New(database, nil, nil, nil, hostKeyPath, "127.0.0.1:0", "test.local")
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}
	gw.RoutussyURL = server.URL
	gw.RoutussyInternalKey = "test-secret"

	user, err := gw.ensureLocalUserForRoutussy(context.Background(), fingerprint, clientPubKey)
	if err != nil {
		t.Fatalf("ensureLocalUserForRoutussy: %v", err)
	}
	if user == nil {
		t.Fatal("expected provisioned user")
	}
	if user.Handle == "" {
		t.Fatal("expected non-empty handle")
	}

	found, err := database.UserByFingerprint(context.Background(), fingerprint)
	if err != nil {
		t.Fatalf("UserByFingerprint after provision: %v", err)
	}
	if found.ID != user.ID {
		t.Fatalf("expected same user ID, got %d vs %d", found.ID, user.ID)
	}

	keys, err := database.SSHKeysByUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("SSHKeysByUser: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 ssh key, got %d", len(keys))
	}
	if keys[0].Fingerprint != fingerprint {
		t.Fatalf("expected fingerprint %s, got %s", fingerprint, keys[0].Fingerprint)
	}
}

func TestSanitizeHandle(t *testing.T) {
	cases := map[string]string{
		"Test User#0001": "test-user-0001",
		"123abc":         "u-123abc",
		"___":            "",
		"a":              "a1",
	}
	for in, want := range cases {
		got := sanitizeHandle(in)
		if got != want {
			t.Fatalf("sanitizeHandle(%q)=%q want %q", in, got, want)
		}
	}
}
