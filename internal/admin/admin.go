// Package admin implements the ussycode admin web panel.
//
// The panel provides a dashboard for operators and admins to manage
// users, VMs, and system nodes. Authentication is via magic link tokens
// or session cookies — only users with trust_level 'operator' or 'admin'
// can access the panel.
//
// Templates and static assets are embedded via //go:embed directives.
package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mojomast/ussycode/internal/db"
	"github.com/mojomast/ussycode/internal/telemetry"
)

// --- Session management ---

// session stores an authenticated admin session.
type session struct {
	UserID    int64
	Handle    string
	ExpiresAt time.Time
}

// sessionStore is a simple in-memory session store.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

func (s *sessionStore) Create(userID int64, handle string, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	id := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[id] = &session{
		UserID:    userID,
		Handle:    handle,
		ExpiresAt: time.Now().Add(ttl),
	}
	return id, nil
}

func (s *sessionStore) Get(id string) (*session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

func (s *sessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// CleanExpired removes expired sessions.
func (s *sessionStore) CleanExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
}

// --- Dashboard stats ---

// DashboardStats holds counts for the admin dashboard.
type DashboardStats struct {
	TotalUsers     int
	TotalVMs       int
	RunningVMs     int
	StoppedVMs     int
	ErrorVMs       int
	TotalShares    int
	TotalAPITokens int
	RecentUsers    []*db.User
}

// UserRow extends db.User with computed fields for the admin table.
type UserRow struct {
	db.User
	VMCount  int
	KeyCount int
}

// VMRow extends db.VM with the owner handle.
type VMRow struct {
	db.VM
	OwnerHandle string
}

// --- Handler ---

// Handler implements the admin web panel HTTP handler.
type Handler struct {
	db       *db.DB
	sessions *sessionStore
	logger   *slog.Logger
	tmpl     *template.Template
	staticFS fs.FS
	domain   string
}

// Config holds admin handler configuration.
type Config struct {
	Domain string // base domain (e.g., "ussy.host")
}

// NewHandler creates a new admin web panel handler.
// The templateFS should contain "templates/*.html" and "static/*.css".
func NewHandler(database *db.DB, templateFS fs.FS, logger *slog.Logger, cfg *Config) (*Handler, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	domain := cfg.Domain
	if domain == "" {
		domain = "ussy.host"
	}

	funcMap := template.FuncMap{
		"timeAgo": timeAgo,
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"trustBadge":  trustBadge,
		"statusBadge": statusBadge,
		"add":         func(a, b int) int { return a + b },
		"nullStr": func(ns sql.NullString) string {
			if ns.Valid {
				return ns.String
			}
			return "—"
		},
		"nullInt": func(ni sql.NullInt64) string {
			if ni.Valid {
				return strconv.FormatInt(ni.Int64, 10)
			}
			return "—"
		},
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	// Store the static FS for serving CSS
	staticSub, err := fs.Sub(templateFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static sub-fs: %w", err)
	}

	h := &Handler{
		db:       database,
		sessions: newSessionStore(),
		logger:   logger,
		tmpl:     tmpl,
		staticFS: staticSub,
		domain:   domain,
	}

	// Start session cleanup goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			h.sessions.CleanExpired()
		}
	}()

	return h, nil
}

