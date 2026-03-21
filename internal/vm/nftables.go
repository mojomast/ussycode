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
//	    chain input {
//	        type filter hook input priority 0; policy accept;
//	        iifname "ussy0" ct state established,related accept
//	        iifname "ussy0" tcp dport 8083 accept
//	        iifname "ussy0" drop
//	    }
//	    chain prerouting {
//	        type nat hook prerouting priority dstnat; policy accept;
//	        iifname "ussy0" ip daddr 169.254.169.254 tcp dport 80 redirect to :8083
//	    }
//	    chain postrouting {
//	        type nat hook postrouting priority 100; policy accept;
//	        oifname != "ussy0" ip saddr 10.0.0.0/24 masquerade
//	    }
//	    chain forward {
//	        type filter hook forward priority 0; policy drop;
//	        iifname "ussy0" oifname "ussy0" drop           # block inter-VM
//	        oifname "ussy0" ct state established,related accept
//	        iifname "ussy0" ip saddr 10.0.0.0/24 accept    # VM → internet
//	        oifname "ussy0" ip daddr 10.0.0.0/24 accept    # internet → VM (return)
//	    }
//	}
//
// NOTE: nftables sees bridged traffic with iifname/oifname set to the bridge
// interface ("ussy0"), NOT individual tap devices. Per-VM rules therefore match
// on (bridge, guest IP) rather than (tap device, guest IP).
//
// Additionally, when net.bridge.bridge-nf-call-iptables=1 (required by Docker),
// bridged traffic also traverses iptables. We add rules to the DOCKER-USER chain
// to ensure ussy0 traffic is accepted before Docker/ufw rules can drop it.
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
    chain input {
        type filter hook input priority 0; policy accept;
        iifname "%s" ct state established,related accept
        iifname "%s" tcp dport 8083 accept
        iifname "%s" drop
    }
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
        iifname "%s" oifname "%s" drop
        oifname "%s" ct state established,related accept
        iifname "%s" ip saddr %s accept
        oifname "%s" ip daddr %s accept
    }
}
`, n.table,
		bridge, bridge, bridge, // input chain
		bridge,             // prerouting chain
		bridge, subnetCIDR, // postrouting chain
		bridge, bridge, // forward: inter-VM drop
		bridge,             // forward: return traffic
		bridge, subnetCIDR, // forward: VM → internet
		bridge, subnetCIDR, // forward: internet → VM
	)

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

	// When Docker is running with net.bridge.bridge-nf-call-iptables=1,
	// bridged traffic also traverses iptables FORWARD chain. Docker's chains
	// and ufw's DEFAULT_FORWARD_POLICY=DROP will block ussy0 traffic unless
	// we add explicit rules to the DOCKER-USER chain (processed first).
	//
	// We also need iptables NAT masquerade since the nftables postrouting
	// may not see bridged traffic when bridge-nf-call-iptables is enabled.
	if err := n.setupIptablesCompat(ctx, bridge, subnetCIDR); err != nil {
		n.logger.Warn("failed to set up iptables compat rules (Docker may not be running)",
			"error", err,
		)
		// Non-fatal: if Docker isn't installed, iptables compat isn't needed
	}

	return nil
}

// setupIptablesCompat adds iptables rules to coexist with Docker.
//
// When net.bridge.bridge-nf-call-iptables=1 (Docker's default), bridged
// frames traverse iptables in addition to nftables. Docker adds its own
// FORWARD chain rules, and ufw's DEFAULT_FORWARD_POLICY=DROP causes
// bridge traffic to be dropped before our nftables rules see it.
//
// We insert rules into DOCKER-USER (processed first in iptables FORWARD)
// and add NAT masquerade for the VM subnet.
func (n *NftablesManager) setupIptablesCompat(ctx context.Context, bridge, subnetCIDR string) error {
	// Check if DOCKER-USER chain exists (Docker is installed and running)
	if _, err := n.runner.Execute(ctx, "iptables", "-L", "DOCKER-USER", "-n"); err != nil {
		return fmt.Errorf("DOCKER-USER chain not found: %w", err)
	}

	// Remove any existing ussy rules to avoid duplicates on restart
	// (iptables -D fails silently if rule doesn't exist, but we ignore errors)
	for _, args := range [][]string{
		{"-D", "DOCKER-USER", "-i", bridge, "-o", bridge, "-j", "DROP"},
		{"-D", "DOCKER-USER", "-i", bridge, "!", "-o", bridge, "-j", "ACCEPT"},
		{"-D", "DOCKER-USER", "-o", bridge, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	} {
		n.runner.Execute(ctx, "iptables", args...)
	}

	// Insert rules in DOCKER-USER (order matters — inserted in reverse with -I):
	// 1. Drop inter-VM traffic (bridge → bridge)
	// 2. Accept outbound VM traffic (bridge → !bridge)
	// 3. Accept return traffic to VMs (→ bridge, established/related)
	rules := [][]string{
		// Inserted last (bottom of chain): drop inter-VM
		{"-I", "DOCKER-USER", "-i", bridge, "-o", bridge, "-j", "DROP"},
		// Inserted second: accept outbound from bridge
		{"-I", "DOCKER-USER", "-i", bridge, "!", "-o", bridge, "-j", "ACCEPT"},
		// Inserted first (top of chain): accept return traffic
		{"-I", "DOCKER-USER", "-o", bridge, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}

	for _, args := range rules {
		out, err := n.runner.Execute(ctx, "iptables", args...)
		if err != nil {
			return fmt.Errorf("iptables %v: %s: %w", args, string(out), err)
		}
	}

	// Add NAT masquerade for VM subnet in iptables too
	// (nftables masquerade may not apply to bridge-nf traffic)
	// Remove existing rule first to avoid duplicates
	n.runner.Execute(ctx, "iptables", "-t", "nat", "-D", "POSTROUTING",
		"-s", subnetCIDR, "!", "-o", bridge, "-j", "MASQUERADE")
	out, err := n.runner.Execute(ctx, "iptables", "-t", "nat", "-I", "POSTROUTING", "1",
		"-s", subnetCIDR, "!", "-o", bridge, "-j", "MASQUERADE")
	if err != nil {
		return fmt.Errorf("iptables nat masquerade: %s: %w", string(out), err)
	}

	n.logger.Info("iptables compat rules configured for Docker coexistence",
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

	// Also clean up iptables compat rules
	n.cleanupIptablesCompat(ctx, bridge, subnetCIDR)

	return nil
}

// cleanupIptablesCompat removes iptables rules added by setupIptablesCompat.
func (n *NftablesManager) cleanupIptablesCompat(ctx context.Context, bridge, subnetCIDR string) {
	for _, args := range [][]string{
		{"-D", "DOCKER-USER", "-i", bridge, "-o", bridge, "-j", "DROP"},
		{"-D", "DOCKER-USER", "-i", bridge, "!", "-o", bridge, "-j", "ACCEPT"},
		{"-D", "DOCKER-USER", "-o", bridge, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-t", "nat", "-D", "POSTROUTING", "-s", subnetCIDR, "!", "-o", bridge, "-j", "MASQUERADE"},
	} {
		n.runner.Execute(ctx, "iptables", args...)
	}
	n.logger.Info("iptables compat rules cleaned up", "bridge", bridge)
}

// AddVMRules adds per-VM nftables rules to allow traffic from a specific VM.
//
// NOTE: nftables sees bridged traffic as coming from the bridge interface,
// not individual tap devices. Rules therefore match on (bridge, guest IP).
// The base forward chain already allows all traffic from the bridge subnet,
// so per-VM rules are only needed if you want to selectively deny VMs
// (by removing the subnet-wide rule and using per-VM allow rules instead).
//
// Currently this is a no-op since the subnet-wide rules in SetupNAT handle
// forwarding. Per-VM rules are kept as infrastructure for future per-VM
// network policy (e.g., bandwidth limits, port restrictions).
func (n *NftablesManager) AddVMRules(ctx context.Context, vmID, tapDevice, guestIP, bridge string) error {
	n.logger.Info("VM firewall rules active (covered by subnet-wide forward rules)",
		"vm_id", vmID,
		"tap", tapDevice,
		"ip", guestIP,
	)
	// No per-VM nftables rules needed — subnet-wide rules in the forward
	// chain already allow all traffic from/to the VM subnet.
	// Bridge port isolation (set by network.go) prevents inter-VM L2 traffic.
	return nil
}

// RemoveVMRules removes per-VM nftables rules.
// Currently a no-op since we use subnet-wide rules (see AddVMRules).
func (n *NftablesManager) RemoveVMRules(ctx context.Context, vmID, tapDevice, bridge string) error {
	n.logger.Info("VM firewall rules deactivated",
		"vm_id", vmID,
		"tap", tapDevice,
	)
	// No per-VM rules to remove — see AddVMRules comment.
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
