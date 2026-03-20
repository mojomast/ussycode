package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSMTPServer starts an SMTP server on an ephemeral port and returns it.
func testSMTPServer(t *testing.T, rootfsDir string) (*SMTPServer, string) {
	t.Helper()

	cfg := &SMTPConfig{
		ListenAddr:       "127.0.0.1:0",
		Domain:           "test.ussy.host",
		RootfsDir:        rootfsDir,
		MaxUnread:        100,
		RatePerHourPerVM: 10,
		Hostname:         "smtp.test.ussy.host",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewSMTPServer(cfg, logger)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go srv.ListenAndServe(ctx, listener)

	// Give the server a moment to start accepting
	time.Sleep(10 * time.Millisecond)

	return srv, listener.Addr().String()
}

// smtpClient is a simple SMTP client for testing.
type smtpClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialSMTP(t *testing.T, addr string) *smtpClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial SMTP: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return &smtpClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
}

func (c *smtpClient) readLine() string {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimRight(line, "\r\n")
}

// readMultiLine reads SMTP multi-line responses (lines with - continuation).
func (c *smtpClient) readMultiLine() []string {
	var lines []string
	for {
		line := c.readLine()
		lines = append(lines, line)
		// Multi-line responses have a dash after the code (e.g., "250-")
		// Last line has a space after the code (e.g., "250 ")
		if len(line) < 4 || line[3] != '-' {
			break
		}
	}
	return lines
}

func (c *smtpClient) send(format string, args ...any) {
	fmt.Fprintf(c.conn, format+"\r\n", args...)
}

func (c *smtpClient) sendAndRead(format string, args ...any) string {
	c.send(format, args...)
	return c.readLine()
}

func TestSMTPServer_BasicSession(t *testing.T) {
	tmpDir := t.TempDir()
	_, addr := testSMTPServer(t, tmpDir)

	client := dialSMTP(t, addr)

	// Read greeting
	greeting := client.readLine()
	if !strings.HasPrefix(greeting, "220 ") {
		t.Fatalf("expected 220 greeting, got: %q", greeting)
	}

	// EHLO
	client.send("EHLO test.example.com")
	ehloLines := client.readMultiLine()
	if len(ehloLines) == 0 || !strings.HasPrefix(ehloLines[0], "250") {
		t.Fatalf("expected 250 EHLO response, got: %v", ehloLines)
	}

	// MAIL FROM
	resp := client.sendAndRead("MAIL FROM:<sender@example.com>")
	if !strings.HasPrefix(resp, "250") {
		t.Fatalf("expected 250 for MAIL FROM, got: %q", resp)
	}

	// RCPT TO - valid VM address
	resp = client.sendAndRead("RCPT TO:<user@myvm.test.ussy.host>")
	if !strings.HasPrefix(resp, "250") {
		t.Fatalf("expected 250 for RCPT TO, got: %q", resp)
	}

	// DATA
	resp = client.sendAndRead("DATA")
	if !strings.HasPrefix(resp, "354") {
		t.Fatalf("expected 354 for DATA, got: %q", resp)
	}

	// Send message body
	client.send("Subject: Test Email")
	client.send("From: sender@example.com")
	client.send("To: user@myvm.test.ussy.host")
	client.send("")
	client.send("Hello from the test!")
	resp = client.sendAndRead(".")
	if !strings.HasPrefix(resp, "250") {
		t.Fatalf("expected 250 after DATA, got: %q", resp)
	}

	// QUIT
	resp = client.sendAndRead("QUIT")
	if !strings.HasPrefix(resp, "221") {
		t.Fatalf("expected 221 for QUIT, got: %q", resp)
	}

	// Verify file was delivered to Maildir
	maildirNew := filepath.Join(tmpDir, "myvm", "home", "ussycode", "Maildir", "new")
	entries, err := os.ReadDir(maildirNew)
	if err != nil {
		t.Fatalf("read maildir/new: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 message in maildir/new, got %d", len(entries))
	}

	// Verify filename format
	name := entries[0].Name()
	if !strings.HasSuffix(name, ".ussyco.de") {
		t.Errorf("expected filename ending in .ussyco.de, got %q", name)
	}

	// Verify message content
	data, err := os.ReadFile(filepath.Join(maildirNew, name))
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Return-Path: <sender@example.com>") {
		t.Error("message missing Return-Path header")
	}
	if !strings.Contains(content, "X-Ussycode-VM: myvm") {
		t.Error("message missing X-Ussycode-VM header")
	}
	if !strings.Contains(content, "Hello from the test!") {
		t.Error("message missing body content")
	}
}

