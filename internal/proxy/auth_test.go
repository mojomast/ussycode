package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mojomast/ussycode/internal/db"
)

func testAuthProxy(t *testing.T) (*AuthProxy, *db.DB, *db.User, *db.User, *db.VM) {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/proxy.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	ctx := context.Background()
	owner, err := database.CreateUser(ctx, "owner")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := database.CreateUser(ctx, "viewer")
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	vmRecord, err := database.CreateVM(ctx, owner.ID, "demo", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("create vm: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	return NewAuthProxy(database, "ussy.host", logger), database, owner, viewer, vmRecord
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func TestAuthProxy_PublicVM(t *testing.T) {
	proxy, database, _, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()
	if err := database.SetVMPublic(ctx, vmRecord.ID, true); err != nil {
		t.Fatalf("SetVMPublic: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://demo.ussy.host/", nil)
	req.Host = "demo.ussy.host"
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Ussy-Auth-Mode"); got != "public" {
		t.Fatalf("auth mode = %q, want public", got)
	}
	if got := rr.Header().Get("X-Ussy-UserID"); got != "" {
		t.Fatalf("expected no user headers for public access, got %q", got)
	}
}

func TestAuthProxy_BearerShareAccess(t *testing.T) {
	proxy, database, _, viewer, vmRecord := testAuthProxy(t)
	ctx := context.Background()
	if err := database.ShareVMWithUser(ctx, vmRecord.ID, viewer.ID); err != nil {
		t.Fatalf("ShareVMWithUser: %v", err)
	}
	if err := database.CreateToken(ctx, viewer.ID, "viewer-token", "signed", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://demo.ussy.host/", nil)
	req.Host = "demo.ussy.host"
	req.Header.Set("Authorization", "Bearer viewer-token")
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Ussy-Handle"); got != viewer.Handle {
		t.Fatalf("handle = %q, want %q", got, viewer.Handle)
	}
}

func TestAuthProxy_RedeemsShareLinkAndSetsCookie(t *testing.T) {
	proxy, database, _, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()
	share, err := database.ShareVMWithLink(ctx, vmRecord.ID, "share-token")
	if err != nil {
		t.Fatalf("ShareVMWithLink: %v", err)
	}
	if share.VMID != vmRecord.ID {
		t.Fatalf("share VMID = %d, want %d", share.VMID, vmRecord.ID)
	}

	req := httptest.NewRequest(http.MethodGet, "http://demo.ussy.host/?ussy_share=share-token", nil)
	req.Host = "demo.ussy.host"
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Value != "share-token" {
		t.Fatalf("expected share cookie to be set, got %#v", cookies)
	}
	if loc := rr.Header().Get("Location"); loc != "http://demo.ussy.host/" {
		t.Fatalf("location = %q, want http://demo.ussy.host/", loc)
	}
}

func TestAuthProxy_AllowsRedeemedShareCookie(t *testing.T) {
	proxy, database, _, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()
	if _, err := database.ShareVMWithLink(ctx, vmRecord.ID, "share-token"); err != nil {
		t.Fatalf("ShareVMWithLink: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://demo.ussy.host/", nil)
	req.Host = "demo.ussy.host"
	req.AddCookie(&http.Cookie{Name: shareCookieName(vmRecord.ID), Value: "share-token"})
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Ussy-Auth-Mode"); got != "share-link" {
		t.Fatalf("auth mode = %q, want share-link", got)
	}
}

// TestAuthProxy_ForwardAuthVMNameFromXForwardedHost verifies that when Caddy's
// forward_auth calls the auth proxy, the VM name is resolved from the
// X-Forwarded-Host header rather than from r.Host (which would be the auth
// proxy's own bind address, e.g. "localhost:9876").
func TestAuthProxy_ForwardAuthVMNameFromXForwardedHost(t *testing.T) {
	proxy, database, _, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()
	if err := database.SetVMPublic(ctx, vmRecord.ID, true); err != nil {
		t.Fatalf("SetVMPublic: %v", err)
	}

	// Simulate what Caddy's forward_auth sends: Host is the auth proxy itself,
	// the real client-facing host is in X-Forwarded-Host.
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9876/", nil)
	req.Host = "localhost:9876"
	req.Header.Set("X-Forwarded-Host", "demo.ussy.host")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Uri", "/")
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("X-Ussy-Auth-Mode"); got != "public" {
		t.Fatalf("auth mode = %q, want public", got)
	}
}

// TestAuthProxy_ForwardAuthShareLinkRedemption covers the full share-link
// redemption flow as seen through Caddy's forward_auth:
//   - r.Host is the auth proxy address, NOT the VM hostname
//   - X-Forwarded-Host carries the VM's subdomain
//   - X-Forwarded-Uri carries the original request path+query (with ussy_share=)
//   - X-Forwarded-Proto carries "https"
//
// Expect: 302 redirect to https://<vmhost>/ with the share cookie set.
func TestAuthProxy_ForwardAuthShareLinkRedemption(t *testing.T) {
	proxy, database, _, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()
	if _, err := database.ShareVMWithLink(ctx, vmRecord.ID, "tok-fwd"); err != nil {
		t.Fatalf("ShareVMWithLink: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost:9876/", nil)
	req.Host = "localhost:9876"
	req.Header.Set("X-Forwarded-Host", "demo.ussy.host")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Uri", "/?ussy_share=tok-fwd")
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d (Found)", rr.Code, http.StatusFound)
	}

	// Cookie must be set with the share token value
	cookies := rr.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected share cookie to be set, got none")
	}
	var gotCookie *http.Cookie
	wantName := shareCookieName(vmRecord.ID)
	for _, c := range cookies {
		if c.Name == wantName {
			gotCookie = c
			break
		}
	}
	if gotCookie == nil {
		t.Fatalf("cookie %q not found in response; got: %v", wantName, cookies)
	}
	if gotCookie.Value != "tok-fwd" {
		t.Fatalf("cookie value = %q, want %q", gotCookie.Value, "tok-fwd")
	}

	// Location must point back to the VM's clean URL (no ussy_share param)
	if loc := rr.Header().Get("Location"); loc != "https://demo.ussy.host/" {
		t.Fatalf("Location = %q, want https://demo.ussy.host/", loc)
	}
}

// TestAuthProxy_InvalidShareTokenForbidden checks that a bogus or non-existent
// share token returns 403 Forbidden rather than letting the request through.
func TestAuthProxy_InvalidShareTokenForbidden(t *testing.T) {
	proxy, _, _, _, _ := testAuthProxy(t)

	req := httptest.NewRequest(http.MethodGet, "http://demo.ussy.host/?ussy_share=does-not-exist", nil)
	req.Host = "demo.ussy.host"
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (Forbidden)", rr.Code, http.StatusForbidden)
	}
}

// TestAuthProxy_RevokedShareLinkCookieDenied verifies that after a share link
// is deleted (revoked), a browser that already holds the corresponding cookie
// is denied access rather than being allowed through stale session state.
func TestAuthProxy_RevokedShareLinkCookieDenied(t *testing.T) {
	proxy, database, owner, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()

	// Create a share link and immediately revoke it
	if _, err := database.ShareVMWithLink(ctx, vmRecord.ID, "revoked-tok"); err != nil {
		t.Fatalf("ShareVMWithLink: %v", err)
	}
	if err := database.RemoveShareLink(ctx, vmRecord.ID, "revoked-tok"); err != nil {
		t.Fatalf("RemoveShareLink: %v", err)
	}
	_ = owner // only owner retains access via bearer token; cookie holder is denied

	// Browser still sends the old share cookie
	req := httptest.NewRequest(http.MethodGet, "http://demo.ussy.host/", nil)
	req.Host = "demo.ussy.host"
	req.AddCookie(&http.Cookie{Name: shareCookieName(vmRecord.ID), Value: "revoked-tok"})
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	// VM is private and the share link is gone → must be denied
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (Unauthorized)", rr.Code, http.StatusUnauthorized)
	}
}

// TestAuthProxy_ShareTokenBelongsToWrongVM ensures a valid share token
// for VM-A cannot be used to access VM-B.
func TestAuthProxy_ShareTokenBelongsToWrongVM(t *testing.T) {
	proxy, database, owner, _, vmRecord := testAuthProxy(t)
	ctx := context.Background()

	// Create a second VM owned by the same user
	vmB, err := database.CreateVM(ctx, owner.ID, "other-vm", "ussyuntu", 1, 512, 5)
	if err != nil {
		t.Fatalf("CreateVM (vmB): %v", err)
	}

	// Share-link created for vmA
	if _, err := database.ShareVMWithLink(ctx, vmRecord.ID, "tok-vma"); err != nil {
		t.Fatalf("ShareVMWithLink (vmA): %v", err)
	}

	// Try to redeem the vmA token against vmB's URL
	req := httptest.NewRequest(http.MethodGet,
		"http://other-vm.ussy.host/?ussy_share=tok-vma", nil)
	req.Host = "other-vm.ussy.host"
	rr := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rr, req)

	// Token exists in DB but belongs to a different VM → forbidden
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (Forbidden)", rr.Code, http.StatusForbidden)
	}
	_ = vmB
}
