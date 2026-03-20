package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
)

// mockSMTPSend captures SMTP send calls for testing.
type mockSMTPSend struct {
	calls []smtpSendCall
	err   error // error to return
}

type smtpSendCall struct {
	addr string
	from string
	to   []string
	msg  []byte
}

func (m *mockSMTPSend) sendFunc() SMTPSendFunc {
	return func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		m.calls = append(m.calls, smtpSendCall{
			addr: addr,
			from: from,
			to:   to,
			msg:  msg,
		})
		return m.err
	}
}

func TestEmailSend_Success(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Create user with email
	user, err := database.CreateUser(ctx, "emailuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := database.SetUserEmail(ctx, user.ID, "user@example.com"); err != nil {
		t.Fatalf("SetUserEmail: %v", err)
	}

	cfg := &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		FromAddress:      "noreply@ussy.host",
		RatePerHourPerVM: 10,
	}

	sender := NewEmailSender(database, cfg, logger)
	mock := &mockSMTPSend{}
	sender.SetSendFunc(mock.sendFunc())

	meta := &VMMetadata{
		UserID:     user.ID,
		UserHandle: "emailuser",
		VMName:     "testvm",
	}

	body := `{"to":"user@example.com","subject":"Test Subject","body":"Hello world"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	w := httptest.NewRecorder()

	sender.HandleEmailSend(w, req, meta)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result EmailSendResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Status != "sent" {
		t.Errorf("expected status 'sent', got %q", result.Status)
	}

	// Verify SMTP was called
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 SMTP call, got %d", len(mock.calls))
	}
	call := mock.calls[0]
	if call.from != "noreply@ussy.host" {
		t.Errorf("expected from 'noreply@ussy.host', got %q", call.from)
	}
	if len(call.to) != 1 || call.to[0] != "user@example.com" {
		t.Errorf("expected to ['user@example.com'], got %v", call.to)
	}
	msgStr := string(call.msg)
	if !strings.Contains(msgStr, "Subject: Test Subject") {
		t.Error("message missing Subject header")
	}
	if !strings.Contains(msgStr, "Hello world") {
		t.Error("message missing body")
	}
	if !strings.Contains(msgStr, "X-Ussycode-VM: testvm") {
		t.Error("message missing X-Ussycode-VM header")
	}
}

func TestEmailSend_WrongRecipient(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user, err := database.CreateUser(ctx, "wrongrecipuser")
	if err != nil {
		t.Fatal(err)
	}
	database.SetUserEmail(ctx, user.ID, "owner@example.com")

	sender := NewEmailSender(database, &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		RatePerHourPerVM: 10,
	}, logger)
	mock := &mockSMTPSend{}
	sender.SetSendFunc(mock.sendFunc())

	meta := &VMMetadata{
		UserID:     user.ID,
		UserHandle: "wrongrecipuser",
		VMName:     "testvm",
	}

	// Try sending to a different address
	body := `{"to":"hacker@evil.com","subject":"Test","body":"Hello"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	w := httptest.NewRecorder()

	sender.HandleEmailSend(w, req, meta)

	resp := w.Result()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	// Verify no SMTP calls were made
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 SMTP calls, got %d", len(mock.calls))
	}
}

