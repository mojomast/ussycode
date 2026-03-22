// Package proxy implements a reverse proxy manager that integrates with
// Caddy to provide automatic TLS and per-VM subdomain routing.
//
// It uses the Caddy admin API (http://localhost:2019) to dynamically
// add/remove reverse proxy routes as VMs start and stop.
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
	adminAPI     string // Caddy admin API URL (default: http://localhost:2019)
	domain       string // base domain (e.g., "ussy.host")
	authProxyURL string // URL of the auth proxy for Caddy forward_auth (e.g., "http://localhost:9876")
	client       *http.Client
	logger       *slog.Logger
	mu           sync.Mutex
	routes       map[string]string // vmName -> vmIP
}

// Config holds configuration for the proxy manager.
type Config struct {
	AdminAPI     string // Caddy admin API URL
	Domain       string // base domain for VM subdomains
	AuthProxyURL string // URL of the auth proxy for Caddy forward_auth (e.g., "http://localhost:9876")
}

// NewManager creates a new Caddy proxy manager.
func NewManager(cfg *Config, logger *slog.Logger) *Manager {
	adminAPI := cfg.AdminAPI
	if adminAPI == "" {
		adminAPI = "http://localhost:2019"
	}

	return &Manager{
		adminAPI:     adminAPI,
		domain:       cfg.Domain,
		authProxyURL: cfg.AuthProxyURL,
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

	// Build the subroute handler chain: auth check → header injection → upstream proxy.
	// forward_auth is only added when an auth proxy URL is configured; skipping it
	// in dev/test mode (authProxyURL == "") means routes work without Caddy auth.
	subrouteHandles := []caddyHandler{}

	if m.authProxyURL != "" {
		subrouteHandles = append(subrouteHandles, caddyHandler{
			Handler: "forward_auth",
			URI:     m.authProxyURL,
			// Copy auth-context headers from the auth proxy response into
			// the upstream request so the VM sees who is calling it.
			CopyHeaders: []string{
				"X-Ussy-Auth-Mode",
				"X-Ussy-VM",
				"X-Ussy-VM-ID",
				"X-Ussy-UserID",
				"X-Ussy-Handle",
			},
		})
	}

	subrouteHandles = append(subrouteHandles,
		caddyHandler{
			Handler: "headers",
			Request: &caddyHeaderOps{
				Set: map[string][]string{
					"X-Forwarded-Proto": {"https"},
					"X-Forwarded-Host":  {hostname},
				},
			},
		},
		caddyHandler{
			Handler: "reverse_proxy",
			Upstreams: []caddyUpstream{
				{Dial: upstream},
			},
		},
	)

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
					{Handle: subrouteHandles},
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

// EnsureBaseConfig pushes a base Caddy configuration that sets up the
// wildcard TLS certificate and default server. Call this once at startup.
func (m *Manager) EnsureBaseConfig(ctx context.Context, email string) error {
	m.logger.Info("configuring Caddy base config", "domain", m.domain, "email", email)

	wildcardDomain := fmt.Sprintf("*.%s", m.domain)

	cfg := caddyConfig{
		Apps: caddyApps{
			HTTP: caddyHTTP{
				Servers: map[string]caddyServer{
					"srv0": {
						Listen: []string{":443", ":80"},
						Routes: []caddyRoute{
							// Default 404 handler for unmatched subdomains
							{
								Handle: []caddyHandler{
									{
										Handler:    "static_response",
										StatusCode: 404,
										Body:       "no such environment\n",
									},
								},
							},
						},
					},
				},
			},
			TLS: &caddyTLS{
				Automation: caddyTLSAutomation{
					Policies: []caddyTLSPolicy{
						{
							Subjects: []string{m.domain, wildcardDomain},
							Issuers: []caddyTLSIssuer{
								{
									Module: "acme",
									Email:  email,
									Challenges: &caddyACMEChallenges{
										DNS: &caddyDNSChallenge{
											Provider: caddyDNSProvider{
												Name: "cloudflare",
											},
										},
									},
								},
							},
						},
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
	Apps caddyApps `json:"apps"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
	TLS  *caddyTLS `json:"tls,omitempty"`
}

type caddyHTTP struct {
	Servers map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen []string     `json:"listen"`
	Routes []caddyRoute `json:"routes"`
}

type caddyRoute struct {
	ID     string         `json:"@id,omitempty"`
	Match  []caddyMatch   `json:"match,omitempty"`
	Handle []caddyHandler `json:"handle"`
}

type caddyMatch struct {
	Host []string `json:"host,omitempty"`
}

type caddyHandler struct {
	Handler     string          `json:"handler"`
	Routes      []caddySubroute `json:"routes,omitempty"`
	Upstreams   []caddyUpstream `json:"upstreams,omitempty"`
	StatusCode  int             `json:"status_code,omitempty"`
	Body        string          `json:"body,omitempty"`
	Request     *caddyHeaderOps `json:"request,omitempty"`
	// forward_auth fields
	URI         string          `json:"uri,omitempty"`
	CopyHeaders []string        `json:"copy_headers,omitempty"`
}

type caddySubroute struct {
	Handle []caddyHandler `json:"handle"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

type caddyHeaderOps struct {
	Set map[string][]string `json:"set,omitempty"`
}

type caddyTLS struct {
	Automation caddyTLSAutomation `json:"automation"`
}

type caddyTLSAutomation struct {
	Policies []caddyTLSPolicy `json:"policies"`
}

type caddyTLSPolicy struct {
	Subjects []string         `json:"subjects"`
	Issuers  []caddyTLSIssuer `json:"issuers"`
}

type caddyTLSIssuer struct {
	Module     string               `json:"module"`
	Email      string               `json:"email,omitempty"`
	Challenges *caddyACMEChallenges `json:"challenges,omitempty"`
}

type caddyACMEChallenges struct {
	DNS *caddyDNSChallenge `json:"dns,omitempty"`
}

type caddyDNSChallenge struct {
	Provider caddyDNSProvider `json:"provider"`
}

type caddyDNSProvider struct {
	Name string `json:"name"`
}
