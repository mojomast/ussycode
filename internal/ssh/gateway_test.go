package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
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

// TestRegistrationPersistence tests that after a user registers via SSH,
// they can reconnect and be recognized (not shown registration again).
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

	// --- Connection 1: Registration ---
	t.Log("=== Connection 1: Registration ===")

	config := &gossh.ClientConfig{
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	conn, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("dial (connection 1): %v", err)
	}

	session, err := conn.NewSession()
	if err != nil {
		conn.Close()
		t.Fatalf("new session (connection 1): %v", err)
	}

	// Set up pseudo-terminal for interactive session
	modes := gossh.TerminalModes{
		gossh.ECHO: 0,
	}
	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		session.Close()
		conn.Close()
		t.Fatalf("request pty: %v", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		conn.Close()
		t.Fatalf("stdin pipe: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		conn.Close()
		t.Fatalf("stdout pipe: %v", err)
	}

	if err := session.Shell(); err != nil {
		session.Close()
		conn.Close()
		t.Fatalf("shell: %v", err)
	}

	// Read until we see the handle prompt
	buf := make([]byte, 8192)
	var output strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for registration prompt. Got so far:\n%s", output.String())
		default:
		}

		// Set a read deadline on the underlying session
		n, err := stdout.Read(buf)
		if err != nil {
			t.Fatalf("read error during registration: %v (got so far: %s)", err, output.String())
		}
		output.Write(buf[:n])
		if strings.Contains(output.String(), "handle:") || strings.Contains(output.String(), "handle: ") {
			break
		}
	}

	t.Logf("registration prompt received")

	// Type a handle — use \r for PTY mode (terminal expects carriage return)
	handle := "testuser123"
	fmt.Fprintf(stdin, "%s\r", handle)
	t.Logf("sent handle: %s", handle)

	// Read the response — should see "welcome" or "you're in"
	time.Sleep(500 * time.Millisecond)
	output.Reset()
	readDeadline := time.After(5 * time.Second)
	for {
		select {
		case <-readDeadline:
			t.Logf("output after handle: %s", output.String())
			goto doneReading1
		default:
		}
		n, err := stdout.Read(buf)
		if err != nil {
			break
		}
		output.Write(buf[:n])
		if strings.Contains(output.String(), "you're in") || strings.Contains(output.String(), "ussy>") {
			break
		}
	}
doneReading1:
	t.Logf("registration output: %s", output.String())

	if !strings.Contains(output.String(), "you're in") && !strings.Contains(output.String(), "welcome") {
		t.Errorf("expected welcome message after registration, got:\n%s", output.String())
	}

	// Close the first connection
	session.Close()
	conn.Close()
	time.Sleep(300 * time.Millisecond)

	// --- Verify DB state ---
	t.Log("=== Verifying DB state ===")

	user, err := database.UserByFingerprint(context.Background(), fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("CRITICAL BUG: User not found in DB after registration! fingerprint=%s", fingerprint)
		}
		t.Fatalf("UserByFingerprint error: %v", err)
	}
	t.Logf("DB verification: found user id=%d handle=%s", user.ID, user.Handle)

	if user.Handle != handle {
		t.Errorf("expected handle %q, got %q", handle, user.Handle)
	}

	// --- Connection 2: Should be recognized ---
	t.Log("=== Connection 2: Should be recognized ===")

	conn2, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("dial (connection 2): %v", err)
	}
	defer conn2.Close()

	session2, err := conn2.NewSession()
	if err != nil {
		t.Fatalf("new session (connection 2): %v", err)
	}
	defer session2.Close()

	if err := session2.RequestPty("xterm", 80, 40, modes); err != nil {
		t.Fatalf("request pty (connection 2): %v", err)
	}

	stdout2, err := session2.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe (connection 2): %v", err)
	}

	if err := session2.Shell(); err != nil {
		t.Fatalf("shell (connection 2): %v", err)
	}

	// Read the initial output — should see "welcome back" NOT "welcome to the ussyverse"
	var output2 strings.Builder
	deadline2 := time.After(5 * time.Second)
	for {
		select {
		case <-deadline2:
			goto doneReading2
		default:
		}
		n, err := stdout2.Read(buf)
		if err != nil {
			break
		}
		output2.Write(buf[:n])
		// We expect either "welcome back" (existing user) or "ussyverse" (registration)
		if strings.Contains(output2.String(), "welcome back") || strings.Contains(output2.String(), "ussyverse") || strings.Contains(output2.String(), "ussy>") {
			// Give a bit more time to get the full message
			time.Sleep(300 * time.Millisecond)
			n, _ = stdout2.Read(buf)
			if n > 0 {
				output2.Write(buf[:n])
			}
			break
		}
	}
doneReading2:
	reconnectOutput := output2.String()
	t.Logf("reconnect output:\n%s", reconnectOutput)

	if strings.Contains(reconnectOutput, "welcome to the ussyverse") || strings.Contains(reconnectOutput, "new here") {
		t.Error("BUG: User shown registration flow on reconnect — user was not persisted!")
	}

	if strings.Contains(reconnectOutput, "welcome back") {
		t.Log("SUCCESS: User recognized on reconnect")
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
