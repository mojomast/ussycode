package ssh

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/mojomast/ussycode/internal/db"
	"github.com/mojomast/ussycode/internal/gateway"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Shell is the custom interactive shell presented to authenticated users.
type Shell struct {
	gw      *Gateway
	session gssh.Session
	user    *db.User
	term    *term.Terminal
}

// Run starts the interactive shell REPL.
func (s *Shell) Run() error {
	s.term = term.NewTerminal(s.session, "\033[35mussy>\033[0m ")

	// Print welcome message
	s.printWelcome()

	// REPL loop
	for {
		line, err := s.term.ReadLine()
		if err != nil {
			if err == io.EOF {
				// Ctrl+D
				s.writeln("\ngoodbye.")
				return nil
			}
			return fmt.Errorf("read line: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]
		args := parts[1:]

		if cmd == "exit" || cmd == "quit" {
			s.writeln("goodbye.")
			return nil
		}

		handler, ok := commands[cmd]
		if !ok {
			s.writef("unknown command: %s. type 'help' for commands.\n", cmd)
			continue
		}

		if err := handler(s, args); err != nil {
			s.writef("error: %v\n", err)
		}
	}
}

// execCommand handles non-interactive (single-command) SSH sessions.
// e.g. `ssh ussy.host ls` or `ssh ussy.host new --name=foo`
func (s *Shell) execCommand(cmd []string) {
	if len(cmd) == 0 {
		return
	}

	name := cmd[0]
	args := cmd[1:]

	handler, ok := commands[name]
	if !ok {
		fmt.Fprintf(s.session, "unknown command: %s\n", name)
		return
	}

	// For non-interactive commands, write directly to session
	// (no terminal wrapping needed)
	s.term = term.NewTerminal(s.session, "")

	if err := handler(s, args); err != nil {
		fmt.Fprintf(s.session, "error: %v\n", err)
	}
}

func (s *Shell) printWelcome() {
	running, _ := s.gw.DB.RunningVMCountByUser(context.Background(), s.user.ID)
	total, _ := s.gw.DB.VMCountByUser(context.Background(), s.user.ID)

	s.writeln("")
	s.writeln("  \033[35m~ ussycode ~\033[0m  \033[90mpart of the ussyverse  |  https://ussy.host\033[0m")
	s.writeln("")
	s.writef("  welcome back, \033[1m%s\033[0m.\n", s.user.Handle)

	switch {
	case running > 0:
		s.writef("  you have %d vm%s running (%d total). type 'ls' to see them.\n",
			running, plural(running), total)
	case total > 0:
		s.writef("  you have %d vm%s. type 'ls' to see them.\n", total, plural(total))
	default:
		s.writeln("  you have no vms yet. type 'new' to create one.")
	}

	s.writeln("  type 'help' for commands. type 'community' for links & stats.")
	s.writeln("")
}

func (s *Shell) writeln(msg string) {
	if s.term != nil {
		fmt.Fprintln(s.term, msg)
	}
}

func (s *Shell) writef(format string, args ...interface{}) {
	if s.term != nil {
		fmt.Fprintf(s.term, format, args...)
	}
}

// dispatchCommand parses and executes a command line string through the
// shell's command handlers. Used by the tutorial to run real commands.
func (s *Shell) dispatchCommand(line string) error {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return nil
	}
	cmd := parts[0]
	args := parts[1:]

	handler, ok := commands[cmd]
	if !ok {
		return fmt.Errorf("unknown command: %s", cmd)
	}
	return handler(s, args)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (s *Shell) vmSSHKeys(ctx context.Context) []string {
	var sshKeys []string
	keys, err := s.gw.DB.SSHKeysByUser(ctx, s.user.ID)
	if err == nil {
		for _, k := range keys {
			sshKeys = append(sshKeys, k.PublicKey)
		}
	}
	hostPubKey := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(s.gw.hostSigner.PublicKey())))
	if hostPubKey != "" {
		sshKeys = append(sshKeys, hostPubKey)
	}
	return sshKeys
}

