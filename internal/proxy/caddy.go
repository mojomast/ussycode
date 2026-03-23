// Package proxy implements a reverse proxy manager backed by Caddy's admin API.
//
// In the aligned self-hosting topology, nginx owns public :80/:443 and forwards
// traffic to an internal-only Caddy listener. ussycode uses the Caddy admin API
// (http://localhost:2019) to dynamically add/remove API, admin, VM, and
// custom-domain reverse proxy routes as VMs start and stop.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Manager manages Caddy reverse proxy routes for VMs.
type Manager struct {
	adminAPI      string // Caddy admin API URL (default: http://localhost:2019)
	domain        string // base domain (e.g., "ussy.host")
	apiDomain     string // public API hostname (e.g. api.ussy.host)
	adminUpstream string // internal admin upstream
	apiUpstream   string // internal API upstream
	authUpstream  string // internal auth proxy upstream
	client        *http.Client
	logger        *slog.Logger
	mu            sync.Mutex
	routes        map[string]string // vmName -> vmIP
}

// Config holds configuration for the proxy manager.
type Config struct {
	AdminAPI      string // Caddy admin API URL
	Domain        string // base domain for VM subdomains
	APIDomain     string // public hostname for the API
	AdminUpstream string // internal admin upstream, e.g. 127.0.0.1:9090
	APIUpstream   string // internal API upstream, e.g. 127.0.0.1:8080
	AuthUpstream  string // internal auth proxy upstream, e.g. 127.0.0.1:9876
}

// NewManager creates a new Caddy proxy manager.
func NewManager(cfg *Config, logger *slog.Logger) *Manager {
	adminAPI := cfg.AdminAPI
	if adminAPI == "" {
		adminAPI = "http://localhost:2019"
	}

	adminUpstream := cfg.AdminUpstream
	if adminUpstream == "" {
		adminUpstream = "127.0.0.1:9090"
	}
	apiUpstream := cfg.APIUpstream
	if apiUpstream == "" {
		apiUpstream = "127.0.0.1:8080"
	}
	authUpstream := cfg.AuthUpstream
	if authUpstream == "" {
		authUpstream = "127.0.0.1:9876"
	}

	return &Manager{
		adminAPI:      adminAPI,
		domain:        cfg.Domain,
		apiDomain:     cfg.APIDomain,
		adminUpstream: adminUpstream,
		apiUpstream:   apiUpstream,
		authUpstream:  authUpstream,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
		routes: make(map[string]string),
	}
}

// AddRoute registers a reverse proxy route for a VM.
// Requests to vmName.domain will be proxied to vmIP:port.
func (m *Manager) AddRoute(ctx context.Context, vmName, vmIP string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if port == 0 {
		port = 8080 // default port
	}

	upstream := fmt.Sprintf("%s:%d", vmIP, port)
	hostname := fmt.Sprintf("%s.%s", vmName, m.domain)

	m.logger.Info("adding proxy route", "hostname", hostname, "upstream", upstream)

	// Build a Caddy route config
	route := caddyRoute{
		ID: routeID(vmName),
		Match: []caddyMatch{
			{Host: []string{hostname}},
		},
		Handle: []caddyHandler{
			{
				Handler: "subroute",
				Routes: []caddySubroute{
					{
						Handle: []caddyHandler{
							{
								Handler: "headers",
								Request: &caddyHeaderOps{
									Set: map[string][]string{
										"X-Forwarded-Proto": {"https"},
										"X-Forwarded-Host":  {hostname},
									},
								},
							},
							{
								Handler: "reverse_proxy",
								Upstreams: []caddyUpstream{
									{Dial: upstream},
								},
							},
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal route: %w", err)
	}

	// Use Caddy's /config/apps/http/servers/srv0/routes API
	// POST adds a new route
	url := fmt.Sprintf("%s/config/apps/http/servers/srv0/routes", m.adminAPI)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	m.routes[vmName] = vmIP
	m.logger.Info("proxy route added", "hostname", hostname, "upstream", upstream)
	return nil
}

// RemoveRoute removes the reverse proxy route for a VM.
func (m *Manager) RemoveRoute(ctx context.Context, vmName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := routeID(vmName)
	hostname := fmt.Sprintf("%s.%s", vmName, m.domain)

	m.logger.Info("removing proxy route", "hostname", hostname)

	// Use Caddy's /id/ endpoint to remove by route ID
	url := fmt.Sprintf("%s/id/%s", m.adminAPI, id)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		m.logger.Warn("caddy route removal failed (may not exist)",
			"id", id, "status", resp.StatusCode, "body", string(respBody))
	}

	delete(m.routes, vmName)
	return nil
}

// UpdateRoute changes the upstream for an existing VM route.
func (m *Manager) UpdateRoute(ctx context.Context, vmName, vmIP string, port int) error {
	// Simplest approach: remove and re-add
	_ = m.RemoveRoute(ctx, vmName)
	return m.AddRoute(ctx, vmName, vmIP, port)
}

// EnsureBaseConfig pushes a base Caddy configuration for the internal-only
// nginx-edge topology. Call this once at startup to ensure Caddy serves the
// control-plane hosts and leaves public TLS/listeners to nginx.
func (m *Manager) EnsureBaseConfig(ctx context.Context) error {
	m.logger.Info("configuring Caddy base config", "domain", m.domain, "api_domain", m.apiDomain)

	routes := []caddyRoute{
		{
			ID:    "ussycode-healthz",
			Match: []caddyMatch{{Path: []string{"/healthz"}}},
			Handle: []caddyHandler{{
				Handler:    "static_response",
				StatusCode: http.StatusOK,
			}},
		},
	}

	if m.apiDomain != "" {
		routes = append(routes, caddyRoute{
			ID:    "ussycode-api",
			Match: []caddyMatch{{Host: []string{m.apiDomain}}},
			Handle: []caddyHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: m.apiUpstream}},
			}},
		})
	}

	routes = append(routes,
		caddyRoute{
			ID:    "ussycode-admin",
			Match: []caddyMatch{{Host: []string{m.domain}}},
			Handle: []caddyHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: m.adminUpstream}},
			}},
		},
	)

	cfg := caddyConfig{
		Admin: caddyAdmin{Listen: "localhost:2019"},
		Apps: caddyApps{
			HTTP: caddyHTTP{
				HTTPPort:  8085,
				HTTPSPort: 8443,
				Servers: map[string]caddyServer{
					"srv0": {
						Listen: []string{"127.0.0.1:8085"},
						AutomaticHTTPS: &caddyAutomaticHTTPS{
							Disable:          true,
							DisableRedirects: true,
						},
						Routes: routes,
					},
				},
			},
		},
	}

	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	url := fmt.Sprintf("%s/load", m.adminAPI)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy load error (status %d): %s", resp.StatusCode, string(respBody))
	}

	m.logger.Info("Caddy base config loaded")
	return nil
}

