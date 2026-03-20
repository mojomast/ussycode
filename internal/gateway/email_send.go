// Package gateway - email_send.go implements outbound email sending from VMs.
// VMs can send emails via POST /gateway/email/send, but only to the
// VM owner's registered email address. Rate limited to 10/hour/VM.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/mojomast/ussycode/internal/db"
)

// EmailSendConfig holds configuration for the outbound email service.
type EmailSendConfig struct {
	// SMTPRelay is the address of the SMTP relay server (host:port).
	SMTPRelay string

	// SMTPUsername is the optional SMTP AUTH username.
	SMTPUsername string

	// SMTPPassword is the optional SMTP AUTH password.
	SMTPPassword string

	// FromAddress is the From: address for outbound emails.
	FromAddress string

	// RatePerHourPerVM limits outbound emails per hour per VM. Default: 10.
	RatePerHourPerVM int
}

// DefaultEmailSendConfig returns an EmailSendConfig with sensible defaults.
func DefaultEmailSendConfig() *EmailSendConfig {
	return &EmailSendConfig{
		SMTPRelay:        "localhost:25",
		FromAddress:      "noreply@ussy.host",
		RatePerHourPerVM: 10,
	}
}

// EmailSender handles outbound email from VMs.
type EmailSender struct {
	cfg    *EmailSendConfig
	db     *db.DB
	send   SMTPSendFunc // pluggable for testing
	logger *slog.Logger

	// Per-VM send rate limiting
	rateMu  sync.Mutex
	vmRates map[string]*emailSendBucket
}

// emailSendBucket tracks outbound email rate per VM.
type emailSendBucket struct {
	count    int
	windowAt time.Time
}

// SMTPSendFunc is the function signature for sending email via SMTP.
// This matches the signature used internally and can be replaced in tests.
type SMTPSendFunc func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error

// NewEmailSender creates a new outbound email sender.
func NewEmailSender(database *db.DB, cfg *EmailSendConfig, logger *slog.Logger) *EmailSender {
	if cfg == nil {
		cfg = DefaultEmailSendConfig()
	}
	if cfg.RatePerHourPerVM <= 0 {
		cfg.RatePerHourPerVM = 10
	}
	if cfg.FromAddress == "" {
		cfg.FromAddress = "noreply@ussy.host"
	}

	return &EmailSender{
		cfg:     cfg,
		db:      database,
		send:    smtp.SendMail,
		logger:  logger,
		vmRates: make(map[string]*emailSendBucket),
	}
}

// SetSendFunc overrides the SMTP send function (useful for testing).
func (e *EmailSender) SetSendFunc(fn SMTPSendFunc) {
	e.send = fn
}

