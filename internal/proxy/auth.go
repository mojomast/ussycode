package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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

		// Extract VM name from the Host header.
		// When called through Caddy's forward_auth the real host arrives in
		// X-Forwarded-Host; r.Host is the auth proxy's own address in that case.
		vmName := ap.extractVMName(r)
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
		// The token may be in r.URL (direct call / tests) or in the
		// X-Forwarded-Uri header that Caddy's forward_auth sets.
		if token := ap.extractShareToken(r); token != "" {
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
			// Redirect to the same URL without the ?ussy_share= param so the
			// browser lands on the clean URL with the session cookie set.
			// ap.cleanRedirectURL reconstructs the full URL from X-Forwarded-*
			// headers when called through Caddy's forward_auth.
			w.Header().Set("Location", ap.cleanRedirectURL(r))
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

// extractVMName extracts the VM name from the request's host.
//
// When the request arrives directly (tests, local calls) the VM hostname is
// in r.Host.  When called through Caddy's forward_auth directive, r.Host is
// the auth proxy's own address and the real client-facing hostname is in the
// X-Forwarded-Host header set by Caddy — so we fall back to that.
func (ap *AuthProxy) extractVMName(r *http.Request) string {
	if name := ap.vmNameFromHost(r.Host); name != "" {
		return name
	}
	return ap.vmNameFromHost(r.Header.Get("X-Forwarded-Host"))
}

// vmNameFromHost extracts the VM subdomain label from a bare hostname string.
// e.g., "myvm.ussy.host" -> "myvm", "" or non-matching -> ""
func (ap *AuthProxy) vmNameFromHost(host string) string {
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

// extractShareToken returns the ussy_share query-parameter value, checking
// both the direct URL (used in tests / direct calls) and the X-Forwarded-Uri
// header that Caddy's forward_auth populates with the original request URI.
func (ap *AuthProxy) extractShareToken(r *http.Request) string {
	if token := r.URL.Query().Get("ussy_share"); token != "" {
		return token
	}
	if fu := r.Header.Get("X-Forwarded-Uri"); fu != "" {
		if u, err := url.ParseRequestURI(fu); err == nil {
			return u.Query().Get("ussy_share")
		}
	}
	return ""
}

// cleanRedirectURL builds the redirect URL after stripping ?ussy_share=.
//
// Direct calls (tests): scheme/host come from r.URL; path+query from r.URL.
// Caddy forward_auth calls: scheme from X-Forwarded-Proto, host from
// X-Forwarded-Host, path+query from X-Forwarded-Uri.
func (ap *AuthProxy) cleanRedirectURL(r *http.Request) string {
	// Scheme
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}

	// Host
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}

	// Path + query (strip ussy_share)
	rawURI := r.URL.RequestURI()
	if fu := r.Header.Get("X-Forwarded-Uri"); fu != "" {
		rawURI = fu
	}

	u, _ := url.ParseRequestURI(rawURI)
	if u == nil {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	q.Del("ussy_share")
	u.RawQuery = q.Encode()

	return scheme + "://" + host + u.String()
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
