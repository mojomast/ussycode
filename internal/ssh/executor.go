package ssh

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/mojomast/ussycode/internal/db"
	"golang.org/x/term"
)

// APIExecutor executes the SSH command surface programmatically for the HTTPS API.
// It reuses the same command handlers as the interactive SSH shell so the SSH and
// HTTP surfaces stay aligned.
type APIExecutor struct {
	gw *Gateway
}

// NewAPIExecutor creates a programmatic executor backed by the SSH command map.
func NewAPIExecutor(gw *Gateway) *APIExecutor {
	return &APIExecutor{gw: gw}
}

// Execute runs a single command for the given user.
func (e *APIExecutor) Execute(ctx context.Context, user *db.User, command string, args []string) (string, int, error) {
	_ = ctx
	if e == nil || e.gw == nil {
		return "", 1, fmt.Errorf("executor unavailable")
	}
	if strings.TrimSpace(command) == "" {
		return "", 1, fmt.Errorf("empty command")
	}
	if command == "ssh" {
		return "", 1, fmt.Errorf("command %q is interactive-only over SSH", command)
	}

	handler, ok := commands[command]
	if !ok {
		return "", 127, fmt.Errorf("unknown command: %s", command)
	}

	buf := &bytes.Buffer{}
	shell := &Shell{
		gw:   e.gw,
		user: user,
		term: term.NewTerminal(buf, ""),
	}

	if err := handler(shell, args); err != nil {
		return strings.TrimSpace(buf.String()), 1, err
	}

	return strings.TrimSpace(buf.String()), 0, nil
}