// Routes registers admin routes on the given mux.
// All routes are prefixed with /admin/, plus the shared magic-link auth
// endpoint at /__auth/magic/{token} which is also registered on the main
// HTTP mux (see cmd/ussycode/main.go) so it is reachable from the public
// domain regardless of which backend Caddy routes base-domain traffic to.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/", h.handleDashboard)
	mux.HandleFunc("GET /admin/login", h.handleLoginPage)
	mux.HandleFunc("GET /admin/login/callback", h.handleLoginCallback)
	mux.HandleFunc("POST /admin/logout", h.handleLogout)
	mux.HandleFunc("GET /admin/users", h.handleUsers)
	mux.HandleFunc("GET /admin/users/{id}", h.handleUserDetail)
	mux.HandleFunc("POST /admin/users/{id}/trust", h.handleSetTrustLevel)
	mux.HandleFunc("GET /admin/vms", h.handleVMs)
	mux.HandleFunc("GET /admin/vms/{id}", h.handleVMDetail)
	mux.HandleFunc("GET /admin/nodes", h.handleNodes)
	mux.HandleFunc("GET /admin/static/style.css", h.handleCSS)
	mux.HandleFunc("GET /__auth/magic/{token}", h.HandleMagicLink)
}

// --- Auth middleware ---

const (
	sessionCookieName = "ussy_admin_session"
	sessionTTL        = 24 * time.Hour
)

// requireAuth checks the session cookie and returns the authenticated session.
// If not authenticated, it redirects to login.
func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) (*session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return nil, false
	}

	sess, ok := h.sessions.Get(cookie.Value)
	if !ok {
		// Expired or invalid session
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/admin/",
			MaxAge:   -1,
			HttpOnly: true,
		})
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return nil, false
	}

	return sess, true
}

// --- Handlers ---

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login.html", map[string]interface{}{
		"Error": r.URL.Query().Get("error"),
	})
}

