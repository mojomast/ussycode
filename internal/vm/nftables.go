package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// FirewallManager abstracts firewall operations for VM networking.
// The default implementation uses nftables; tests can inject a mock.
type FirewallManager interface {
	// SetupNAT creates NAT masquerade rules for a subnet going out through
	// the specified bridge interface.
	SetupNAT(ctx context.Context, bridge, subnetCIDR string) error

	// CleanupNAT removes the NAT rules for a subnet.
	CleanupNAT(ctx context.Context, bridge, subnetCIDR string) error

	// AddVMRules adds per-VM firewall rules (e.g., allowing traffic from
	// the VM's TAP device through the bridge).
	AddVMRules(ctx context.Context, vmID, tapDevice, guestIP, bridge string) error

	// RemoveVMRules removes per-VM firewall rules.
	RemoveVMRules(ctx context.Context, vmID, tapDevice, bridge string) error
}

// NftablesManager implements FirewallManager using nftables.
//
// nftables table layout:
//
//	table inet ussycode {
//	    chain postrouting {
//	        type nat hook postrouting priority 100;
//	        oifname != "ussy0" ip saddr 10.0.0.0/24 masquerade
//	    }
//	    chain forward {
//	        type filter hook forward priority 0; policy drop;
//	        iifname "ussy0" accept
//	        oifname "ussy0" ct state established,related accept
//	        # per-VM rules added dynamically
//	    }
//	}
type NftablesManager struct {
	runner CommandExecutor
	table  string // nftables table name (default: "ussycode")
	logger *slog.Logger
}

// CommandExecutor abstracts command execution for testability.
type CommandExecutor interface {
	// Execute runs a command and returns combined output and error.
	Execute(ctx context.Context, name string, args ...string) ([]byte, error)
}

// defaultExecutor runs real system commands.
type defaultExecutor struct{}

func (d *defaultExecutor) Execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCmdContext(ctx, name, args...)
}

// NewNftablesManager creates a new nftables-based firewall manager.
// If runner is nil, a default executor using real system commands is used.
func NewNftablesManager(runner CommandExecutor, logger *slog.Logger) *NftablesManager {
	if runner == nil {
		runner = &defaultExecutor{}
	}
	return &NftablesManager{
		runner: runner,
		table:  "ussycode",
		logger: logger,
	}
}

// SetupNAT creates the nftables table with NAT masquerade and forwarding rules.
// This replaces the iptables NAT setup in the old network.go.
//
// The nftables ruleset:
//  1. Creates table "ussycode" in the inet family
//  2. Adds a postrouting NAT chain with masquerade for VM subnet traffic
//  3. Adds a forward chain that allows traffic from/to the bridge
func (n *NftablesManager) SetupNAT(ctx context.Context, bridge, subnetCIDR string) error {
	n.logger.Info("setting up nftables NAT",
		"table", n.table,
		"bridge", bridge,
		"subnet", subnetCIDR,
	)

	// Build the nftables ruleset as a single atomic script.
	// Using nft -f with a script ensures atomicity — either all rules
	// are applied or none are.
	//
	// The prerouting chain redirects VM traffic destined for the metadata
	// IP (169.254.169.254:80) to the internal metadata service port (8083).
	// This is needed because nginx binds 0.0.0.0:80 on the host, stealing
	// port 80 on the metadata IP before our metadata server can grab it.
	ruleset := fmt.Sprintf(`
table inet %s {
    chain prerouting {
        type nat hook prerouting priority dstnat; policy accept;
        iifname "%s" ip daddr 169.254.169.254 tcp dport 80 redirect to :8083
    }
    chain postrouting {
        type nat hook postrouting priority 100; policy accept;
        oifname != "%s" ip saddr %s masquerade
    }
    chain forward {
        type filter hook forward priority 0; policy drop;
        iifname "%s" accept
        oifname "%s" ct state established,related accept
    }
}
`, n.table, bridge, bridge, subnetCIDR, bridge, bridge)

	// First, try to delete any existing table (ignore errors if it doesn't exist)
	n.runner.Execute(ctx, "nft", "delete", "table", "inet", n.table)

	// Apply the ruleset atomically
	out, err := n.runner.Execute(ctx, "nft", "-f", "-", ruleset)
	if err != nil {
		return fmt.Errorf("apply nftables ruleset: %s: %w", string(out), err)
	}

	n.logger.Info("nftables NAT configured",
		"table", n.table,
		"bridge", bridge,
		"subnet", subnetCIDR,
	)
	return nil
}