func TestSMTPServer_InvalidRecipient(t *testing.T) {
	tmpDir := t.TempDir()
	_, addr := testSMTPServer(t, tmpDir)

	client := dialSMTP(t, addr)
	client.readLine() // greeting

	client.sendAndRead("HELO test.example.com")
	client.sendAndRead("MAIL FROM:<sender@example.com>")

	// Try a recipient with wrong domain
	resp := client.sendAndRead("RCPT TO:<user@wrong.domain.com>")
	if !strings.HasPrefix(resp, "550") {
		t.Fatalf("expected 550 for invalid domain, got: %q", resp)
	}

	// Try a recipient at the base domain (no VM subdomain)
	resp = client.sendAndRead("RCPT TO:<user@test.ussy.host>")
	if !strings.HasPrefix(resp, "550") {
		t.Fatalf("expected 550 for base domain, got: %q", resp)
	}
}

func TestSMTPServer_SequenceErrors(t *testing.T) {
	tmpDir := t.TempDir()
	_, addr := testSMTPServer(t, tmpDir)

	client := dialSMTP(t, addr)
	client.readLine() // greeting

	client.sendAndRead("HELO test.example.com")

	// RCPT TO without MAIL FROM
	resp := client.sendAndRead("RCPT TO:<user@myvm.test.ussy.host>")
	if !strings.HasPrefix(resp, "503") {
		t.Fatalf("expected 503 for RCPT without MAIL, got: %q", resp)
	}

	// DATA without RCPT TO
	resp = client.sendAndRead("DATA")
	if !strings.HasPrefix(resp, "503") {
		t.Fatalf("expected 503 for DATA without RCPT, got: %q", resp)
	}
}

func TestSMTPServer_RSET(t *testing.T) {
	tmpDir := t.TempDir()
	_, addr := testSMTPServer(t, tmpDir)

	client := dialSMTP(t, addr)
	client.readLine() // greeting

	client.sendAndRead("HELO test.example.com")
	client.sendAndRead("MAIL FROM:<sender@example.com>")
	client.sendAndRead("RCPT TO:<user@myvm.test.ussy.host>")

	// RSET should clear the state
	resp := client.sendAndRead("RSET")
	if !strings.HasPrefix(resp, "250") {
		t.Fatalf("expected 250 for RSET, got: %q", resp)
	}

	// DATA should fail now (no recipients)
	resp = client.sendAndRead("DATA")
	if !strings.HasPrefix(resp, "503") {
		t.Fatalf("expected 503 after RSET, got: %q", resp)
	}
}