// AddCustomDomain registers a reverse proxy route for a custom domain pointing
// to a VM's existing upstream. The VM must already have a route registered.
func (m *Manager) AddCustomDomain(ctx context.Context, domain, vmName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vmIP, ok := m.routes[vmName]
	if !ok {
		return fmt.Errorf("no active route for VM %q", vmName)
	}

	upstream := fmt.Sprintf("%s:8080", vmIP)

	m.logger.Info("adding custom domain route", "domain", domain, "upstream", upstream)

	route := caddyRoute{
		ID: customDomainRouteID(domain),
		Match: []caddyMatch{
			{Host: []string{domain}},
		},
		Handle: []caddyHandler{
			{
				Handler: "subroute",
				Routes: []caddySubroute{
					{
						Handle: []caddyHandler{
							{
								Handler: "headers",
								Request: &caddyHeaderOps{
									Set: map[string][]string{
										"X-Forwarded-Proto": {"https"},
										"X-Forwarded-Host":  {domain},
									},
								},
							},
							{
								Handler: "reverse_proxy",
								Upstreams: []caddyUpstream{
									{Dial: upstream},
								},
							},
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal route: %w", err)
	}

	url := fmt.Sprintf("%s/config/apps/http/servers/srv0/routes", m.adminAPI)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	m.logger.Info("custom domain route added", "domain", domain, "upstream", upstream)
	return nil
}

// RemoveCustomDomain removes the reverse proxy route for a custom domain.
func (m *Manager) RemoveCustomDomain(ctx context.Context, domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := customDomainRouteID(domain)

	m.logger.Info("removing custom domain route", "domain", domain)

	url := fmt.Sprintf("%s/id/%s", m.adminAPI, id)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		m.logger.Warn("custom domain route removal failed (may not exist)",
			"domain", domain, "status", resp.StatusCode, "body", string(respBody))
	}

	return nil
}

// ListRoutes returns the currently tracked VM routes.
func (m *Manager) ListRoutes() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	routes := make(map[string]string, len(m.routes))
	for k, v := range m.routes {
		routes[k] = v
	}
	return routes
}

// Healthy checks if the Caddy admin API is reachable.
func (m *Manager) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", m.adminAPI+"/config/", nil)
	if err != nil {
		return false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Logger returns the manager's logger.
func (m *Manager) Logger() *slog.Logger {
	return m.logger
}

// --- helper: route ID ---

func routeID(vmName string) string {
	return "ussycode-vm-" + vmName
}

func customDomainRouteID(domain string) string {
	return "ussycode-cname-" + domain
}

// --- Caddy JSON API types ---

type caddyConfig struct {
	Admin caddyAdmin `json:"admin,omitempty"`
	Apps  caddyApps  `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen,omitempty"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
}

type caddyHTTP struct {
	HTTPPort  int                    `json:"http_port,omitempty"`
	HTTPSPort int                    `json:"https_port,omitempty"`
	Servers   map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen         []string             `json:"listen"`
	AutomaticHTTPS *caddyAutomaticHTTPS `json:"automatic_https,omitempty"`
	Routes         []caddyRoute         `json:"routes"`
}

type caddyAutomaticHTTPS struct {
	Disable          bool `json:"disable,omitempty"`
	DisableRedirects bool `json:"disable_redirects,omitempty"`
}

type caddyRoute struct {
	ID     string         `json:"@id,omitempty"`
	Match  []caddyMatch   `json:"match,omitempty"`
	Handle []caddyHandler `json:"handle"`
}

type caddyMatch struct {
	Host []string `json:"host,omitempty"`
	Path []string `json:"path,omitempty"`
}

type caddyHandler struct {
	Handler    string                    `json:"handler"`
	Routes     []caddySubroute           `json:"routes,omitempty"`
	Upstreams  []caddyUpstream           `json:"upstreams,omitempty"`
	StatusCode int                       `json:"status_code,omitempty"`
	Body       string                    `json:"body,omitempty"`
	Request    *caddyHeaderOps           `json:"request,omitempty"`
	Headers    map[string]caddyHeaderOps `json:"headers,omitempty"`
}

type caddySubroute struct {
	Match  []caddyMatch   `json:"match,omitempty"`
	Handle []caddyHandler `json:"handle"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

type caddyHeaderOps struct {
	Set map[string][]string `json:"set,omitempty"`
}
