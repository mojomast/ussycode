package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mojomast/ussycode/internal/db"
)

// testDB creates a temporary database for testing.
func testDB(t *testing.T) *db.DB {
	t.Helper()
	f, err := os.CreateTemp("", "llm-test-*.db")
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

func TestLLMProxy_MockUpstream(t *testing.T) {
	// Set up a mock upstream LLM server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"response": "hello from mock LLM",
			"path":     r.URL.Path,
			"auth":     r.Header.Get("Authorization"),
			"api_key":  r.Header.Get("x-api-key"),
		})
	}))
	defer upstream.Close()

	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &LLMGatewayConfig{
		RateLimit:     60,
		EncryptSecret: "test-secret-key-for-encryption",
		Providers: []ProviderConfig{
			{Name: "ollama", BaseURL: upstream.URL, BYOK: false},
			{Name: "anthropic", BaseURL: upstream.URL, BYOK: true},
			{Name: "openai", BaseURL: upstream.URL, BYOK: true},
		},
	}

	gw, err := NewLLMGateway(database, cfg, logger)
	if err != nil {
		t.Fatalf("NewLLMGateway: %v", err)
	}

	// Create a test user
	user, err := database.CreateUser(context.Background(), "testuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	t.Run("proxy_selfhosted_no_auth", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/gateway/llm/ollama/v1/chat/completions", strings.NewReader(`{"model":"llama3"}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		gw.Proxy(w, req, "ollama")

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["response"] != "hello from mock LLM" {
			t.Errorf("unexpected response: %v", body)
		}
		// Self-hosted should not have auth
		if body["auth"] != "" {
			t.Errorf("expected no auth header, got %q", body["auth"])
		}
	})

	t.Run("proxy_byok_no_key_configured", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/gateway/llm/anthropic/v1/messages", strings.NewReader(`{}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		gw.Proxy(w, req, "anthropic")

		resp := w.Result()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("proxy_byok_with_key", func(t *testing.T) {
		// Store a key
		if err := gw.SetUserKey(context.Background(), user.ID, "anthropic", "sk-ant-test-key-123"); err != nil {
			t.Fatalf("SetUserKey: %v", err)
		}

		req := httptest.NewRequest("POST", "/gateway/llm/anthropic/v1/messages", strings.NewReader(`{}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		gw.Proxy(w, req, "anthropic")

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["api_key"] != "sk-ant-test-key-123" {
			t.Errorf("expected anthropic x-api-key header, got %q", body["api_key"])
		}
	})

	t.Run("proxy_openai_with_key", func(t *testing.T) {
		if err := gw.SetUserKey(context.Background(), user.ID, "openai", "sk-openai-test-key"); err != nil {
			t.Fatalf("SetUserKey: %v", err)
		}

		req := httptest.NewRequest("POST", "/gateway/llm/openai/v1/chat/completions", strings.NewReader(`{}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		gw.Proxy(w, req, "openai")

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["auth"] != "Bearer sk-openai-test-key" {
			t.Errorf("expected Bearer token, got %q", body["auth"])
		}
	})

	t.Run("proxy_unknown_provider", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/gateway/llm/unknown/v1/chat", strings.NewReader(`{}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		gw.Proxy(w, req, "unknown")

		resp := w.Result()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("proxy_no_user_context", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/gateway/llm/ollama/v1/chat", strings.NewReader(`{}`))
		w := httptest.NewRecorder()

		gw.Proxy(w, req, "ollama")

		resp := w.Result()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403, got %d", resp.StatusCode)
		}
	})
}

func TestLLMKeyEncryption(t *testing.T) {
	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &LLMGatewayConfig{
		RateLimit:     60,
		EncryptSecret: "my-super-secret-key",
		Providers: []ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com", BYOK: true},
		},
	}

	gw, err := NewLLMGateway(database, cfg, logger)
	if err != nil {
		t.Fatalf("NewLLMGateway: %v", err)
	}

	user, err := database.CreateUser(context.Background(), "cryptouser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	originalKey := "sk-test-123456789-very-secret"

	// Store key
	if err := gw.SetUserKey(context.Background(), user.ID, "openai", originalKey); err != nil {
		t.Fatalf("SetUserKey: %v", err)
	}

	// Verify the encrypted key in DB is NOT the plaintext
	encryptedKey, err := database.GetLLMKey(context.Background(), user.ID, "openai")
	if err != nil {
		t.Fatalf("GetLLMKey from DB: %v", err)
	}
	if encryptedKey == originalKey {
		t.Fatal("key stored in plaintext, expected encrypted")
	}

	// Retrieve and verify decryption
	decrypted, err := gw.GetUserKey(context.Background(), user.ID, "openai")
	if err != nil {
		t.Fatalf("GetUserKey: %v", err)
	}
	if decrypted != originalKey {
		t.Errorf("expected %q, got %q", originalKey, decrypted)
	}
}

func TestRateLimiting(t *testing.T) {
	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Very low rate limit: 3 requests per minute
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := &LLMGatewayConfig{
		RateLimit:     3,
		EncryptSecret: "test-secret",
		Providers: []ProviderConfig{
			{Name: "ollama", BaseURL: upstream.URL, BYOK: false},
		},
	}

	gw, err := NewLLMGateway(database, cfg, logger)
	if err != nil {
		t.Fatalf("NewLLMGateway: %v", err)
	}

	user, err := database.CreateUser(context.Background(), "ratelimituser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// First 3 requests should succeed (bucket starts full)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/gateway/llm/ollama/v1/chat", strings.NewReader(`{}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		gw.Proxy(w, req, "ollama")
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// 4th request should be rate limited
	req := httptest.NewRequest("POST", "/gateway/llm/ollama/v1/chat", strings.NewReader(`{}`))
	ctx := WithLLMUserID(req.Context(), user.ID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	gw.Proxy(w, req, "ollama")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

func TestUsageTracking(t *testing.T) {
	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := &LLMGatewayConfig{
		RateLimit:     100,
		EncryptSecret: "test-secret",
		Providers: []ProviderConfig{
			{Name: "ollama", BaseURL: upstream.URL, BYOK: false},
		},
	}

	gw, err := NewLLMGateway(database, cfg, logger)
	if err != nil {
		t.Fatalf("NewLLMGateway: %v", err)
	}

	user, err := database.CreateUser(context.Background(), "usageuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Make a few requests
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/gateway/llm/ollama/v1/chat", strings.NewReader(`{"prompt":"hello"}`))
		ctx := WithLLMUserID(req.Context(), user.ID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		gw.Proxy(w, req, "ollama")
	}

	// Wait a bit for async usage tracking goroutines to finish
	time.Sleep(200 * time.Millisecond)

	// Check usage was recorded
	usage, err := database.GetLLMUsage(context.Background(), user.ID, "ollama")
	if err != nil {
		t.Fatalf("GetLLMUsage: %v", err)
	}
	if usage.RequestCount != 3 {
		t.Errorf("expected 3 requests tracked, got %d", usage.RequestCount)
	}
}

func TestKeyEncryptor(t *testing.T) {
	enc, err := NewKeyEncryptor("test-secret")
	if err != nil {
		t.Fatalf("NewKeyEncryptor: %v", err)
	}

	original := "sk-ant-api-key-test-12345"

	// Encrypt
	ciphertext, err := enc.Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Verify ciphertext is different from plaintext
	if ciphertext == original {
		t.Fatal("ciphertext should differ from plaintext")
	}

	// Decrypt
	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if decrypted != original {
		t.Errorf("expected %q, got %q", original, decrypted)
	}

	// Encrypting same value twice should produce different ciphertexts (random nonce)
	ciphertext2, err := enc.Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt2: %v", err)
	}
	if ciphertext == ciphertext2 {
		t.Error("two encryptions of same plaintext should produce different ciphertext")
	}

	// Wrong key should fail to decrypt
	enc2, err := NewKeyEncryptor("different-secret")
	if err != nil {
		t.Fatalf("NewKeyEncryptor2: %v", err)
	}
	_, err = enc2.Decrypt(ciphertext)
	if err == nil {
		t.Error("expected error decrypting with wrong key")
	}
}

func TestNewKeyEncryptor_EmptySecret(t *testing.T) {
	_, err := NewKeyEncryptor("")
	if err == nil {
		t.Error("expected error for empty secret")
	}
}

func TestLLMGateway_SSEStreaming(t *testing.T) {
	// Mock SSE streaming upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		for i := 0; i < 3; i++ {
			w.Write([]byte("data: {\"chunk\":" + string(rune('0'+i)) + "}\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &LLMGatewayConfig{
		RateLimit:     100,
		EncryptSecret: "test-secret",
		Providers: []ProviderConfig{
			{Name: "ollama", BaseURL: upstream.URL, BYOK: false},
		},
	}

	gw, err := NewLLMGateway(database, cfg, logger)
	if err != nil {
		t.Fatalf("NewLLMGateway: %v", err)
	}

	user, err := database.CreateUser(context.Background(), "sseuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest("POST", "/gateway/llm/ollama/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	ctx := WithLLMUserID(req.Context(), user.ID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	gw.Proxy(w, req, "ollama")

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify SSE events came through
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Errorf("expected SSE stream with DONE event, got: %s", bodyStr)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream content type, got %s", resp.Header.Get("Content-Type"))
	}
}

func TestDBLLMQueries(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "dbllmuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Set key
	if err := database.SetLLMKey(ctx, user.ID, "openai", "encrypted-key-data"); err != nil {
		t.Fatalf("SetLLMKey: %v", err)
	}

	// Get key
	key, err := database.GetLLMKey(ctx, user.ID, "openai")
	if err != nil {
		t.Fatalf("GetLLMKey: %v", err)
	}
	if key != "encrypted-key-data" {
		t.Errorf("expected 'encrypted-key-data', got %q", key)
	}

	// Update key (upsert)
	if err := database.SetLLMKey(ctx, user.ID, "openai", "new-encrypted-key"); err != nil {
		t.Fatalf("SetLLMKey (update): %v", err)
	}
	key, err = database.GetLLMKey(ctx, user.ID, "openai")
	if err != nil {
		t.Fatalf("GetLLMKey after update: %v", err)
	}
	if key != "new-encrypted-key" {
		t.Errorf("expected 'new-encrypted-key', got %q", key)
	}

	// List providers
	if err := database.SetLLMKey(ctx, user.ID, "anthropic", "another-key"); err != nil {
		t.Fatalf("SetLLMKey anthropic: %v", err)
	}
	providers, err := database.LLMKeyProvidersByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("LLMKeyProvidersByUser: %v", err)
	}
	if len(providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(providers))
	}

	// Delete key
	if err := database.DeleteLLMKey(ctx, user.ID, "openai"); err != nil {
		t.Fatalf("DeleteLLMKey: %v", err)
	}
	providers, err = database.LLMKeyProvidersByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("LLMKeyProvidersByUser after delete: %v", err)
	}
	if len(providers) != 1 {
		t.Errorf("expected 1 provider after delete, got %d", len(providers))
	}

	// Usage tracking
	if err := database.IncrementLLMUsage(ctx, user.ID, "openai", 5, 1000); err != nil {
		t.Fatalf("IncrementLLMUsage: %v", err)
	}
	if err := database.IncrementLLMUsage(ctx, user.ID, "openai", 3, 500); err != nil {
		t.Fatalf("IncrementLLMUsage2: %v", err)
	}

	usage, err := database.GetLLMUsage(ctx, user.ID, "openai")
	if err != nil {
		t.Fatalf("GetLLMUsage: %v", err)
	}
	if usage.RequestCount != 8 {
		t.Errorf("expected 8 requests, got %d", usage.RequestCount)
	}
	if usage.EstimatedTokens != 1500 {
		t.Errorf("expected 1500 tokens, got %d", usage.EstimatedTokens)
	}
}
