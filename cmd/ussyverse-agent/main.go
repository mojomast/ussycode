// Package main implements the ussyverse-agent CLI, the node agent that joins
// and participates in the ussyverse server pool.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mojomast/ussycode/internal/agent"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "join":
		if err := runJoin(os.Args[2:]); err != nil {
			slog.Error("join failed", "error", err)
			os.Exit(1)
		}
	case "status":
		if err := runStatus(); err != nil {
			slog.Error("status failed", "error", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("ussyverse-agent %s\n", version)
	case "run":
		if err := runAgent(); err != nil {
			slog.Error("agent exited with error", "error", err)
			os.Exit(1)
		}
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `ussyverse-agent — node agent for the ussyverse server pool

Usage:
  ussyverse-agent <command> [options]

Commands:
  join      Register this node with the control plane
  run       Start the agent (heartbeat + command loop)
  status    Show the current agent status
  version   Print the agent version
  help      Show this help message

Join options:
  --token <TOKEN>    Join token provided by the control plane
  --control <URL>    Control plane URL (e.g. https://control.example.com)
  --data-dir <PATH>  Agent data directory (default: /var/lib/ussyverse-agent)

Run options:
  --data-dir <PATH>  Agent data directory (default: /var/lib/ussyverse-agent)

Examples:
  ussyverse-agent join --token abc123 --control https://cp.example.com
  ussyverse-agent run
  ussyverse-agent status
`)
}

// runJoin handles the "join" subcommand: register with the control plane.
func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	token := fs.String("token", "", "join token provided by the control plane")
	controlURL := fs.String("control", "", "control plane URL")
	dataDir := fs.String("data-dir", agent.DefaultDataDir, "agent data directory")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *token == "" {
		return fmt.Errorf("--token is required")
	}
	if *controlURL == "" {
		return fmt.Errorf("--control is required")
	}

	cfg := agent.Config{
		DataDir:    *dataDir,
		ControlURL: *controlURL,
	}

	a, err := agent.New(cfg)
	if err != nil {
		return fmt.Errorf("initializing agent: %w", err)
	}

	ctx := context.Background()
	if err := a.Join(ctx, *token); err != nil {
		return fmt.Errorf("joining cluster: %w", err)
	}

	slog.Info("successfully joined the cluster", "data_dir", *dataDir)
	return nil
}

// runStatus prints the agent's current state from its local state file.
func runStatus() error {
	state, err := agent.LoadState(agent.DefaultDataDir)
	if err != nil {
		return fmt.Errorf("loading agent state: %w", err)
	}

	fmt.Printf("Node ID:        %s\n", state.NodeID)
	fmt.Printf("Control Plane:  %s\n", state.ControlURL)
	fmt.Printf("Joined At:      %s\n", state.JoinedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("WireGuard CIDR: %s\n", state.WireGuardCIDR)
	return nil
}

// runAgent starts the long-running agent process (heartbeat + command handler).
func runAgent() error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataDir := fs.String("data-dir", agent.DefaultDataDir, "agent data directory")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	state, err := agent.LoadState(*dataDir)
	if err != nil {
		return fmt.Errorf("loading state (have you run 'join' first?): %w", err)
	}

	cfg := agent.Config{
		DataDir:    *dataDir,
		ControlURL: state.ControlURL,
	}

	a, err := agent.New(cfg)
	if err != nil {
		return fmt.Errorf("initializing agent: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting ussyverse-agent", "node_id", state.NodeID, "control", state.ControlURL)

	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("agent stopped: %w", err)
	}
	return nil
}
