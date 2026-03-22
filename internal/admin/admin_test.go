package admin

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mojomast/ussycode/internal/db"
)

// setupTestDB creates a temporary database for testing.
func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	f, err := os.CreateTemp("", "admin-test-*.db")
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

// setupTestHandler creates an admin handler with the embedded WebFS and test DB.
func setupTestHandler(t *testing.T) (*Handler, *db.DB) {
	t.Helper()
	database := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Use the real embedded WebFS (with "web/" prefix)
	webSub, err := fs.Sub(WebFS, "web")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}

	handler, err := NewHandler(database, webSub, logger, &Config{Domain: "test.ussy.host"})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	return handler, database
}

// createAdminUser creates an admin-level user and returns it.
func createAdminUser(t *testing.T, database *db.DB, handle string) *db.User {
	t.Helper()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, handle)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Upgrade to admin
	err = database.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET trust_level = 'admin' WHERE id = ?`, user.ID)
		return err
	})
	if err != nil {
		t.Fatalf("upgrade to admin: %v", err)
	}

	user.TrustLevel = "admin"
	return user
}

// createMagicToken creates a magic token for the given user.
func createMagicToken(t *testing.T, database *db.DB, userID int64, token string) {
	t.Helper()
	ctx := context.Background()
	err := database.CreateMagicToken(ctx, userID, token, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("CreateMagicToken: %v", err)
	}
}

// addSessionCookie adds a valid session cookie to the request.
func addSessionCookie(t *testing.T, h *Handler, user *db.User) string {
	t.Helper()
	sessionID, err := h.sessions.Create(user.ID, user.Handle, sessionTTL)
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return sessionID
}

// reqWithSession creates an HTTP request with a valid session cookie.
func reqWithSession(t *testing.T, method, url, sessionID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, url, nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: sessionID,
	})
	return req
}

// --- Session store tests ---

func TestSessionStore_CreateAndGet(t *testing.T) {
	store := newSessionStore()

	id, err := store.Create(42, "testuser", 1*time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if id == "" {
		t.Fatal("expected non-empty session ID")
	}

	sess, ok := store.Get(id)
	if !ok {
		t.Fatal("expected session to exist")
	}

	if sess.UserID != 42 {
		t.Errorf("expected UserID 42, got %d", sess.UserID)
	}
	if sess.Handle != "testuser" {
		t.Errorf("expected Handle 'testuser', got %q", sess.Handle)
	}
}

func TestSessionStore_GetExpired(t *testing.T) {
	store := newSessionStore()

	// Create a session with 0 TTL (already expired)
	id, err := store.Create(42, "expired", -1*time.Second)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, ok := store.Get(id)
	if ok {
		t.Error("expected expired session to not be returned")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	store := newSessionStore()

	id, err := store.Create(42, "todelete", 1*time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	store.Delete(id)

	_, ok := store.Get(id)
	if ok {
		t.Error("expected deleted session to not be returned")
	}
}

func TestSessionStore_CleanExpired(t *testing.T) {
	store := newSessionStore()

	// Create one valid and one expired session
	_, err := store.Create(1, "valid", 1*time.Hour)
	if err != nil {
		t.Fatalf("Create valid: %v", err)
	}
	_, err = store.Create(2, "expired", -1*time.Second)
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}

	// Count before clean
	store.mu.RLock()
	beforeCount := len(store.sessions)
	store.mu.RUnlock()

	if beforeCount != 2 {
		t.Fatalf("expected 2 sessions before clean, got %d", beforeCount)
	}

	store.CleanExpired()

	store.mu.RLock()
	afterCount := len(store.sessions)
	store.mu.RUnlock()

	if afterCount != 1 {
		t.Errorf("expected 1 session after clean, got %d", afterCount)
	}
}

func TestSessionStore_UniqueIDs(t *testing.T) {
	store := newSessionStore()

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := store.Create(int64(i), fmt.Sprintf("user%d", i), 1*time.Hour)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if ids[id] {
			t.Fatalf("duplicate session ID generated: %s", id)
		}
		ids[id] = true
	}
}

// --- Auth middleware tests ---

func TestRequireAuth_NoCookie(t *testing.T) {
	handler, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if loc != "/admin/login" {
		t.Errorf("expected redirect to /admin/login, got %q", loc)
	}
}

func TestRequireAuth_InvalidSession(t *testing.T) {
	handler, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/", nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: "totally-invalid-session-id",
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if loc != "/admin/login" {
		t.Errorf("expected redirect to /admin/login, got %q", loc)
	}
}

func TestRequireAuth_ValidSession(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "authadmin")
	sessionID := addSessionCookie(t, handler, admin)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// Should contain dashboard content
	body := w.Body.String()
	if !strings.Contains(body, "Dashboard") {
		t.Error("expected dashboard page content")
	}
}

// --- Login flow tests ---

func TestLoginPage(t *testing.T) {
	handler, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "login") && !strings.Contains(body, "Login") {
		t.Error("expected login page content")
	}
}

func TestLoginCallback_MissingToken(t *testing.T) {
	handler, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/login/callback", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "missing") {
		t.Errorf("expected error about missing token, got redirect to %q", loc)
	}
}

func TestLoginCallback_InvalidToken(t *testing.T) {
	handler, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/login/callback?token=nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "invalid") || !strings.Contains(loc, "expired") {
		t.Errorf("expected error about invalid/expired token, got redirect to %q", loc)
	}
}

func TestLoginCallback_NonAdmin(t *testing.T) {
	handler, database := setupTestHandler(t)

	// Create a non-admin user (default trust_level is 'newbie')
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "normie")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a magic token
	createMagicToken(t, database, user.ID, "normie-token-123")

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/login/callback?token=normie-token-123", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "permissions") {
		t.Errorf("expected error about insufficient permissions, got redirect to %q", loc)
	}
}

func TestLoginCallback_AdminSuccess(t *testing.T) {
	handler, database := setupTestHandler(t)

	admin := createAdminUser(t, database, "superadmin")
	createMagicToken(t, database, admin.ID, "admin-token-abc")

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/login/callback?token=admin-token-abc", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if loc != "/admin/" {
		t.Errorf("expected redirect to /admin/, got %q", loc)
	}

	// Should have a session cookie
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			found = true
			if c.Value == "" {
				t.Error("expected non-empty session cookie")
			}
		}
	}
	if !found {
		t.Error("expected session cookie to be set")
	}
}

func TestLoginCallback_OperatorSuccess(t *testing.T) {
	handler, database := setupTestHandler(t)
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "opsuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Upgrade to operator
	err = database.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET trust_level = 'operator' WHERE id = ?`, user.ID)
		return err
	})
	if err != nil {
		t.Fatalf("upgrade to operator: %v", err)
	}

	createMagicToken(t, database, user.ID, "operator-token-xyz")

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/login/callback?token=operator-token-xyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if loc != "/admin/" {
		t.Errorf("expected redirect to /admin/, got %q", loc)
	}
}

