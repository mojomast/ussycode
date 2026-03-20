// Package gateway - email.go implements a minimal SMTP server that accepts
// inbound email for VM users and delivers to Maildir format inside VM rootfs.
//
// The SMTP server implements the bare minimum commands: HELO/EHLO, MAIL FROM,
// RCPT TO, DATA, RSET, NOOP, QUIT. It accepts mail for *@<vm>.<domain>
// patterns and delivers to the VM's Maildir/new/ directory.
package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SMTPConfig holds configuration for the inbound SMTP server.
type SMTPConfig struct {
	// ListenAddr is the address to listen on (e.g., ":25" or ":2525").
	ListenAddr string

	// Domain is the base domain for VM subdomains (e.g., "ussy.host").
	// Mail to *@<vm>.<domain> is accepted.
	Domain string

	// RootfsDir is the base directory where VM rootfs disks are mounted
	// or where Maildir directories should be created.
	// Maildir path: <RootfsDir>/<vmName>/home/ussycode/Maildir/new/
	RootfsDir string

	// MaxUnread is the maximum number of unread files in Maildir/new before
	// delivery is auto-disabled for that VM. Default: 1000.
	MaxUnread int

	// RatePerHourPerVM limits the number of emails accepted per hour per VM.
	// Default: 100.
	RatePerHourPerVM int

	// Hostname is the SMTP server hostname announced in HELO/EHLO responses.
	Hostname string
}

// DefaultSMTPConfig returns an SMTPConfig with sensible defaults.
func DefaultSMTPConfig() *SMTPConfig {
	return &SMTPConfig{
		ListenAddr:       ":2525",
		Domain:           "ussy.host",
		RootfsDir:        "/var/lib/ussycode/disks",
		MaxUnread:        1000,
		RatePerHourPerVM: 100,
		Hostname:         "mail.ussy.host",
	}
}

// SMTPServer is a minimal SMTP server that delivers to Maildir.
type SMTPServer struct {
	cfg      *SMTPConfig
	logger   *slog.Logger
	listener net.Listener

	// vmRateLimiters tracks per-VM email rate limiting.
	rateMu  sync.Mutex
	vmRates map[string]*emailRateBucket

	// done is closed when the server is shutting down.
	done chan struct{}
}

// emailRateBucket is a simple sliding-window rate counter per VM.
type emailRateBucket struct {
	count    int
	windowAt time.Time // start of the current hour window
}

// NewSMTPServer creates a new minimal SMTP server.
func NewSMTPServer(cfg *SMTPConfig, logger *slog.Logger) *SMTPServer {
	if cfg == nil {
		cfg = DefaultSMTPConfig()
	}
	if cfg.MaxUnread <= 0 {
		cfg.MaxUnread = 1000
	}
	if cfg.RatePerHourPerVM <= 0 {
		cfg.RatePerHourPerVM = 100
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "localhost"
	}

	return &SMTPServer{
		cfg:     cfg,
		logger:  logger,
		vmRates: make(map[string]*emailRateBucket),
		done:    make(chan struct{}),
	}
}

// Start starts the SMTP server. Blocks until ctx is cancelled.
func (s *SMTPServer) Start(ctx context.Context) error {
	var err error
	s.listener, err = net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("smtp listen: %w", err)
	}

	s.logger.Info("SMTP server starting",
		"addr", s.cfg.ListenAddr,
		"domain", s.cfg.Domain,
		"hostname", s.cfg.Hostname,
	)

	// Shutdown goroutine
	go func() {
		<-ctx.Done()
		close(s.done)
		s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil // graceful shutdown
			default:
				s.logger.Error("SMTP accept error", "error", err)
				continue
			}
		}
		go s.handleConn(conn)
	}
}

// Listener returns the underlying net.Listener, useful for tests.
func (s *SMTPServer) Listener() net.Listener {
	return s.listener
}

// ListenAndServe creates a listener and serves, for use in tests that
// want to assign an ephemeral port.
func (s *SMTPServer) ListenAndServe(ctx context.Context, listener net.Listener) error {
	s.listener = listener

	s.logger.Info("SMTP server starting",
		"addr", listener.Addr().String(),
		"domain", s.cfg.Domain,
	)

	go func() {
		<-ctx.Done()
		close(s.done)
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
				s.logger.Error("SMTP accept error", "error", err)
				continue
			}
		}
		go s.handleConn(conn)
	}
}

// smtpSession holds the state for a single SMTP connection.
type smtpSession struct {
	server   *SMTPServer
	conn     net.Conn
	reader   *bufio.Reader
	writer   *bufio.Writer
	heloName string
	mailFrom string
	rcptTo   []string
	vmName   string // extracted from first valid RCPT TO
}

