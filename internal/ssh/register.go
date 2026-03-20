package ssh

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/mojomast/ussycode/internal/db"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

var validHandle = regexp.MustCompile(`^[a-z][a-z0-9-]{0,18}[a-z0-9]$`)

// handleRegistration guides a new user through handle selection.
// Returns the created User, or nil if the user cancelled.
func (g *Gateway) handleRegistration(session gssh.Session) (*db.User, error) {
	t := term.NewTerminal(session, "")

	// Welcome banner
	fmt.Fprintln(t, "")
	fmt.Fprintln(t, "  \033[35m╔══════════════════════════════════════════╗\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m          welcome to the ussyverse        \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m                                          \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m   looks like you're new here.            \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m                                          \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m   your ssh key is your identity.         \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m   no passwords. no email. just keys.     \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m                                          \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m║\033[0m   pick a handle:                         \033[35m║\033[0m")
	fmt.Fprintln(t, "  \033[35m╚══════════════════════════════════════════╝\033[0m")
	fmt.Fprintln(t, "")

	t.SetPrompt("  > handle: ")

	ctx := context.Background()

	for attempts := 0; attempts < 5; attempts++ {
		handle, err := t.ReadLine()
		if err != nil {
			if err == io.EOF {
				return nil, nil // user cancelled
			}
			return nil, fmt.Errorf("read handle: %w", err)
		}

		handle = strings.TrimSpace(strings.ToLower(handle))

		if handle == "" {
			fmt.Fprintln(t, "  handle can't be empty. try again.")
			continue
		}

		if !validHandle.MatchString(handle) {
			fmt.Fprintln(t, "  handle must be 2-20 chars: lowercase letters, numbers, hyphens.")
			fmt.Fprintln(t, "  must start with a letter and end with a letter or number.")
			continue
		}

		exists, err := g.DB.HandleExists(ctx, handle)
		if err != nil {
			return nil, fmt.Errorf("check handle: %w", err)
		}
		if exists {
			fmt.Fprintf(t, "  %q is taken. try another.\n", handle)
			continue
		}

		// Get the public key from session context
		pubKey, _ := session.Context().Value(ctxKeyPublicKey).(gssh.PublicKey)
		fingerprint, _ := session.Context().Value(ctxKeyFingerprint).(string)

		log.Printf("[register] creating user: handle=%q, fingerprint=%s, hasPubKey=%v", handle, fingerprint, pubKey != nil)

		if pubKey == nil || fingerprint == "" {
			return nil, fmt.Errorf("no public key in session context")
		}

		// Create user first
		user, err := g.DB.CreateUser(ctx, handle)
		if err != nil {
			log.Printf("[register] CreateUser failed: %v", err)
			return nil, fmt.Errorf("create user: %w", err)
		}
		log.Printf("[register] user created: id=%d, handle=%s", user.ID, user.Handle)

		// Then add their SSH key
		pubKeyStr := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pubKey)))
		sshKey, err := g.DB.AddSSHKey(ctx, user.ID, pubKeyStr, fingerprint, "")
		if err != nil {
			log.Printf("[register] AddSSHKey failed: %v", err)
			return nil, fmt.Errorf("add ssh key: %w", err)
		}
		log.Printf("[register] ssh key added: id=%d, fingerprint=%s, userID=%d", sshKey.ID, sshKey.Fingerprint, sshKey.UserID)

		// Verify the user can be looked up by fingerprint immediately
		verifyUser, verifyErr := g.DB.UserByFingerprint(ctx, fingerprint)
		if verifyErr != nil {
			log.Printf("[register] WARNING: post-registration verification failed: %v", verifyErr)
		} else {
			log.Printf("[register] verification OK: UserByFingerprint returned user id=%d handle=%s", verifyUser.ID, verifyUser.Handle)
		}

		fmt.Fprintln(t, "")
		fmt.Fprintf(t, "  welcome, %s. you're in.\n", user.Handle)
		fmt.Fprintln(t, "")

		log.Printf("new user registered: %s (fingerprint: %s)", user.Handle, fingerprint)
		return user, nil
	}

	fmt.Fprintln(t, "  too many attempts. try again later.")
	return nil, nil
}
