package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mojomast/ussycode/internal/db"
	"github.com/mojomast/ussycode/internal/telemetry"
)

// AuthProxy wraps the Caddy proxy manager to inject authentication
// headers (X-Ussy-*) into requests routed to VMs.
//
// In practice, this runs as a small HTTP server between Caddy and the VMs,
// or Caddy can be configured to call this as an auth middleware.
// For the MVP, we implement it as a standalone auth-check endpoint that
// Caddy's forward_auth directive calls.
type AuthProxy struct {
	db     *db.DB
	domain string
	logger *slog.Logger
}

// NewAuthProxy creates a new auth proxy.
func NewAuthProxy(database *db.DB, domain string, logger *slog.Logger) *AuthProxy {
	return &AuthProxy{
		db:     database,
		domain: domain,
		logger: logger,
	}
}

// Handler returns an HTTP handler for Caddy's forward_auth directive.
//
// Caddy sends the original request to this endpoint. We check auth,
// then return 200 with X-Ussy-* headers (which Caddy copies to the
// upstream request) or 401/403 to block.
//
// Auth is checked via:
//  1. Bearer token in Authorization header (API tokens)
//  2. Cookie-based session (browser access via magic link)
//  3. Public VM access (no auth required if VM is public)
func (ap *AuthProxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Extract VM name from the Host header
		vmName := ap.extractVMName(r.Host)
		if vmName == "" {
			telemetry.RecordProxyDecision(ctx, "invalid_host")
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}

		// Look up the VM
		vm, err := ap.db.VMByName(ctx, vmName)
		if err != nil {
			telemetry.RecordProxyDecision(ctx, "vm_not_found")
			ap.logger.Debug("VM not found for host", "host", r.Host, "vm", vmName)
			http.Error(w, "no such environment", http.StatusNotFound)
			return
		}

		// Redeem a link token into a short-lived VM-scoped cookie.
		if token := r.URL.Query().Get("ussy_share"); token != "" {
			share, err := ap.db.ShareByLinkToken(ctx, token)
			if err != nil || share.VMID != vm.ID {
				telemetry.RecordProxyDecision(ctx, "share_link_invalid")
				ap.logger.Warn("share link redemption failed", "vm", vmName, "error", err)
				http.Error(w, "invalid share link", http.StatusForbidden)
				return
			}

			telemetry.RecordProxyDecision(ctx, "share_link_redeemed")

			http.SetCookie(w, &http.Cookie{
				Name:     shareCookieName(vm.ID),
				Value:    token,
				Path:     "/",
				MaxAge:   int((24 * time.Hour).Seconds()),
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			cleanURL := *r.URL
			q := cleanURL.Query()
			q.Del("ussy_share")
			cleanURL.RawQuery = q.Encode()
			w.Header().Set("Location", cleanURL.String())
			w.WriteHeader(http.StatusFound)
			return
		}

		// Check if VM is public (via shares table)
		isPublic, _ := ap.db.IsVMPublic(ctx, vm.ID)
		if isPublic {
			telemetry.RecordProxyDecision(ctx, "public")
			ap.setAuthHeaders(w, nil, vm, "public")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Check Bearer token
		if token := extractBearerToken(r); token != "" {
			user, err := ap.authenticateToken(ctx, token)
			if err != nil {
				telemetry.RecordProxyDecision(ctx, "bearer_unauthorized")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			// Check if user owns the VM or has share access
			if user.ID != vm.UserID {
				hasAccess, _ := ap.db.HasShareAccess(ctx, vm.ID, user.ID)
				if !hasAccess {
					telemetry.RecordProxyDecision(ctx, "bearer_forbidden")
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
			}

			telemetry.RecordProxyDecision(ctx, "authenticated")
			ap.setAuthHeaders(w, user, vm, "authenticated")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Check redeemed share-link cookie.
		if cookie, err := r.Cookie(shareCookieName(vm.ID)); err == nil && cookie.Value != "" {
			share, err := ap.db.ShareByLinkToken(ctx, cookie.Value)
			if err == nil && share.VMID == vm.ID {
				telemetry.RecordProxyDecision(ctx, "share_link_cookie")
				ap.setAuthHeaders(w, nil, vm, "share-link")
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		// No auth provided and VM is private
		telemetry.RecordProxyDecision(ctx, "unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// extractVMName extracts the VM name from a hostname.
// e.g., "myvm.ussy.host" -> "myvm"
func (ap *AuthProxy) extractVMName(host string) string {
	// Strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	suffix := "." + ap.domain
	if !strings.HasSuffix(host, suffix) {
		return ""
	}

	name := strings.TrimSuffix(host, suffix)
	if name == "" || strings.Contains(name, ".") {
		return ""
	}

	return name
}

// setAuthHeaders sets the X-Ussy-* headers on the response.
// Caddy's forward_auth copies response headers to the upstream request.
func (ap *AuthProxy) setAuthHeaders(w http.ResponseWriter, user *db.User, vm *db.VM, mode string) {
	if mode == "" {
		mode = "authenticated"
	}
	w.Header().Set("X-Ussy-Auth-Mode", mode)
	w.Header().Set("X-Ussy-VM", vm.Name)
	w.Header().Set("X-Ussy-VM-ID", fmt.Sprintf("%d", vm.ID))
	if user != nil {
		w.Header().Set("X-Ussy-UserID", fmt.Sprintf("%d", user.ID))
		w.Header().Set("X-Ussy-Handle", user.Handle)
	}
}

func shareCookieName(vmID int64) string {
	return fmt.Sprintf("ussy_share_%d", vmID)
}

// authenticateToken validates a Bearer token and returns the associated user.
func (ap *AuthProxy) authenticateToken(ctx context.Context, token string) (*db.User, error) {
	// Look up token in DB
	tok, err := ap.db.TokenByHandle(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("invalid token")
	}

	// Check expiry
	if tok.IsExpired() {
		return nil, fmt.Errorf("token expired")
	}

	// Get user
	user, err := ap.db.UserByID(ctx, tok.UserID)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}

	return user, nil
}

// extractBearerToken extracts a Bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimPrefix(auth, prefix)
}