// CleanupNAT removes the entire nftables table.
func (n *NftablesManager) CleanupNAT(ctx context.Context, bridge, subnetCIDR string) error {
	n.logger.Info("cleaning up nftables NAT", "table", n.table)

	out, err := n.runner.Execute(ctx, "nft", "delete", "table", "inet", n.table)
	if err != nil {
		// If table doesn't exist, that's fine
		if strings.Contains(string(out), "No such file") ||
			strings.Contains(string(out), "does not exist") {
			n.logger.Debug("nftables table already gone", "table", n.table)
			return nil
		}
		return fmt.Errorf("delete nftables table: %s: %w", string(out), err)
	}
	return nil
}

// AddVMRules adds per-VM nftables rules to allow traffic from a specific
// TAP device. This enables fine-grained per-VM firewall control.
func (n *NftablesManager) AddVMRules(ctx context.Context, vmID, tapDevice, guestIP, bridge string) error {
	n.logger.Info("adding nftables VM rules",
		"vm_id", vmID,
		"tap", tapDevice,
		"ip", guestIP,
	)

	// Add a rule to allow forwarding from this VM's TAP device
	// The comment field allows us to identify and remove the rule later
	comment := fmt.Sprintf("vm-%s", vmID)

	out, err := n.runner.Execute(ctx, "nft", "add", "rule", "inet", n.table,
		"forward", "iifname", tapDevice, "ip", "saddr", guestIP,
		"accept", "comment", fmt.Sprintf(`"%s"`, comment))
	if err != nil {
		return fmt.Errorf("add VM forward rule: %s: %w", string(out), err)
	}

	// Also allow return traffic to the VM
	out, err = n.runner.Execute(ctx, "nft", "add", "rule", "inet", n.table,
		"forward", "oifname", tapDevice, "ip", "daddr", guestIP,
		"accept", "comment", fmt.Sprintf(`"%s-return"`, comment))
	if err != nil {
		return fmt.Errorf("add VM return rule: %s: %w", string(out), err)
	}

	n.logger.Info("nftables VM rules added", "vm_id", vmID, "tap", tapDevice)
	return nil
}

// RemoveVMRules removes per-VM nftables rules by flushing rules that
// match the VM's comment tag.
func (n *NftablesManager) RemoveVMRules(ctx context.Context, vmID, tapDevice, bridge string) error {
	n.logger.Info("removing nftables VM rules",
		"vm_id", vmID,
		"tap", tapDevice,
	)

	// List current rules to find handles matching this VM
	out, err := n.runner.Execute(ctx, "nft", "--handle", "list", "chain", "inet", n.table, "forward")
	if err != nil {
		// Table might not exist yet, that's OK
		if strings.Contains(string(out), "No such file") ||
			strings.Contains(string(out), "does not exist") {
			return nil
		}
		return fmt.Errorf("list forward chain: %s: %w", string(out), err)
	}

	// Parse output to find rule handles matching our VM's comment
	comment := fmt.Sprintf("vm-%s", vmID)
	handles := parseNftHandles(string(out), comment)

	for _, handle := range handles {
		delOut, err := n.runner.Execute(ctx, "nft", "delete", "rule", "inet", n.table,
			"forward", "handle", handle)
		if err != nil {
			n.logger.Warn("failed to delete nftables rule",
				"handle", handle,
				"vm_id", vmID,
				"error", err,
				"output", string(delOut),
			)
		}
	}

	n.logger.Info("nftables VM rules removed", "vm_id", vmID, "rules_deleted", len(handles))
	return nil
}

// parseNftHandles extracts rule handles from nft --handle output that
// contain the given comment string.
//
// Example nft output line:
//
//	iifname "tap-1234" ip saddr 10.0.0.2 accept comment "vm-1234" # handle 5
func parseNftHandles(output, comment string) []string {
	var handles []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, comment) {
			continue
		}
		// Find "# handle <N>" at the end of the line
		idx := strings.LastIndex(line, "# handle ")
		if idx < 0 {
			continue
		}
		handleStr := strings.TrimSpace(line[idx+len("# handle "):])
		if handleStr != "" {
			handles = append(handles, handleStr)
		}
	}
	return handles
}

// runCmdContext runs a command with context support.
// For commands that take stdin (like nft -f -), the last arg
// after the sentinel "-" is passed as stdin.
func runCmdContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	// Check if the last arg is meant to be stdin (nft -f - <ruleset>)
	// Convention: if args has ["-f", "-", "<content>"], pipe content to stdin
	var stdinData string
	if len(args) >= 3 && args[len(args)-3] == "-f" && args[len(args)-2] == "-" {
		stdinData = args[len(args)-1]
		args = args[:len(args)-1] // remove the ruleset from args
	}

	cmd := exec.CommandContext(ctx, name, args...)
	if stdinData != "" {
		cmd.Stdin = strings.NewReader(stdinData)
	}
	return cmd.CombinedOutput()
}
