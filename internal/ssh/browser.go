package ssh

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/mojomast/ussycode/internal/telemetry"
)

func init() {
	RegisterCommand("browser", cmdBrowser)
}

// cmdBrowser generates a one-time magic link for browser-based authentication.
// Usage:
//
//	browser          -- generate a magic link URL
//	browser --qr     -- generate a magic link with QR code (future enhancement)
func cmdBrowser(s *Shell, args []string) error {
	ctx, span := telemetry.Start(context.Background(), "ssh.browser.magic_link")
	defer span.End()
	showQR := hasArgFlag(args, "--qr")

	// Generate a cryptographically random 32-byte token
	token, err := generateMagicToken()
	if err != nil {
		return fmt.Errorf("generate token: %w", err)
	}

	// Token expires in 5 minutes
	expiresAt := time.Now().UTC().Add(5 * time.Minute)

	// Store in DB
	if err := s.gw.DB.CreateMagicToken(ctx, s.user.ID, token, expiresAt); err != nil {
		telemetry.RecordBrowserToken(ctx, "create_failed")
		return fmt.Errorf("store token: %w", err)
	}
	telemetry.RecordBrowserToken(ctx, "created")

	url := fmt.Sprintf("https://%s/__auth/magic/%s", s.gw.domain, token)

	s.writeln("")
	s.writeln("  \033[1mBrowser Access\033[0m")
	s.writeln("")
	s.writef("  Open this URL in your browser:\n")
	s.writeln("")
	s.writef("    \033[36m%s\033[0m\n", url)
	s.writeln("")
	s.writef("  This link expires in 5 minutes and can only be used once.\n")
	s.writeln("")

	if showQR {
		// Render a simple ASCII QR representation
		// Full QR encoding is complex; we render a visual placeholder
		// that makes the URL easy to find, and note this as a future enhancement.
		renderASCIIQR(s, url)
	}

	return nil
}

// generateMagicToken creates a cryptographically random URL-safe token.
// Returns a 32-byte random value encoded as base64url (43 characters).
func generateMagicToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// renderASCIIQR renders a simplified ASCII representation of a QR-like box
// around the URL for easy visual identification. A full QR implementation
// would require a third-party library (e.g., github.com/skip2/go-qrcode).
func renderASCIIQR(s *Shell, url string) {
	s.writeln("  \033[1mQR Code (simplified):\033[0m")
	s.writeln("")

	// Create a bordered box that's easy to spot
	width := len(url) + 6
	if width < 40 {
		width = 40
	}

	// Top border with QR-like pattern
	topBar := "  "
	for i := 0; i < width; i++ {
		if i%2 == 0 {
			topBar += "█"
		} else {
			topBar += "░"
		}
	}
	s.writeln(topBar)

	// URL line
	s.writef("  █░ \033[36m%s\033[0m", url)
	padding := width - len(url) - 4
	for i := 0; i < padding; i++ {
		s.writef(" ")
	}
	s.writef("░█\n")

	// Bottom border
	s.writeln(topBar)
	s.writeln("")
	s.writeln("  \033[33mNote: For a scannable QR code, use a QR generator with the URL above.\033[0m")
	s.writeln("  \033[33mFull QR rendering is planned for a future release.\033[0m")
	s.writeln("")
}