// EmailSendRequest is the JSON body for POST /gateway/email/send.
type EmailSendRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// EmailSendResponse is the JSON response for email send.
type EmailSendResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// HandleEmailSend processes an outbound email request from a VM.
// The caller must provide the VM metadata (resolved from the request IP).
func (e *EmailSender) HandleEmailSend(w http.ResponseWriter, r *http.Request, meta *VMMetadata) {
	if r.Method != http.MethodPost {
		writeEmailJSON(w, http.StatusMethodNotAllowed, EmailSendResponse{
			Status: "error",
			Error:  "method not allowed",
		})
		return
	}

	// Parse request body
	var req EmailSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeEmailJSON(w, http.StatusBadRequest, EmailSendResponse{
			Status: "error",
			Error:  fmt.Sprintf("invalid JSON: %v", err),
		})
		return
	}

	// Validate required fields
	if req.To == "" || req.Subject == "" || req.Body == "" {
		writeEmailJSON(w, http.StatusBadRequest, EmailSendResponse{
			Status: "error",
			Error:  "fields 'to', 'subject', and 'body' are required",
		})
		return
	}

	// Validate recipient: must be the VM owner's email
	ownerEmail, err := e.db.GetUserEmail(r.Context(), meta.UserID)
	if err != nil {
		e.logger.Error("failed to get owner email",
			"user_id", meta.UserID,
			"error", err,
		)
		writeEmailJSON(w, http.StatusInternalServerError, EmailSendResponse{
			Status: "error",
			Error:  "failed to look up owner email",
		})
		return
	}

	if ownerEmail == "" {
		writeEmailJSON(w, http.StatusForbidden, EmailSendResponse{
			Status: "error",
			Error:  "VM owner has no email configured. Set your email with 'email set <address>'",
		})
		return
	}

	if !strings.EqualFold(req.To, ownerEmail) {
		writeEmailJSON(w, http.StatusForbidden, EmailSendResponse{
			Status: "error",
			Error:  fmt.Sprintf("can only send email to the VM owner (%s)", maskEmail(ownerEmail)),
		})
		return
	}

	// Rate limit check
	if !e.checkSendRate(meta.VMName) {
		e.logger.Warn("email send rate limit exceeded",
			"vm", meta.VMName,
			"user", meta.UserHandle,
		)
		writeEmailJSON(w, http.StatusTooManyRequests, EmailSendResponse{
			Status: "error",
			Error:  fmt.Sprintf("rate limit exceeded (max %d emails/hour per VM)", e.cfg.RatePerHourPerVM),
		})
		return
	}

	// Build the email message in RFC 2822 format
	msg := buildEmailMessage(e.cfg.FromAddress, req.To, req.Subject, req.Body, meta)

	// Send via SMTP relay
	var auth smtp.Auth
	if e.cfg.SMTPUsername != "" {
		host, _, _ := net.SplitHostPort(e.cfg.SMTPRelay)
		auth = smtp.PlainAuth("", e.cfg.SMTPUsername, e.cfg.SMTPPassword, host)
	}

	if err := e.send(e.cfg.SMTPRelay, auth, e.cfg.FromAddress, []string{req.To}, msg); err != nil {
		e.logger.Error("SMTP send failed",
			"vm", meta.VMName,
			"to", req.To,
			"error", err,
		)
		writeEmailJSON(w, http.StatusBadGateway, EmailSendResponse{
			Status: "error",
			Error:  fmt.Sprintf("SMTP relay error: %v", err),
		})
		return
	}

	e.logger.Info("email sent",
		"vm", meta.VMName,
		"user", meta.UserHandle,
		"to", req.To,
		"subject", req.Subject,
	)

	writeEmailJSON(w, http.StatusOK, EmailSendResponse{
		Status:  "sent",
		Message: fmt.Sprintf("email sent to %s", req.To),
	})
}

// checkSendRate checks and increments the per-VM outbound rate counter.
func (e *EmailSender) checkSendRate(vmName string) bool {
	e.rateMu.Lock()
	defer e.rateMu.Unlock()

	now := time.Now()
	currentHour := now.Truncate(time.Hour)

	bucket, ok := e.vmRates[vmName]
	if !ok || !bucket.windowAt.Equal(currentHour) {
		e.vmRates[vmName] = &emailSendBucket{
			count:    1,
			windowAt: currentHour,
		}
		return true
	}

	if bucket.count >= e.cfg.RatePerHourPerVM {
		return false
	}

	bucket.count++
	return true
}

// buildEmailMessage constructs an RFC 2822 formatted email.
func buildEmailMessage(from, to, subject, body string, meta *VMMetadata) []byte {
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString(fmt.Sprintf("X-Ussycode-VM: %s\r\n", meta.VMName))
	msg.WriteString(fmt.Sprintf("X-Ussycode-User: %s\r\n", meta.UserHandle))
	msg.WriteString("\r\n")
	msg.WriteString(body)
	return []byte(msg.String())
}

// maskEmail partially masks an email address for privacy.
// e.g., "user@example.com" -> "u***@example.com"
func maskEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return "***"
	}
	local := parts[0]
	if len(local) > 1 {
		local = string(local[0]) + "***"
	}
	return local + "@" + parts[1]
}

// writeEmailJSON writes a JSON response for email endpoints.
func writeEmailJSON(w http.ResponseWriter, code int, resp EmailSendResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(resp)
}

// EmailSenderForContext is a context key type for the email sender.
type emailSenderCtxKey int

const ctxKeyEmailSender emailSenderCtxKey = 0

// WithEmailSender returns a context with the email sender attached.
func WithEmailSender(ctx context.Context, sender *EmailSender) context.Context {
	return context.WithValue(ctx, ctxKeyEmailSender, sender)
}

// EmailSenderFromContext retrieves the email sender from a context.
func EmailSenderFromContext(ctx context.Context) *EmailSender {
	sender, _ := ctx.Value(ctxKeyEmailSender).(*EmailSender)
	return sender
}