func TestEmailSend_NoEmailConfigured(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user, err := database.CreateUser(ctx, "noemailuser")
	if err != nil {
		t.Fatal(err)
	}
	// Don't set email

	sender := NewEmailSender(database, &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		RatePerHourPerVM: 10,
	}, logger)

	meta := &VMMetadata{
		UserID:     user.ID,
		UserHandle: "noemailuser",
		VMName:     "testvm",
	}

	body := `{"to":"anyone@example.com","subject":"Test","body":"Hello"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	w := httptest.NewRecorder()

	sender.HandleEmailSend(w, req, meta)

	resp := w.Result()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestEmailSend_RateLimit(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user, err := database.CreateUser(ctx, "ratelimituser")
	if err != nil {
		t.Fatal(err)
	}
	database.SetUserEmail(ctx, user.ID, "user@example.com")

	sender := NewEmailSender(database, &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		FromAddress:      "noreply@ussy.host",
		RatePerHourPerVM: 3, // very low for testing
	}, logger)
	mock := &mockSMTPSend{}
	sender.SetSendFunc(mock.sendFunc())

	meta := &VMMetadata{
		UserID:     user.ID,
		UserHandle: "ratelimituser",
		VMName:     "ratelimitvm",
	}

	// Send 3 emails (should succeed)
	for i := 0; i < 3; i++ {
		body := `{"to":"user@example.com","subject":"Test","body":"Hello"}`
		req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
		w := httptest.NewRecorder()
		sender.HandleEmailSend(w, req, meta)
		if w.Code != http.StatusOK {
			t.Fatalf("email %d: expected 200, got %d", i, w.Code)
		}
	}

	// 4th should be rate limited
	body := `{"to":"user@example.com","subject":"Test","body":"Hello"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	sender.HandleEmailSend(w, req, meta)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestEmailSend_InvalidJSON(t *testing.T) {
	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender := NewEmailSender(database, nil, logger)

	meta := &VMMetadata{
		UserID:     1,
		UserHandle: "user",
		VMName:     "testvm",
	}

	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	sender.HandleEmailSend(w, req, meta)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestEmailSend_MissingFields(t *testing.T) {
	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender := NewEmailSender(database, nil, logger)

	meta := &VMMetadata{
		UserID:     1,
		UserHandle: "user",
		VMName:     "testvm",
	}

	tests := []struct {
		name string
		body string
	}{
		{"no_to", `{"subject":"Test","body":"Hello"}`},
		{"no_subject", `{"to":"user@example.com","body":"Hello"}`},
		{"no_body", `{"to":"user@example.com","subject":"Test"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			sender.HandleEmailSend(w, req, meta)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", w.Code)
			}
		})
	}
}

func TestEmailSend_MethodNotAllowed(t *testing.T) {
	database := testDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender := NewEmailSender(database, nil, logger)

	meta := &VMMetadata{
		UserID:     1,
		UserHandle: "user",
		VMName:     "testvm",
	}

	req := httptest.NewRequest("GET", "/gateway/email/send", nil)
	w := httptest.NewRecorder()
	sender.HandleEmailSend(w, req, meta)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestEmailSend_SMTPRelayError(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user, err := database.CreateUser(ctx, "smtperroruser")
	if err != nil {
		t.Fatal(err)
	}
	database.SetUserEmail(ctx, user.ID, "user@example.com")

	sender := NewEmailSender(database, &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		FromAddress:      "noreply@ussy.host",
		RatePerHourPerVM: 10,
	}, logger)

	// Mock that returns an error
	mock := &mockSMTPSend{err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "connection refused"}}}
	sender.SetSendFunc(mock.sendFunc())

	meta := &VMMetadata{
		UserID:     user.ID,
		UserHandle: "smtperroruser",
		VMName:     "testvm",
	}

	body := `{"to":"user@example.com","subject":"Test","body":"Hello"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	sender.HandleEmailSend(w, req, meta)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
}

func TestEmailSend_CaseInsensitiveRecipient(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user, err := database.CreateUser(ctx, "caseuser")
	if err != nil {
		t.Fatal(err)
	}
	database.SetUserEmail(ctx, user.ID, "User@Example.COM")

	sender := NewEmailSender(database, &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		FromAddress:      "noreply@ussy.host",
		RatePerHourPerVM: 10,
	}, logger)
	mock := &mockSMTPSend{}
	sender.SetSendFunc(mock.sendFunc())

	meta := &VMMetadata{
		UserID:     user.ID,
		UserHandle: "caseuser",
		VMName:     "testvm",
	}

	// Send with different case - should still work
	body := `{"to":"user@example.com","subject":"Test","body":"Hello"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	sender.HandleEmailSend(w, req, meta)

	if w.Code != http.StatusOK {
		bodyBytes, _ := io.ReadAll(w.Result().Body)
		t.Fatalf("expected 200, got %d: %s", w.Code, string(bodyBytes))
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 SMTP call, got %d", len(mock.calls))
	}
}

func TestMaskEmail(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"user@example.com", "u***@example.com"},
		{"a@b.com", "a@b.com"}, // single char local part
		{"@foo.com", "@foo.com"},
		{"nope", "***"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := maskEmail(tt.input)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestBuildEmailMessage(t *testing.T) {
	meta := &VMMetadata{
		VMName:     "testvm",
		UserHandle: "testuser",
	}

	msg := buildEmailMessage("noreply@ussy.host", "user@example.com", "Test Subject", "Hello body", meta)
	msgStr := string(msg)

	if !strings.Contains(msgStr, "From: noreply@ussy.host") {
		t.Error("missing From header")
	}
	if !strings.Contains(msgStr, "To: user@example.com") {
		t.Error("missing To header")
	}
	if !strings.Contains(msgStr, "Subject: Test Subject") {
		t.Error("missing Subject header")
	}
	if !strings.Contains(msgStr, "X-Ussycode-VM: testvm") {
		t.Error("missing X-Ussycode-VM header")
	}
	if !strings.Contains(msgStr, "MIME-Version: 1.0") {
		t.Error("missing MIME-Version header")
	}

	// Body should be after a blank line (CRLF CRLF)
	parts := strings.SplitN(msgStr, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("expected header/body separation")
	}
	if !strings.Contains(parts[1], "Hello body") {
		t.Error("missing body content")
	}
}

// TestMetadataServer_EmailSendWiring tests that the metadata server
// correctly wires through to the EmailSender.
func TestMetadataServer_EmailSendWiring(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user, err := database.CreateUser(ctx, "metauser")
	if err != nil {
		t.Fatal(err)
	}
	database.SetUserEmail(ctx, user.ID, "meta@example.com")

	// Create metadata server
	srv := NewServer(":0", logger)
	srv.RegisterVM("192.168.1.1", &VMMetadata{
		InstanceID: "test-1",
		LocalIPv4:  "192.168.1.1",
		Hostname:   "testvm",
		UserID:     user.ID,
		UserHandle: "metauser",
		VMName:     "metavm",
	})

	// Create and wire email sender
	sender := NewEmailSender(database, &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		FromAddress:      "noreply@ussy.host",
		RatePerHourPerVM: 10,
	}, logger)
	mock := &mockSMTPSend{}
	sender.SetSendFunc(mock.sendFunc())
	srv.SetEmailSender(sender)

	// Create request
	body := `{"to":"meta@example.com","subject":"Wiring Test","body":"Works!"}`
	req := httptest.NewRequest("POST", "/gateway/email/send", strings.NewReader(body))
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()

	// Use the handler directly
	handler := srv.Handler()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		bodyBytes, _ := io.ReadAll(w.Result().Body)
		t.Fatalf("expected 200, got %d: %s", w.Code, string(bodyBytes))
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 SMTP call, got %d", len(mock.calls))
	}
}

// Ensure bytes import is used
var _ = bytes.Buffer{}