// --- Logout test ---

func TestLogout(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "logoutadmin")
	sessionID := addSessionCookie(t, handler, admin)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "POST", "/admin/logout", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if loc != "/admin/login" {
		t.Errorf("expected redirect to /admin/login, got %q", loc)
	}

	// Session should be deleted
	_, ok := handler.sessions.Get(sessionID)
	if ok {
		t.Error("expected session to be deleted after logout")
	}
}

// --- Dashboard tests ---

func TestDashboard_Authenticated(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "dashadmin")
	sessionID := addSessionCookie(t, handler, admin)

	// Create some data for dashboard stats
	ctx := context.Background()
	user2, _ := database.CreateUser(ctx, "dashuser2")
	database.CreateVM(ctx, user2.ID, "testvm1", "ussyuntu", 1, 256, 5)
	database.CreateVM(ctx, admin.ID, "testvm2", "ussyuntu", 2, 512, 10)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	// Should contain dashboard elements
	if !strings.Contains(body, "Dashboard") {
		t.Error("expected 'Dashboard' in page")
	}
	if !strings.Contains(body, "Total Users") {
		t.Error("expected 'Total Users' in stats")
	}
	if !strings.Contains(body, "Total VMs") {
		t.Error("expected 'Total VMs' in stats")
	}
}