// handleConn handles a single SMTP connection.
func (s *SMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	// Set a generous read deadline
	conn.SetDeadline(time.Now().Add(5 * time.Minute))

	sess := &smtpSession{
		server: s,
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}

	// Send greeting
	sess.writeLine("220 %s ESMTP ussycode", s.cfg.Hostname)

	for {
		line, err := sess.readLine()
		if err != nil {
			if err != io.EOF {
				s.logger.Debug("SMTP read error", "error", err, "remote", conn.RemoteAddr())
			}
			return
		}

		// Parse command (case-insensitive, first 4 chars or space-delimited)
		cmd, arg := parseSMTPCommand(line)

		switch cmd {
		case "HELO":
			sess.handleHELO(arg)
		case "EHLO":
			sess.handleEHLO(arg)
		case "MAIL":
			sess.handleMAIL(arg)
		case "RCPT":
			sess.handleRCPT(arg)
		case "DATA":
			sess.handleDATA()
		case "RSET":
			sess.handleRSET()
		case "NOOP":
			sess.writeLine("250 OK")
		case "QUIT":
			sess.writeLine("221 Bye")
			return
		default:
			sess.writeLine("502 Command not recognized")
		}
	}
}

func (sess *smtpSession) writeLine(format string, args ...any) {
	fmt.Fprintf(sess.writer, format+"\r\n", args...)
	sess.writer.Flush()
}

