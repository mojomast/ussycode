package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mojomast/ussycode/internal/db"
)

// LLMGateway proxies requests to LLM providers, handling BYOK key injection,
// rate limiting, and usage tracking.
type LLMGateway interface {
	// Proxy forwards the request to the appropriate LLM provider.
	Proxy(w http.ResponseWriter, r *http.Request, provider string)
	// SetUserKey stores an encrypted API key for a user+provider.
	SetUserKey(ctx context.Context, userID int64, provider, key string) error
	// GetUserKey retrieves the decrypted API key for a user+provider.
	GetUserKey(ctx context.Context, userID int64, provider string) (string, error)
}

// ProviderConfig holds configuration for a single LLM provider.
type ProviderConfig struct {
	Name    string // e.g. "anthropic", "openai", "ollama"
	BaseURL string // upstream URL
	BYOK    bool   // whether this provider needs a user-supplied API key
}

// LLMGatewayConfig holds configuration for the LLM gateway.
type LLMGatewayConfig struct {
	Providers     []ProviderConfig
	RateLimit     float64 // requests per minute per user (default 60)
	EncryptSecret string  // secret for encrypting stored API keys
}

// DefaultLLMGatewayConfig returns a config with default provider URLs.
func DefaultLLMGatewayConfig() *LLMGatewayConfig {
	return &LLMGatewayConfig{
		RateLimit:     60,
		EncryptSecret: "",
		Providers: []ProviderConfig{
			{Name: "anthropic", BaseURL: "https://api.anthropic.com", BYOK: true},
			{Name: "openai", BaseURL: "https://api.openai.com", BYOK: true},
			{Name: "ollama", BaseURL: "http://localhost:11434", BYOK: false},
			{Name: "fireworks", BaseURL: "https://api.fireworks.ai", BYOK: true},
			{Name: "vllm", BaseURL: "http://localhost:8000", BYOK: false},
		},
	}
}

// llmGatewayImpl is the concrete implementation of LLMGateway.
type llmGatewayImpl struct {
	db        *db.DB
	encryptor *KeyEncryptor
	providers map[string]*providerProxy
	limiters  *rateLimiterMap
	logger    *slog.Logger
}

// providerProxy holds a reverse proxy and config for a single LLM provider.
type providerProxy struct {
	config ProviderConfig
	proxy  *httputil.ReverseProxy
	target *url.URL
}

// NewLLMGateway creates a new LLM gateway with the given configuration.
func NewLLMGateway(database *db.DB, cfg *LLMGatewayConfig, logger *slog.Logger) (LLMGateway, error) {
	if cfg == nil {
		cfg = DefaultLLMGatewayConfig()
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 60
	}

	var encryptor *KeyEncryptor
	if cfg.EncryptSecret != "" {
		var err error
		encryptor, err = NewKeyEncryptor(cfg.EncryptSecret)
		if err != nil {
			return nil, fmt.Errorf("create key encryptor: %w", err)
		}
	}

	gw := &llmGatewayImpl{
		db:        database,
		encryptor: encryptor,
		providers: make(map[string]*providerProxy),
		limiters:  newRateLimiterMap(cfg.RateLimit),
		logger:    logger,
	}

	for _, pc := range cfg.Providers {
		target, err := url.Parse(pc.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("parse URL for provider %s: %w", pc.Name, err)
		}

		rp := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				// Director is set per-request below via a closure
			},
			// FlushInterval enables streaming (SSE) passthrough.
			// A negative value means flush immediately after each write.
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				logger.Error("LLM proxy error",
					"provider", pc.Name,
					"error", err,
					"url", r.URL.String(),
				)
				writeJSONError(w, http.StatusBadGateway,
					fmt.Sprintf("upstream error for %s: %v", pc.Name, err))
			},
		}

		gw.providers[pc.Name] = &providerProxy{
			config: pc,
			proxy:  rp,
			target: target,
		}
	}

	return gw, nil
}