// registerVMMetadata registers a VM with the metadata service so it can
// query its own metadata at 169.254.169.254.
func (s *Shell) registerVMMetadata(ctx context.Context, vmID int64, vmName, image string) {
	if s.gw.Metadata == nil {
		return
	}

	// Re-read the VM to get the assigned IP
	vmRecord, err := s.gw.DB.VMsByUser(ctx, s.user.ID)
	if err != nil {
		return
	}

	sshKeys := s.vmSSHKeys(ctx)

	// Build env vars for the VM, including routussy API key if available
	envVars := s.buildVMEnvVars()

	// Always inject the public domain and VM name so OpenCode's
	// ussycode-web-proxy skill can construct the correct public URL
	// (e.g. https://mild-owl.dev.ussyco.de instead of mild-owl.ussyco.de)
	if s.gw.domain != "" {
		envVars["USSYCODE_PUBLIC_DOMAIN"] = s.gw.domain
	}
	envVars["USSYCODE_VM_NAME"] = vmName

	for _, v := range vmRecord {
		if v.ID == vmID && v.IPAddress.Valid {
			s.gw.Metadata.RegisterVM(v.IPAddress.String, &gateway.VMMetadata{
				InstanceID: fmt.Sprintf("vm-%d", vmID),
				LocalIPv4:  v.IPAddress.String,
				Hostname:   vmName,
				UserID:     s.user.ID,
				UserHandle: s.user.Handle,
				VMName:     vmName,
				Image:      image,
				SSHKeys:    sshKeys,
				Gateway:    "10.0.0.1",
				EnvVars:    envVars,
			})
			return
		}
	}
}

// buildVMEnvVars constructs the environment variables map to inject into a VM.
// If routussy integration is configured and the user's SSH fingerprint is known
// to routussy, this includes OPENCODE_API_KEY and OPENCODE_BASE_URL so that
// pi and OpenCode inside the VM can authenticate against the routussy proxy.
// Both pi (default, auto-launches) and OpenCode (optional, manual) use these
// same env vars for LLM provider authentication.
func (s *Shell) buildVMEnvVars() map[string]string {
	envVars := make(map[string]string)

	// Only inject routussy env vars if the gateway has routussy configured
	if s.gw.RoutussyURL == "" {
		return envVars
	}

	// Get the SSH fingerprint from the session context
	fingerprint, _ := s.session.Context().Value(ctxKeyFingerprint).(string)
	if fingerprint == "" {
		return envVars
	}

	// Verify the user is known to routussy before injecting credentials
	_, err := s.gw.LookupRoutussyUser(fingerprint)
	if err != nil {
		log.Printf("[env] routussy lookup failed for fingerprint=%s: %v (skipping API key injection)", fingerprint, err)
		return envVars
	}

	// Set the fingerprint-based API key and routussy base URL
	// The VM will use these to authenticate OpenCode against the routussy proxy
	envVars["OPENCODE_API_KEY"] = "ussycode-fp:" + fingerprint
	envVars["OPENCODE_BASE_URL"] = strings.TrimRight(s.gw.RoutussyURL, "/") + "/v1"

	log.Printf("[env] injected routussy env vars for fingerprint=%s", fingerprint)
	return envVars
}

// unregisterVMMetadata removes a VM from the metadata service.
func (s *Shell) unregisterVMMetadata(ctx context.Context, vmID int64) {
	if s.gw.Metadata == nil {
		return
	}

	// Read VM to get IP before it's cleared
	vmRecord, err := s.gw.DB.VMsByUser(ctx, s.user.ID)
	if err != nil {
		return
	}

	for _, v := range vmRecord {
		if v.ID == vmID && v.IPAddress.Valid {
			s.gw.Metadata.UnregisterVM(v.IPAddress.String)
			return
		}
	}
}

// addProxyRoute registers a Caddy reverse proxy route for a running VM.
func (s *Shell) addProxyRoute(ctx context.Context, vmID int64, vmName string) {
	if s.gw.Proxy == nil {
		return
	}

	// Find the VM's IP
	vmRecord, err := s.gw.DB.VMsByUser(ctx, s.user.ID)
	if err != nil {
		return
	}

	for _, v := range vmRecord {
		if v.ID == vmID && v.IPAddress.Valid {
			if err := s.gw.Proxy.AddRoute(ctx, vmName, v.IPAddress.String, 8080); err != nil {
				s.gw.Proxy.Logger().Warn("failed to add proxy route",
					"vm", vmName, "error", err)
			}
			return
		}
	}
}

// removeProxyRoute removes the Caddy reverse proxy route for a VM.
func (s *Shell) removeProxyRoute(ctx context.Context, vmName string) {
	if s.gw.Proxy == nil {
		return
	}
	if err := s.gw.Proxy.RemoveRoute(ctx, vmName); err != nil {
		s.gw.Proxy.Logger().Warn("failed to remove proxy route",
			"vm", vmName, "error", err)
	}
}
