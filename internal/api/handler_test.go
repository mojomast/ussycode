package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mojomast/ussycode/internal/db"
)

// mockExecutor implements CommandExecutor for testing.
type mockExecutor struct {
	output   string
	exitCode int
	err      error
	// Track calls
	lastUser    *db.User
	lastCommand string
	lastArgs    []string
}

func (m *mockExecutor) Execute(ctx context.Context, user *db.User, command string, args []string) (string, int, error) {
	m.lastUser = user
	m.lastCommand = command
	m.lastArgs = args
	return m.output, m.exitCode, m.err
}

// setupTestDB creates a temporary database for testing.
func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	f, err := os.CreateTemp("", "api-test-*.db")
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
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	return database
}

// setupTestHandler creates a handler with a mock executor and test DB.
func setupTestHandler(t *testing.T) (*Handler, *mockExecutor, *db.DB) {
	t.Helper()
	database := setupTestDB(t)
	executor := &mockExecutor{output: "test output", exitCode: 0}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	handler := NewHandler(database, executor, nil, logger, &Config{
		RatePerMinute: 600, // high limit for tests
		Burst:         100,
	})

	return handler, executor, database
}

// createTestUserWithToken creates a user and a usy1 short token for testing.
func createTestUserWithToken(t *testing.T, database *db.DB, handle string) (*db.User, string) {
	t.Helper()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, handle)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Add an SSH key for fingerprint tracking
	_, err = database.AddSSHKey(ctx, user.ID, "ssh-ed25519 AAAA test@test", "SHA256:test-fp-"+handle, "test")
	if err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}

	// Create a usy1 token
	tokenID := "test-token-" + handle
	perms := TokenPermissions{
		Exp:  time.Now().Add(1 * time.Hour).Unix(),
		Nbf:  time.Now().Add(-1 * time.Minute).Unix(),
		Cmds: []string{}, // all commands allowed
		Ctx:  handle,
	}
	permsJSON, _ := json.Marshal(perms)

	_, err = database.CreateAPIToken(ctx, tokenID, user.ID, string(permsJSON), "test token")
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	return user, "usy1." + tokenID
}

func TestHealthEndpoint(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", resp["status"])
	}

	if _, ok := resp["time"]; !ok {
		t.Error("expected 'time' field in response")
	}
}

func TestVersionEndpoint(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, ok := resp["version"]; !ok {
		t.Error("expected 'version' field in response")
	}
}

func TestExecEndpoint_NoAuth(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}

	if errResp.Code != http.StatusUnauthorized {
		t.Errorf("expected error code 401, got %d", errResp.Code)
	}
}

func TestExecEndpoint_InvalidAuthScheme(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestExecEndpoint_InvalidTokenFormat(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer invalid-no-prefix")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestExecEndpoint_ShortToken_Success(t *testing.T) {
	handler, executor, database := setupTestHandler(t)

	_, token := createTestUserWithToken(t, database, "testexec")
	executor.output = "my-vm  running  ussyuntu"
	executor.exitCode = 0

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp ExecResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Output != "my-vm  running  ussyuntu" {
		t.Errorf("unexpected output: %q", resp.Output)
	}

	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}

	// Verify executor was called correctly
	if executor.lastCommand != "ls" {
		t.Errorf("expected command 'ls', got %q", executor.lastCommand)
	}
}