// Proxy handles a proxied request to an LLM provider.
func (g *llmGatewayImpl) Proxy(w http.ResponseWriter, r *http.Request, provider string) {
	pp, ok := g.providers[provider]
	if !ok {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown provider: %s", provider))
		return
	}

	// Identify the user from the request context (set by metadata server)
	userID, ok := r.Context().Value(ctxKeyLLMUserID).(int64)
	if !ok {
		writeJSONError(w, http.StatusForbidden, "user not identified")
		return
	}

	// Rate limiting: check per-user token bucket
	if !g.limiters.allow(userID) {
		g.logger.Warn("LLM rate limit exceeded",
			"user_id", userID,
			"provider", provider,
		)
		writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded, try again later")
		return
	}

	// For BYOK providers, inject the user's API key
	var apiKey string
	if pp.config.BYOK {
		var err error
		apiKey, err = g.GetUserKey(r.Context(), userID, provider)
		if err != nil {
			g.logger.Warn("no API key configured",
				"user_id", userID,
				"provider", provider,
				"error", err,
			)
			writeJSONError(w, http.StatusUnauthorized,
				fmt.Sprintf("no API key configured for %s. Use 'llm-key set %s <key>' to configure.", provider, provider))
			return
		}
	}

	// Track usage asynchronously
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Estimate 1 request, rough token estimate from content-length
		tokens := estimateTokens(r)
		if err := g.db.IncrementLLMUsage(ctx, userID, provider, 1, tokens); err != nil {
			g.logger.Error("failed to track LLM usage",
				"user_id", userID,
				"provider", provider,
				"error", err,
			)
		}
	}()

	g.logger.Info("proxying LLM request",
		"user_id", userID,
		"provider", provider,
		"method", r.Method,
		"path", r.URL.Path,
	)

	// Build a per-request director that rewrites the URL and injects auth
	target := pp.target
	pp.proxy.Director = func(req *http.Request) {
		// Rewrite the URL: strip /gateway/llm/<provider> prefix
		originalPath := req.URL.Path
		prefix := "/gateway/llm/" + provider
		subPath := strings.TrimPrefix(originalPath, prefix)
		if subPath == "" {
			subPath = "/"
		}

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, subPath)
		req.Host = target.Host

		// Remove hop-by-hop headers
		req.Header.Del("X-Forwarded-For")

		// Inject API key for BYOK providers
		if apiKey != "" {
			switch provider {
			case "anthropic":
				req.Header.Set("x-api-key", apiKey)
				req.Header.Set("anthropic-version", "2023-06-01")
			case "openai":
				req.Header.Set("Authorization", "Bearer "+apiKey)
			case "fireworks":
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
		}
	}

	pp.proxy.ServeHTTP(w, r)
}

// SetUserKey encrypts and stores an API key for a user+provider.
func (g *llmGatewayImpl) SetUserKey(ctx context.Context, userID int64, provider, key string) error {
	if g.encryptor == nil {
		return fmt.Errorf("key encryption not configured (set LLM_ENCRYPT_SECRET)")
	}

	// Validate provider
	if _, ok := g.providers[provider]; !ok {
		return fmt.Errorf("unknown provider: %s", provider)
	}

	encrypted, err := g.encryptor.Encrypt(key)
	if err != nil {
		return fmt.Errorf("encrypt key: %w", err)
	}

	return g.db.SetLLMKey(ctx, userID, provider, encrypted)
}

// GetUserKey retrieves and decrypts the API key for a user+provider.
func (g *llmGatewayImpl) GetUserKey(ctx context.Context, userID int64, provider string) (string, error) {
	if g.encryptor == nil {
		return "", fmt.Errorf("key encryption not configured")
	}

	encrypted, err := g.db.GetLLMKey(ctx, userID, provider)
	if err != nil {
		return "", fmt.Errorf("get key from db: %w", err)
	}

	return g.encryptor.Decrypt(encrypted)
}

// --- Rate Limiter ---

// rateLimiterMap provides per-user token bucket rate limiting.
type rateLimiterMap struct {
	mu       sync.Mutex
	buckets  map[int64]*tokenBucket
	rate     float64 // tokens per minute
	capacity int     // max burst
}

type tokenBucket struct {
	tokens   float64
	lastTime time.Time
}

func newRateLimiterMap(ratePerMinute float64) *rateLimiterMap {
	return &rateLimiterMap{
		buckets:  make(map[int64]*tokenBucket),
		rate:     ratePerMinute,
		capacity: int(ratePerMinute), // burst = rate
	}
}

// allow checks if a request is allowed for the given user.
func (m *rateLimiterMap) allow(userID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	bucket, ok := m.buckets[userID]
	if !ok {
		bucket = &tokenBucket{
			tokens:   float64(m.capacity) - 1, // consume one token
			lastTime: now,
		}
		m.buckets[userID] = bucket
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(bucket.lastTime).Minutes()
	bucket.tokens += elapsed * m.rate
	if bucket.tokens > float64(m.capacity) {
		bucket.tokens = float64(m.capacity)
	}
	bucket.lastTime = now

	if bucket.tokens < 1 {
		return false
	}

	bucket.tokens--
	return true
}

// --- Context Keys ---

type llmCtxKey int

const (
	ctxKeyLLMUserID llmCtxKey = iota
)

// WithLLMUserID returns a context with the user ID set for LLM proxy use.
func WithLLMUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, ctxKeyLLMUserID, userID)
}

// --- Helpers ---

// estimateTokens provides a rough token count estimate from the request.
// Uses ~4 chars per token as a rough heuristic.
func estimateTokens(r *http.Request) int {
	if r.ContentLength > 0 {
		return int(r.ContentLength / 4)
	}
	return 100 // default estimate for unknown content length
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
