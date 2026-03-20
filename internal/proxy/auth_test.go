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