func TestSMTPServer_RateLimit(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a server with a very low rate limit
	cfg := &SMTPConfig{
		ListenAddr:       "127.0.0.1:0",
		Domain:           "test.ussy.host",
		RootfsDir:        tmpDir,
		MaxUnread:        1000,
		RatePerHourPerVM: 3, // very low for testing
		Hostname:         "smtp.test.ussy.host",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewSMTPServer(cfg, logger)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.ListenAndServe(ctx, listener)
	time.Sleep(10 * time.Millisecond)

	addr := listener.Addr().String()

	// Send 3 emails (should succeed)
	for i := 0; i < 3; i++ {
		client := dialSMTP(t, addr)
		client.readLine()
		client.sendAndRead("HELO test")
		client.sendAndRead("MAIL FROM:<sender@example.com>")
		client.sendAndRead("RCPT TO:<user@myvm.test.ussy.host>")
		client.sendAndRead("DATA")
		client.send("Subject: Test %d", i)
		client.send("")
		client.send("Body %d", i)
		resp := client.sendAndRead(".")
		if !strings.HasPrefix(resp, "250") {
			t.Fatalf("email %d: expected 250, got: %q", i, resp)
		}
		client.sendAndRead("QUIT")
	}

	// 4th email should be rate limited
	client := dialSMTP(t, addr)
	client.readLine()
	client.sendAndRead("HELO test")
	client.sendAndRead("MAIL FROM:<sender@example.com>")
	client.sendAndRead("RCPT TO:<user@myvm.test.ussy.host>")
	client.sendAndRead("DATA")
	client.send("Subject: Too many")
	client.send("")
	client.send("Should be rejected")
	resp := client.sendAndRead(".")
	if !strings.HasPrefix(resp, "452") {
		t.Fatalf("expected 452 for rate limit, got: %q", resp)
	}
}

func TestSMTPServer_MaxUnread(t *testing.T) {
	tmpDir := t.TempDir()
	vmName := "fullvm"

	// Pre-create Maildir/new with many files (more than max)
	maildirNew := filepath.Join(tmpDir, vmName, "home", "ussycode", "Maildir", "new")
	if err := os.MkdirAll(maildirNew, 0755); err != nil {
		t.Fatal(err)
	}

	// Create fake unread messages
	for i := 0; i < 101; i++ {
		path := filepath.Join(maildirNew, fmt.Sprintf("%d.fake.ussyco.de", i))
		os.WriteFile(path, []byte("fake"), 0644)
	}

	cfg := &SMTPConfig{
		ListenAddr:       "127.0.0.1:0",
		Domain:           "test.ussy.host",
		RootfsDir:        tmpDir,
		MaxUnread:        100,
		RatePerHourPerVM: 100,
		Hostname:         "smtp.test",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewSMTPServer(cfg, logger)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.ListenAndServe(ctx, listener)
	time.Sleep(10 * time.Millisecond)

	client := dialSMTP(t, listener.Addr().String())
	client.readLine()
	client.sendAndRead("HELO test")
	client.sendAndRead("MAIL FROM:<sender@example.com>")
	client.sendAndRead("RCPT TO:<user@fullvm.test.ussy.host>")
	client.sendAndRead("DATA")
	client.send("Subject: Full mailbox test")
	client.send("")
	client.send("Body")
	resp := client.sendAndRead(".")
	if !strings.HasPrefix(resp, "452") {
		t.Fatalf("expected 452 for full mailbox, got: %q", resp)
	}
}

func TestSMTPServer_NOOP(t *testing.T) {
	tmpDir := t.TempDir()
	_, addr := testSMTPServer(t, tmpDir)

	client := dialSMTP(t, addr)
	client.readLine() // greeting

	resp := client.sendAndRead("NOOP")
	if !strings.HasPrefix(resp, "250") {
		t.Fatalf("expected 250 for NOOP, got: %q", resp)
	}
}

func TestSMTPServer_UnknownCommand(t *testing.T) {
	tmpDir := t.TempDir()
	_, addr := testSMTPServer(t, tmpDir)

	client := dialSMTP(t, addr)
	client.readLine() // greeting

	resp := client.sendAndRead("VRFY user")
	if !strings.HasPrefix(resp, "502") {
		t.Fatalf("expected 502 for unknown command, got: %q", resp)
	}
}

func TestSMTPServer_DotStuffing(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewSMTPServer(&SMTPConfig{
		Domain:           "test.ussy.host",
		RootfsDir:        tmpDir,
		MaxUnread:        100,
		RatePerHourPerVM: 10,
		Hostname:         "smtp.test.ussy.host",
	}, logger)

	sess := &smtpSession{
		server:   srv,
		reader:   bufio.NewReader(strings.NewReader("Subject: Dot stuffing test\r\n\r\nThis line starts with a dot:\r\n..and this had dot-stuffing\r\nRegular line\r\n.\r\n")),
		writer:   bufio.NewWriter(io.Discard),
		mailFrom: "sender@example.com",
		rcptTo:   []string{"user@dotvm.test.ussy.host"},
		vmName:   "dotvm",
	}

	sess.handleDATA()

	maildirNew := filepath.Join(tmpDir, "dotvm", "home", "ussycode", "Maildir", "new")
	entries, err := os.ReadDir(maildirNew)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 message, got %d", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(maildirNew, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, ".and this had dot-stuffing") {
		t.Errorf("dot-stuffing not handled correctly: %s", content)
	}
}

func TestExtractVMFromAddress(t *testing.T) {
	srv := &SMTPServer{
		cfg: &SMTPConfig{Domain: "ussy.host"},
	}

	tests := []struct {
		addr   string
		vmName string
		ok     bool
	}{
		{"user@myvm.ussy.host", "myvm", true},
		{"admin@production.ussy.host", "production", true},
		{"user@ussy.host", "", false},         // no subdomain
		{"user@example.com", "", false},       // wrong domain
		{"user@sub.vm.ussy.host", "", false},  // nested subdomain
		{"invalid", "", false},                // no @ sign
		{"@myvm.ussy.host", "myvm", true},     // empty local part is OK
		{"user@MYVM.USSY.HOST", "myvm", true}, // case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			vmName, ok := srv.extractVMFromAddress(tt.addr)
			if ok != tt.ok {
				t.Errorf("expected ok=%v, got %v", tt.ok, ok)
			}
			if vmName != tt.vmName {
				t.Errorf("expected vmName=%q, got %q", tt.vmName, vmName)
			}
		})
	}
}

func TestExtractAngleBracketAddr(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"<user@example.com>", "user@example.com"},
		{"user@example.com", "user@example.com"},
		{" <user@example.com> ", "user@example.com"},
		{" user@example.com ", "user@example.com"},
		{"<>", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractAngleBracketAddr(tt.input)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestGenerateMaildirFilename(t *testing.T) {
	name1 := generateMaildirFilename()
	name2 := generateMaildirFilename()

	// Both should end with .ussyco.de
	if !strings.HasSuffix(name1, ".ussyco.de") {
		t.Errorf("expected .ussyco.de suffix, got %q", name1)
	}
	if !strings.HasSuffix(name2, ".ussyco.de") {
		t.Errorf("expected .ussyco.de suffix, got %q", name2)
	}

	// Should be unique
	if name1 == name2 {
		t.Error("filenames should be unique")
	}
}