// --- Users page tests ---

func TestUsersPage(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "usersadmin")
	sessionID := addSessionCookie(t, handler, admin)

	ctx := context.Background()
	database.CreateUser(ctx, "alice")
	database.CreateUser(ctx, "bob")

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/users", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "alice") {
		t.Error("expected 'alice' in users list")
	}
	if !strings.Contains(body, "bob") {
		t.Error("expected 'bob' in users list")
	}
}

// --- User detail tests ---

func TestUserDetailPage(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "detailadmin")
	sessionID := addSessionCookie(t, handler, admin)

	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "detailuser")
	database.AddSSHKey(ctx, user.ID, "ssh-ed25519 AAAA test@test", "SHA256:detail-fp", "testkey")
	database.CreateVM(ctx, user.ID, "uservm", "ussyuntu", 1, 256, 5)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", fmt.Sprintf("/admin/users/%d", user.ID), sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "detailuser") {
		t.Error("expected user handle in detail page")
	}
	if !strings.Contains(body, "uservm") {
		t.Error("expected VM name in user detail page")
	}
}

func TestUserDetailPage_InvalidID(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "invalididadmin")
	sessionID := addSessionCookie(t, handler, admin)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/users/abc", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestUserDetailPage_NotFound(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "nfadmin")
	sessionID := addSessionCookie(t, handler, admin)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/users/99999", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// --- Set trust level tests ---

func TestSetTrustLevel(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "trustadmin")
	sessionID := addSessionCookie(t, handler, admin)

	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "trustuser")

	mux := http.NewServeMux()
	handler.Routes(mux)

	// POST to update trust level
	form := url.Values{}
	form.Set("trust_level", "citizen")
	req := httptest.NewRequest("POST",
		fmt.Sprintf("/admin/users/%d/trust", user.ID),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: sessionID,
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d; body: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "success") {
		t.Errorf("expected success in redirect, got %q", loc)
	}

	// Verify the update
	updated, err := database.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if updated.TrustLevel != "citizen" {
		t.Errorf("expected trust_level 'citizen', got %q", updated.TrustLevel)
	}
}

func TestSetTrustLevel_InvalidLevel(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "badtrustadmin")
	sessionID := addSessionCookie(t, handler, admin)

	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "badtrustuser")

	mux := http.NewServeMux()
	handler.Routes(mux)

	form := url.Values{}
	form.Set("trust_level", "superadmin") // invalid
	req := httptest.NewRequest("POST",
		fmt.Sprintf("/admin/users/%d/trust", user.ID),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: sessionID,
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "invalid") {
		t.Errorf("expected error about invalid trust level, got %q", loc)
	}
}

// --- VMs page tests ---

func TestVMsPage(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "vmsadmin")
	sessionID := addSessionCookie(t, handler, admin)

	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "vmowner")
	database.CreateVM(ctx, user.ID, "webserver", "ussyuntu", 2, 512, 10)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/vms", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "webserver") {
		t.Error("expected 'webserver' in VMs list")
	}
	if !strings.Contains(body, "vmowner") {
		t.Error("expected owner handle 'vmowner' in VMs list")
	}
}

// --- VM detail tests ---

func TestVMDetailPage(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "vmdetailadmin")
	sessionID := addSessionCookie(t, handler, admin)

	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "vmdetailowner")
	vm, _ := database.CreateVM(ctx, user.ID, "detailvm", "ussyuntu", 4, 1024, 20)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", fmt.Sprintf("/admin/vms/%d", vm.ID), sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "detailvm") {
		t.Error("expected VM name in detail page")
	}
}

func TestVMDetailPage_NotFound(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "vmnfadmin")
	sessionID := addSessionCookie(t, handler, admin)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/vms/99999", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// --- Nodes page tests ---