func (sess *smtpSession) readLine() (string, error) {
	line, err := sess.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// parseSMTPCommand splits an SMTP line into command and argument.
func parseSMTPCommand(line string) (cmd, arg string) {
	line = strings.TrimSpace(line)
	parts := strings.SplitN(line, " ", 2)
	cmd = strings.ToUpper(parts[0])
	if len(parts) > 1 {
		arg = parts[1]
	}
	return cmd, arg
}

func (sess *smtpSession) handleHELO(arg string) {
	sess.heloName = arg
	sess.writeLine("250 %s Hello %s", sess.server.cfg.Hostname, arg)
}

func (sess *smtpSession) handleEHLO(arg string) {
	sess.heloName = arg
	sess.writeLine("250-%s Hello %s", sess.server.cfg.Hostname, arg)
	sess.writeLine("250-SIZE 10485760") // 10MB max
	sess.writeLine("250-8BITMIME")
	sess.writeLine("250 OK")
}

// handleMAIL handles MAIL FROM:<addr>
func (sess *smtpSession) handleMAIL(arg string) {
	// Extract address from "FROM:<addr>" (case-insensitive prefix)
	upper := strings.ToUpper(arg)
	if !strings.HasPrefix(upper, "FROM:") {
		sess.writeLine("501 Syntax: MAIL FROM:<address>")
		return
	}

	addr := extractAngleBracketAddr(arg[5:])
	sess.mailFrom = addr
	sess.rcptTo = nil
	sess.vmName = ""
	sess.writeLine("250 OK")
}

// handleRCPT handles RCPT TO:<addr>
func (sess *smtpSession) handleRCPT(arg string) {
	if sess.mailFrom == "" {
		sess.writeLine("503 Need MAIL command first")
		return
	}

	upper := strings.ToUpper(arg)
	if !strings.HasPrefix(upper, "TO:") {
		sess.writeLine("501 Syntax: RCPT TO:<address>")
		return
	}

	addr := extractAngleBracketAddr(arg[3:])
	if addr == "" {
		sess.writeLine("501 Syntax: RCPT TO:<address>")
		return
	}

	// Validate recipient domain: must be *@<vm>.<domain>
	vmName, ok := sess.server.extractVMFromAddress(addr)
	if !ok {
		sess.writeLine("550 No such user - we only accept mail for *@<vm>.%s", sess.server.cfg.Domain)
		return
	}

	// Use the first valid VM name for delivery
	if sess.vmName == "" {
		sess.vmName = vmName
	}

	sess.rcptTo = append(sess.rcptTo, addr)
	sess.writeLine("250 OK")
}

// handleDATA reads the message body and delivers to Maildir.
func (sess *smtpSession) handleDATA() {
	if len(sess.rcptTo) == 0 {
		sess.writeLine("503 Need RCPT command first")
		return
	}

	sess.writeLine("354 Start mail input; end with <CRLF>.<CRLF>")

	// Read message body until a line containing just "."
	var body strings.Builder
	for {
		line, err := sess.readLine()
		if err != nil {
			sess.server.logger.Error("SMTP DATA read error", "error", err)
			return
		}
		if line == "." {
			break
		}
		// Dot-stuffing: a line starting with ".." means literal "."
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		body.WriteString(line)
		body.WriteString("\r\n")
	}

	// Deliver to Maildir
	err := sess.server.deliverToMaildir(sess.vmName, sess.mailFrom, sess.rcptTo, body.String())
	if err != nil {
		sess.server.logger.Error("SMTP delivery failed",
			"vm", sess.vmName,
			"from", sess.mailFrom,
			"error", err,
		)
		sess.writeLine("452 %s", err.Error())
		return
	}

	sess.server.logger.Info("SMTP message delivered",
		"vm", sess.vmName,
		"from", sess.mailFrom,
		"rcpt", sess.rcptTo,
		"size", body.Len(),
	)

	sess.writeLine("250 OK: message delivered")
}

func (sess *smtpSession) handleRSET() {
	sess.mailFrom = ""
	sess.rcptTo = nil
	sess.vmName = ""
	sess.writeLine("250 OK")
}

// extractVMFromAddress parses an email address and extracts the VM name
// from the domain part. Expected format: *@<vmname>.<domain>
func (s *SMTPServer) extractVMFromAddress(addr string) (vmName string, ok bool) {
	parts := strings.SplitN(addr, "@", 2)
	if len(parts) != 2 {
		return "", false
	}

	domain := strings.ToLower(parts[1])
	baseDomain := strings.ToLower(s.cfg.Domain)

	// Must end with .<domain>
	if !strings.HasSuffix(domain, "."+baseDomain) {
		return "", false
	}

	// Extract the subdomain part (everything before .<domain>)
	vmName = strings.TrimSuffix(domain, "."+baseDomain)
	if vmName == "" || strings.Contains(vmName, ".") {
		// Don't allow empty or nested subdomains
		return "", false
	}

	return vmName, true
}

// extractAngleBracketAddr extracts an email address from optional angle brackets.
// e.g., "<user@example.com>" -> "user@example.com"
//
//	"user@example.com" -> "user@example.com"
func extractAngleBracketAddr(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

// deliverToMaildir writes the message to the VM's Maildir/new/ directory.
// Returns an error if rate limited, too many unread, or filesystem errors.
func (s *SMTPServer) deliverToMaildir(vmName, from string, rcptTo []string, body string) error {
	// Rate limit check
	if !s.checkVMRate(vmName) {
		return fmt.Errorf("rate limit exceeded for VM %s (max %d/hour)", vmName, s.cfg.RatePerHourPerVM)
	}

	// Build Maildir path: <RootfsDir>/<vmName>/home/ussycode/Maildir/new/
	maildirNew := filepath.Join(s.cfg.RootfsDir, vmName, "home", "ussycode", "Maildir", "new")

	// Check unread count (auto-disable if >MaxUnread)
	unreadCount, err := countFilesInDir(maildirNew)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("check unread: %w", err)
	}
	if unreadCount >= s.cfg.MaxUnread {
		return fmt.Errorf("mailbox full for VM %s (%d unread, max %d)", vmName, unreadCount, s.cfg.MaxUnread)
	}

	// Ensure Maildir structure exists (new, cur, tmp)
	for _, sub := range []string{"new", "cur", "tmp"} {
		dir := filepath.Join(s.cfg.RootfsDir, vmName, "home", "ussycode", "Maildir", sub)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create maildir %s: %w", sub, err)
		}
	}

	// Generate unique filename: <timestamp>.<random>.ussyco.de
	filename := generateMaildirFilename()

	// Write to tmp first, then rename to new (Maildir spec: atomic delivery)
	tmpPath := filepath.Join(s.cfg.RootfsDir, vmName, "home", "ussycode", "Maildir", "tmp", filename)
	newPath := filepath.Join(maildirNew, filename)

	// Build full message with envelope headers prepended
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("Return-Path: <%s>\r\n", from))
	msg.WriteString(fmt.Sprintf("Delivered-To: %s\r\n", strings.Join(rcptTo, ", ")))
	msg.WriteString(fmt.Sprintf("X-Ussycode-VM: %s\r\n", vmName))
	msg.WriteString(body)

	if err := os.WriteFile(tmpPath, []byte(msg.String()), 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}

	if err := os.Rename(tmpPath, newPath); err != nil {
		// Cleanup tmp on rename failure
		os.Remove(tmpPath)
		return fmt.Errorf("rename to new: %w", err)
	}

	return nil
}

// generateMaildirFilename creates a unique filename for Maildir delivery.
// Format: <unix_timestamp>.<random_hex>.ussyco.de
func generateMaildirFilename() string {
	now := time.Now()
	randBytes := make([]byte, 8)
	rand.Read(randBytes)
	return fmt.Sprintf("%d.%x.ussyco.de", now.UnixNano(), randBytes)
}

// checkVMRate checks and increments the per-VM rate counter.
// Returns true if the email is allowed, false if rate limited.
func (s *SMTPServer) checkVMRate(vmName string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	now := time.Now()
	currentHour := now.Truncate(time.Hour)

	bucket, ok := s.vmRates[vmName]
	if !ok || !bucket.windowAt.Equal(currentHour) {
		// New window or first time
		s.vmRates[vmName] = &emailRateBucket{
			count:    1,
			windowAt: currentHour,
		}
		return true
	}

	if bucket.count >= s.cfg.RatePerHourPerVM {
		return false
	}

	bucket.count++
	return true
}

// countFilesInDir counts the number of regular files in a directory.
// Returns 0 and nil if the directory doesn't exist.
func countFilesInDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	return count, nil
}