// handleLoginCallback handles the magic link callback.
// The token is passed as a query parameter: /admin/login/callback?token=xxx
func (h *Handler) handleLoginCallback(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Start(r.Context(), "admin.magic_login_callback")
	defer span.End()
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Redirect(w, r, "/admin/login?error=missing+token", http.StatusSeeOther)
		return
	}

	// Validate the magic token (consumes it)
	user, err := h.db.ValidateMagicToken(ctx, token)
	if err != nil {
		telemetry.RecordBrowserToken(ctx, "redeem_failed")
		h.logger.Warn("admin login failed", "error", err)
		http.Redirect(w, r, "/admin/login?error=invalid+or+expired+token", http.StatusSeeOther)
		return
	}
	telemetry.RecordBrowserToken(ctx, "redeemed")

	// Check trust level — must be operator or admin
	if user.TrustLevel != "operator" && user.TrustLevel != "admin" {
		h.logger.Warn("non-admin user attempted admin login",
			"handle", user.Handle, "trust_level", user.TrustLevel)
		http.Redirect(w, r, "/admin/login?error=insufficient+permissions", http.StatusSeeOther)
		return
	}

	// Create session
	sessionID, err := h.sessions.Create(user.ID, user.Handle, sessionTTL)
	if err != nil {
		h.logger.Error("failed to create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/admin/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	h.logger.Info("admin login", "handle", user.Handle, "id", user.ID)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// HandleMagicLink is the one-time magic-link authentication endpoint.
//
//	GET /__auth/magic/{token}
//
// The token is created by the SSH `browser` command and expires after
// 5 minutes. When redeemed the handler:
//
//   - admin/operator users → creates an admin session cookie and redirects
//     to the admin dashboard (/admin/).
//   - any other valid user  → redirects to their first running VM
//     (falls back to / when no VMs exist).
//
// This method is exported so it can be mounted on the main HTTP mux
// (cmd/ussycode/main.go) in addition to the admin-panel mux, ensuring
// the URL is reachable through Caddy on the public-facing domain.
func (h *Handler) HandleMagicLink(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Start(r.Context(), "admin.magic_link")
	defer span.End()

	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	user, err := h.db.ValidateMagicToken(ctx, token)
	if err != nil {
		telemetry.RecordBrowserToken(ctx, "redeem_failed")
		h.logger.Warn("magic link auth failed", "error", err)
		http.Redirect(w, r, "/admin/login?error=invalid+or+expired+token", http.StatusSeeOther)
		return
	}
	telemetry.RecordBrowserToken(ctx, "redeemed")

	h.logger.Info("magic link redeemed", "handle", user.Handle, "trust_level", user.TrustLevel)

	// Admin or operator → create an admin panel session and redirect to the
	// dashboard.
	if user.TrustLevel == "operator" || user.TrustLevel == "admin" {
		sessionID, err := h.sessions.Create(user.ID, user.Handle, sessionTTL)
		if err != nil {
			h.logger.Error("failed to create admin session", "error", err, "user", user.Handle)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    sessionID,
			Path:     "/admin/",
			MaxAge:   int(sessionTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		h.logger.Info("admin magic link login", "handle", user.Handle, "id", user.ID)
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}

	// Regular user → redirect to their first running VM.
	vms, err := h.db.VMsByUser(ctx, user.ID)
	if err == nil && len(vms) > 0 {
		// Prefer a VM that is currently running.
		for _, v := range vms {
			if v.Status == "running" {
				http.Redirect(w, r,
					fmt.Sprintf("https://%s.%s", v.Name, h.domain),
					http.StatusSeeOther)
				return
			}
		}
		// No running VM — use the most-recent one anyway.
		http.Redirect(w, r,
			fmt.Sprintf("https://%s.%s", vms[0].Name, h.domain),
			http.StatusSeeOther)
		return
	}

	// No VMs at all → fall back to the platform root.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		h.sessions.Delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	stats, err := h.getDashboardStats(ctx)
	if err != nil {
		h.logger.Error("failed to get dashboard stats", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "dashboard.html", map[string]interface{}{
		"Session": sess,
		"Stats":   stats,
		"Domain":  h.domain,
	})
}

func (h *Handler) handleUsers(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	users, err := h.listUsers(ctx)
	if err != nil {
		h.logger.Error("failed to list users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "users.html", map[string]interface{}{
		"Session": sess,
		"Users":   users,
		"Domain":  h.domain,
	})
}

func (h *Handler) handleUserDetail(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	user, err := h.db.UserByID(ctx, id)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	vms, err := h.db.VMsByUser(ctx, id)
	if err != nil {
		h.logger.Error("failed to list user VMs", "error", err, "user_id", id)
		vms = nil
	}

	keys, err := h.db.SSHKeysByUser(ctx, id)
	if err != nil {
		h.logger.Error("failed to list user keys", "error", err, "user_id", id)
		keys = nil
	}

	h.render(w, "user_detail.html", map[string]interface{}{
		"Session":     sess,
		"User":        user,
		"VMs":         vms,
		"Keys":        keys,
		"TrustLevels": []string{"newbie", "citizen", "operator", "admin"},
		"Domain":      h.domain,
		"Success":     r.URL.Query().Get("success"),
		"Error":       r.URL.Query().Get("error"),
	})
}

func (h *Handler) handleSetTrustLevel(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	newLevel := r.FormValue("trust_level")
	validLevels := map[string]bool{"newbie": true, "citizen": true, "operator": true, "admin": true}
	if !validLevels[newLevel] {
		http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?error=invalid+trust+level", id), http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	if err := h.db.SetUserTrustLevel(ctx, id, newLevel); err != nil {
		h.logger.Error("failed to set trust level", "error", err, "user_id", id, "level", newLevel)
		http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?error=failed+to+update", id), http.StatusSeeOther)
		return
	}

	actorID := sess.UserID
	targetID := strconv.FormatInt(id, 10)
	detail := fmt.Sprintf("trust_level=%s", newLevel)
	if _, err := h.db.CreateAuditLog(ctx, &actorID, "admin.user.trust_level.set", "user", &targetID, &detail); err != nil {
		h.logger.Warn("failed to write audit log", "error", err, "actor", sess.Handle, "user_id", id, "new_level", newLevel)
	}

	h.logger.Info("trust level updated",
		"admin", sess.Handle, "user_id", id, "new_level", newLevel)
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?success=trust+level+updated", id), http.StatusSeeOther)
}

func (h *Handler) handleVMs(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	vms, err := h.listAllVMs(ctx)
	if err != nil {
		h.logger.Error("failed to list VMs", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "vms.html", map[string]interface{}{
		"Session": sess,
		"VMs":     vms,
		"Domain":  h.domain,
	})
}

func (h *Handler) handleVMDetail(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid VM ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	vmObj, err := h.getVMByID(ctx, id)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}

	owner, err := h.db.UserByID(ctx, vmObj.UserID)
	if err != nil {
		h.logger.Error("failed to get VM owner", "error", err, "vm_id", id)
		owner = &db.User{Handle: "unknown"}
	}

	tags, err := h.db.TagsByVM(ctx, id)
	if err != nil {
		h.logger.Error("failed to get VM tags", "error", err, "vm_id", id)
		tags = nil
	}

	shares, err := h.db.SharesByVM(ctx, id)
	if err != nil {
		h.logger.Error("failed to get VM shares", "error", err, "vm_id", id)
		shares = nil
	}

	h.render(w, "vm_detail.html", map[string]interface{}{
		"Session": sess,
		"VM":      vmObj,
		"Owner":   owner,
		"Tags":    tags,
		"Shares":  shares,
		"Domain":  h.domain,
	})
}

func (h *Handler) handleNodes(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireAuth(w, r)
	if !ok {
		return
	}

	// Nodes are managed by the scheduler and are not stored in SQLite.
	// For now, show a placeholder page. When a scheduler.NodeProvider is
	// wired in, this will display live node status.
	h.render(w, "nodes.html", map[string]interface{}{
		"Session": sess,
		"Domain":  h.domain,
		"Nodes":   []interface{}{},
	})
}

func (h *Handler) handleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	data, err := fs.ReadFile(h.staticFS, "style.css")
	if err != nil {
		h.logger.Error("failed to read style.css", "error", err)
		http.NotFound(w, r)
		return
	}
	w.Write(data)
}

// --- DB queries ---

// getDashboardStats returns aggregate statistics for the dashboard.
func (h *Handler) getDashboardStats(ctx context.Context) (*DashboardStats, error) {
	var stats DashboardStats

	// Use the reader pool directly for simple aggregate counts.
	reader := h.db.Reader()

	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&stats.TotalUsers); err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM vms").Scan(&stats.TotalVMs); err != nil {
		return nil, fmt.Errorf("count vms: %w", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE status = 'running'").Scan(&stats.RunningVMs); err != nil {
		return nil, fmt.Errorf("count running vms: %w", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE status = 'stopped'").Scan(&stats.StoppedVMs); err != nil {
		return nil, fmt.Errorf("count stopped vms: %w", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE status = 'error'").Scan(&stats.ErrorVMs); err != nil {
		return nil, fmt.Errorf("count error vms: %w", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM shares").Scan(&stats.TotalShares); err != nil {
		return nil, fmt.Errorf("count shares: %w", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_tokens WHERE revoked = 0").Scan(&stats.TotalAPITokens); err != nil {
		return nil, fmt.Errorf("count tokens: %w", err)
	}

	// Get 5 most recent users
	recentUsers, err := h.listRecentUsers(ctx, 5)
	if err != nil {
		h.logger.Warn("failed to get recent users", "error", err)
	}
	stats.RecentUsers = recentUsers

	return &stats, nil
}

// listRecentUsers returns the N most recently created users.
func (h *Handler) listRecentUsers(ctx context.Context, limit int) ([]*db.User, error) {
	var users []*db.User
	rows, err := h.db.Reader().QueryContext(ctx,
		`SELECT id, handle, email, trust_level, created_at, updated_at
		 FROM users ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u db.User
		if err := rows.Scan(&u.ID, &u.Handle, &u.Email, &u.TrustLevel, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, rows.Err()
}

// listUsers returns all users with VM and key counts.
func (h *Handler) listUsers(ctx context.Context) ([]*UserRow, error) {
	rows, err := h.db.Reader().QueryContext(ctx,
		`SELECT u.id, u.handle, u.email, u.trust_level, u.created_at, u.updated_at,
		        COALESCE((SELECT COUNT(*) FROM vms WHERE user_id = u.id), 0) AS vm_count,
		        COALESCE((SELECT COUNT(*) FROM ssh_keys WHERE user_id = u.id), 0) AS key_count
		 FROM users u ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*UserRow
	for rows.Next() {
		var ur UserRow
		if err := rows.Scan(&ur.ID, &ur.Handle, &ur.Email, &ur.TrustLevel, &ur.CreatedAt, &ur.UpdatedAt,
			&ur.VMCount, &ur.KeyCount); err != nil {
			return nil, err
		}
		users = append(users, &ur)
	}
	return users, rows.Err()
}

// listAllVMs returns all VMs with owner handles.
func (h *Handler) listAllVMs(ctx context.Context) ([]*VMRow, error) {
	rows, err := h.db.Reader().QueryContext(ctx,
		`SELECT v.id, v.user_id, v.name, v.status, v.image, v.vcpu, v.memory_mb, v.disk_gb,
		        v.tap_device, v.ip_address, v.mac_address, v.pid, v.created_at, v.updated_at,
		        COALESCE(u.handle, 'unknown') AS owner_handle
		 FROM vms v
		 LEFT JOIN users u ON u.id = v.user_id
		 ORDER BY v.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VMRow
	for rows.Next() {
		var vr VMRow
		if err := rows.Scan(&vr.ID, &vr.UserID, &vr.Name, &vr.Status, &vr.Image,
			&vr.VCPU, &vr.MemoryMB, &vr.DiskGB,
			&vr.TapDevice, &vr.IPAddress, &vr.MACAddress, &vr.PID,
			&vr.CreatedAt, &vr.UpdatedAt, &vr.OwnerHandle); err != nil {
			return nil, err
		}
		vms = append(vms, &vr)
	}
	return vms, rows.Err()
}

// getVMByID returns a VM by its ID.
func (h *Handler) getVMByID(ctx context.Context, id int64) (*db.VM, error) {
	var vm db.VM
	row := h.db.Reader().QueryRowContext(ctx,
		`SELECT id, user_id, name, status, image, vcpu, memory_mb, disk_gb,
		        tap_device, ip_address, mac_address, pid, created_at, updated_at
		 FROM vms WHERE id = ?`, id)
	err := row.Scan(&vm.ID, &vm.UserID, &vm.Name, &vm.Status, &vm.Image,
		&vm.VCPU, &vm.MemoryMB, &vm.DiskGB,
		&vm.TapDevice, &vm.IPAddress, &vm.MACAddress, &vm.PID,
		&vm.CreatedAt, &vm.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &vm, nil
}

// render executes a template and writes it to the response.
func (h *Handler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("template render failed", "template", name, "error", err)
		// Don't call http.Error here as we may have already written headers
	}
}

// --- Template helpers ---

// timeAgo returns a human-readable relative time string.
func timeAgo(t db.SQLiteTime) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		hrs := int(d.Hours())
		if hrs == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hrs)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Time.Format("2006-01-02")
	}
}

// trustBadge returns an HTML class for the trust level badge.
func trustBadge(level string) string {
	switch level {
	case "admin":
		return "badge-admin"
	case "operator":
		return "badge-operator"
	case "citizen":
		return "badge-citizen"
	default:
		return "badge-newbie"
	}
}

// statusBadge returns an HTML class for the VM status badge.
func statusBadge(status string) string {
	switch status {
	case "running":
		return "badge-running"
	case "stopped":
		return "badge-stopped"
	case "creating":
		return "badge-creating"
	case "error":
		return "badge-error"
	default:
		return "badge-unknown"
	}
}