func TestNodesPage(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "nodesadmin")
	sessionID := addSessionCookie(t, handler, admin)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := reqWithSession(t, "GET", "/admin/nodes", sessionID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Nodes") {
		t.Error("expected 'Nodes' heading in page")
	}
	// Should show the placeholder empty state
	if !strings.Contains(body, "No nodes registered") {
		t.Error("expected empty state placeholder")
	}
}

// --- CSS handler test ---

func TestCSSHandler(t *testing.T) {
	handler, _ := setupTestHandler(t)

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/admin/static/style.css", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("expected text/css content type, got %q", ct)
	}

	if w.Body.Len() == 0 {
		t.Error("expected non-empty CSS body")
	}
}

// --- Template helper tests ---

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		name     string
		time     time.Time
		contains string
	}{
		{"zero", time.Time{}, "never"},
		{"just now", time.Now(), "just now"},
		{"minutes", time.Now().Add(-5 * time.Minute), "5 minutes ago"},
		{"1 minute", time.Now().Add(-90 * time.Second), "1 minute ago"},
		{"hours", time.Now().Add(-3 * time.Hour), "3 hours ago"},
		{"1 hour", time.Now().Add(-90 * time.Minute), "1 hour ago"},
		{"days", time.Now().Add(-5 * 24 * time.Hour), "5 days ago"},
		{"1 day", time.Now().Add(-30 * time.Hour), "1 day ago"},
		{"old", time.Now().Add(-60 * 24 * time.Hour), "20"}, // should contain year in date format
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := db.SQLiteTime{Time: tc.time}
			result := timeAgo(st)
			if !strings.Contains(result, tc.contains) {
				t.Errorf("timeAgo(%v) = %q, expected to contain %q", tc.time, result, tc.contains)
			}
		})
	}
}

func TestTrustBadge(t *testing.T) {
	tests := []struct {
		level    string
		expected string
	}{
		{"admin", "badge-admin"},
		{"operator", "badge-operator"},
		{"citizen", "badge-citizen"},
		{"newbie", "badge-newbie"},
		{"unknown", "badge-newbie"},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			result := trustBadge(tc.level)
			if result != tc.expected {
				t.Errorf("trustBadge(%q) = %q, expected %q", tc.level, result, tc.expected)
			}
		})
	}
}

func TestStatusBadge(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"running", "badge-running"},
		{"stopped", "badge-stopped"},
		{"creating", "badge-creating"},
		{"error", "badge-error"},
		{"unknown", "badge-unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			result := statusBadge(tc.status)
			if result != tc.expected {
				t.Errorf("statusBadge(%q) = %q, expected %q", tc.status, result, tc.expected)
			}
		})
	}
}

// --- HandleMagicLink tests ---

// newMagicLinkRequest builds a GET request for /__auth/magic/{token} and
// ensures PathValue("token") is populated as Go's stdlib mux would do.
func newMagicLinkRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	path := "/__auth/magic/" + token
	req := httptest.NewRequest("GET", path, nil)
	// Simulate the mux setting the path value (needed when calling the
	// handler directly without a full mux round-trip).
	req.SetPathValue("token", token)
	return req
}

func TestHandleMagicLink_MissingToken(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Call with an empty token value directly.
	req := httptest.NewRequest("GET", "/__auth/magic/", nil)
	req.SetPathValue("token", "")
	w := httptest.NewRecorder()

	handler.HandleMagicLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleMagicLink_InvalidToken(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := newMagicLinkRequest(t, "does-not-exist")
	w := httptest.NewRecorder()

	handler.HandleMagicLink(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "invalid") || !strings.Contains(loc, "expired") {
		t.Errorf("expected error redirect, got %q", loc)
	}
}

func TestHandleMagicLink_AdminSuccess(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "magicadmin")
	createMagicToken(t, database, admin.ID, "magic-admin-tok")

	req := newMagicLinkRequest(t, "magic-admin-tok")
	w := httptest.NewRecorder()

	handler.HandleMagicLink(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/" {
		t.Errorf("expected redirect to /admin/, got %q", loc)
	}

	// A session cookie must be set.
	var sessionCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set")
	}
	if sessionCookie.Value == "" {
		t.Error("expected non-empty session cookie value")
	}

	// The session must be valid in the store.
	sess, ok := handler.sessions.Get(sessionCookie.Value)
	if !ok {
		t.Error("session not found in store")
	} else if sess.Handle != "magicadmin" {
		t.Errorf("expected session handle 'magicadmin', got %q", sess.Handle)
	}
}