func TestExecEndpoint_JSONBody(t *testing.T) {
	handler, executor, database := setupTestHandler(t)

	_, token := createTestUserWithToken(t, database, "testjson")
	executor.output = "done"

	mux := http.NewServeMux()
	handler.Routes(mux)

	reqBody, _ := json.Marshal(ExecRequest{Command: "new --name=test-vm"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	if executor.lastCommand != "new" {
		t.Errorf("expected command 'new', got %q", executor.lastCommand)
	}
	if len(executor.lastArgs) != 1 || executor.lastArgs[0] != "--name=test-vm" {
		t.Errorf("unexpected args: %v", executor.lastArgs)
	}
}

func TestExecEndpoint_EmptyCommand(t *testing.T) {
	handler, _, database := setupTestHandler(t)

	_, token := createTestUserWithToken(t, database, "testempty")

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestExecEndpoint_RevokedToken(t *testing.T) {
	handler, _, database := setupTestHandler(t)

	_, token := createTestUserWithToken(t, database, "testrevoke")

	// Revoke the token
	tokenID := strings.TrimPrefix(token, "usy1.")
	if err := database.RevokeAPIToken(context.Background(), tokenID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestExecEndpoint_ExpiredToken(t *testing.T) {
	handler, _, database := setupTestHandler(t)

	ctx := context.Background()
	user, err := database.CreateUser(ctx, "testexpired")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err = database.AddSSHKey(ctx, user.ID, "ssh-ed25519 AAAA test@test", "SHA256:expired-fp", "test")
	if err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}

	// Create a token that's already expired
	perms := TokenPermissions{
		Exp: time.Now().Add(-1 * time.Hour).Unix(),
		Nbf: time.Now().Add(-2 * time.Hour).Unix(),
	}
	permsJSON, _ := json.Marshal(perms)

	_, err = database.CreateAPIToken(ctx, "expired-token", user.ID, string(permsJSON), "expired")
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer usy1.expired-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestExecEndpoint_CommandPermission(t *testing.T) {
	handler, _, database := setupTestHandler(t)

	ctx := context.Background()
	user, err := database.CreateUser(ctx, "testperms")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err = database.AddSSHKey(ctx, user.ID, "ssh-ed25519 AAAA test@test", "SHA256:perms-fp", "test")
	if err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}

	// Create a token that only allows "ls"
	perms := TokenPermissions{
		Exp:  time.Now().Add(1 * time.Hour).Unix(),
		Nbf:  time.Now().Add(-1 * time.Minute).Unix(),
		Cmds: []string{"ls"},
	}
	permsJSON, _ := json.Marshal(perms)

	_, err = database.CreateAPIToken(ctx, "restricted-token", user.ID, string(permsJSON), "restricted")
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	mux := http.NewServeMux()
	handler.Routes(mux)

	// "ls" should work
	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer usy1.restricted-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for allowed command, got %d; body: %s", w.Code, w.Body.String())
	}

	// "rm" should be forbidden
	body2 := strings.NewReader("rm test-vm")
	req2 := httptest.NewRequest("POST", "/exec", body2)
	req2.Header.Set("Content-Type", "text/plain")
	req2.Header.Set("Authorization", "Bearer usy1.restricted-token")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for disallowed command, got %d; body: %s", w2.Code, w2.Body.String())
	}
}

func TestExecEndpoint_CommandError(t *testing.T) {
	handler, executor, database := setupTestHandler(t)

	_, token := createTestUserWithToken(t, database, "testerror")
	executor.err = fmt.Errorf("vm not found")

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ssh nonexistent")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422, got %d", w.Code)
	}
}

func TestExecEndpoint_PayloadTooLarge(t *testing.T) {
	handler, _, database := setupTestHandler(t)

	_, token := createTestUserWithToken(t, database, "testlarge")

	mux := http.NewServeMux()
	handler.Routes(mux)

	// Create a body larger than 64KB
	large := strings.Repeat("a", 65*1024)
	body := strings.NewReader(large)
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Should be rejected (either 400 bad request because it's truncated or 413)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected status 400 or 413, got %d", w.Code)
	}
}

func TestExecEndpoint_MethodNotAllowed(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	// GET /exec should not match POST /exec
	req := httptest.NewRequest("GET", "/exec", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// ServeMux returns 405 for wrong method when the pattern includes a method
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestExecEndpoint_NonExistentToken(t *testing.T) {
	handler, _, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	body := strings.NewReader("ls")
	req := httptest.NewRequest("POST", "/exec", body)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer usy1.nonexistent-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(60, 5) // 60/min = 1/sec, burst 5

	key := "test-fp"

	// First 5 should be allowed (burst)
	for i := 0; i < 5; i++ {
		if !rl.Allow(key) {
			t.Errorf("request %d should be allowed (within burst)", i)
		}
	}

	// 6th should be rejected (burst exhausted)
	if rl.Allow(key) {
		t.Error("6th request should be rejected (burst exhausted)")
	}

	// Different key should be allowed
	if !rl.Allow("other-fp") {
		t.Error("different key should be allowed")
	}
}

func TestRateLimiter_RetryAfter(t *testing.T) {
	rl := NewRateLimiter(60, 1) // 60/min, burst 1

	key := "retry-fp"

	// Use up the burst
	rl.Allow(key)

	// Should have a non-zero retry-after
	retry := rl.RetryAfter(key)
	if retry <= 0 {
		t.Errorf("expected positive retry-after, got %v", retry)
	}
}

func TestAPITokenCRUD(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	// Create user
	user, err := database.CreateUser(ctx, "tokenuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create API token
	tok, err := database.CreateAPIToken(ctx, "tok-123", user.ID, `{"exp":99999999999}`, "my token")
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if tok.TokenID != "tok-123" {
		t.Errorf("expected token_id 'tok-123', got %q", tok.TokenID)
	}
	if tok.UserID != user.ID {
		t.Errorf("expected user_id %d, got %d", user.ID, tok.UserID)
	}

	// Retrieve by ID
	found, err := database.APITokenByID(ctx, "tok-123")
	if err != nil {
		t.Fatalf("APITokenByID: %v", err)
	}
	if found.FullToken != `{"exp":99999999999}` {
		t.Errorf("unexpected full_token: %q", found.FullToken)
	}

	// List by user
	tokens, err := database.APITokensByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("APITokensByUser: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}

	// Touch
	if err := database.TouchAPIToken(ctx, "tok-123"); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}

	// Revoke
	if err := database.RevokeAPIToken(ctx, "tok-123"); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	revoked, _ := database.APITokenByID(ctx, "tok-123")
	if !revoked.Revoked {
		t.Error("expected token to be revoked")
	}

	// Delete
	if err := database.DeleteAPIToken(ctx, "tok-123"); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	_, err = database.APITokenByID(ctx, "tok-123")
	if err == nil {
		t.Error("expected error after delete")
	}
}