func TestHandleMagicLink_OperatorSuccess(t *testing.T) {
	handler, database := setupTestHandler(t)
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "magicops")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := database.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET trust_level = 'operator' WHERE id = ?`, user.ID)
		return err
	}); err != nil {
		t.Fatalf("upgrade to operator: %v", err)
	}
	createMagicToken(t, database, user.ID, "magic-ops-tok")

	req := newMagicLinkRequest(t, "magic-ops-tok")
	w := httptest.NewRecorder()

	handler.HandleMagicLink(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/" {
		t.Errorf("expected redirect to /admin/, got %q", loc)
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected session cookie to be set for operator")
	}
}

func TestHandleMagicLink_RegularUserWithRunningVM(t *testing.T) {
	handler, database := setupTestHandler(t)
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "vmuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	vm, err := database.CreateVM(ctx, user.ID, "myapp", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Mark VM as running.
	running := "running"
	if err := database.UpdateVMStatus(ctx, vm.ID, "running", nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateVMStatus: %v", err)
	}
	_ = running

	createMagicToken(t, database, user.ID, "magic-vmuser-tok")

	req := newMagicLinkRequest(t, "magic-vmuser-tok")
	w := httptest.NewRecorder()

	handler.HandleMagicLink(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "myapp") {
		t.Errorf("expected redirect to VM URL containing 'myapp', got %q", loc)
	}
	if !strings.Contains(loc, "test.ussy.host") {
		t.Errorf("expected redirect URL to contain domain 'test.ussy.host', got %q", loc)
	}
}

func TestHandleMagicLink_RegularUserNoVM(t *testing.T) {
	handler, database := setupTestHandler(t)
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "novmuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	createMagicToken(t, database, user.ID, "magic-novm-tok")

	req := newMagicLinkRequest(t, "magic-novm-tok")
	w := httptest.NewRecorder()

	handler.HandleMagicLink(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/" {
		t.Errorf("expected fallback redirect to /, got %q", loc)
	}
}

func TestHandleMagicLink_TokenConsumedAfterUse(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "onceadmin")
	createMagicToken(t, database, admin.ID, "magic-once-tok")

	// First use — should succeed.
	req := newMagicLinkRequest(t, "magic-once-tok")
	w := httptest.NewRecorder()
	handler.HandleMagicLink(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("first use: expected 303, got %d", w.Code)
	}

	// Second use — token is already consumed.
	req2 := newMagicLinkRequest(t, "magic-once-tok")
	w2 := httptest.NewRecorder()
	handler.HandleMagicLink(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Errorf("second use: expected redirect (303), got %d", w2.Code)
	}
	loc := w2.Header().Get("Location")
	if !strings.Contains(loc, "invalid") || !strings.Contains(loc, "expired") {
		t.Errorf("second use: expected error redirect, got %q", loc)
	}
}

// TestHandleMagicLink_ViaRoutes verifies the route is reachable through the
// mux (i.e. Routes() registers GET /__auth/magic/{token} correctly).
func TestHandleMagicLink_ViaRoutes(t *testing.T) {
	handler, database := setupTestHandler(t)
	admin := createAdminUser(t, database, "routeadmin")
	createMagicToken(t, database, admin.ID, "magic-route-tok")

	mux := http.NewServeMux()
	handler.Routes(mux)

	req := httptest.NewRequest("GET", "/__auth/magic/magic-route-tok", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/" {
		t.Errorf("expected redirect to /admin/, got %q", loc)
	}
}
