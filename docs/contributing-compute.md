# Contributing Compute to the Ussyverse

## What is the Ussyverse Server Pool?

The Ussyverse is a federated pool of compute nodes that power ussycode environments. Anyone can contribute a server to the pool, expanding the available capacity for all users.

When you contribute a node:
- Your server joins the ussyverse mesh network via WireGuard
- The control plane can schedule VMs onto your node
- You help the community by providing compute resources
- Your node's health and capacity are monitored centrally

## Requirements

### Hardware

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| CPU | 2 cores with VT-x/AMD-V | 8+ cores |
| RAM | 4 GB | 32+ GB |
| Storage | 20 GB free | 100+ GB SSD/NVMe |
| Network | 50 Mbps | 1 Gbps |

### Software

- Linux (Ubuntu 22.04+ recommended)
- KVM support (`/dev/kvm` must exist)
- systemd
- curl, tar

### Network

- Outbound internet access (for control plane communication)
- UDP port 51820 open (WireGuard mesh)
- No inbound ports required (NAT traversal is handled)

## Installation

### One-Line Install

```bash
curl -sL https://get.ussyco.de/agent | sudo bash -s -- \
  --token YOUR_JOIN_TOKEN \
  --control https://cp.ussyco.de
```

### Step by Step

1. **Get a join token** from the control plane admin:

   ```
   # On the control plane (admin only):
   > admin token create --type=agent
   ```

2. **Download and run the installer:**

   ```bash
   wget https://get.ussyco.de/agent -O install-agent.sh
   chmod +x install-agent.sh
   sudo ./install-agent.sh --token TOKEN --control https://cp.ussyco.de
   ```

3. **Start the agent:**

   ```bash
   sudo systemctl enable --now ussyverse-agent
   ```

### Manual Installation

If you prefer not to use the installer:

```bash
# Download the agent binary
curl -sSfL -o /usr/local/bin/ussyverse-agent \
  https://releases.ussyverse.dev/ussyverse-agent-latest-linux-amd64.tar.gz
chmod +x /usr/local/bin/ussyverse-agent

# Create data directory
mkdir -p /var/lib/ussyverse-agent

# Join the cluster
ussyverse-agent join --token TOKEN --control https://cp.ussyco.de

# Create systemd service (see deploy/install-agent.sh for template)
# Start the service
systemctl enable --now ussyverse-agent
```

## Joining the Pool

Once the agent is installed and running:

```bash
ussyverse-agent join --token YOUR_TOKEN --control https://cp.ussyco.de
```

The agent will:
1. Authenticate with the control plane using the join token
2. Generate a WireGuard keypair for mesh networking
3. Register the node's capabilities (CPU, RAM, storage, KVM support)
4. Begin sending periodic heartbeats

## Monitoring Your Node

### Service Status

```bash
systemctl status ussyverse-agent
```

### Logs

```bash
journalctl -u ussyverse-agent -f
```

### Agent Status

```bash
ussyverse-agent status
```

This shows:
- Connection status to control plane
- Number of VMs currently running on your node
- Resource usage (CPU, memory, disk)
- WireGuard mesh connectivity
- Last heartbeat time

### From the Control Plane

Admins can view all nodes in the admin panel:

```
https://cp.ussyco.de/admin/nodes
```

Or via SSH:

```
> admin nodes
```

## Trust Levels

Contributed nodes go through a trust progression:

| Level | Description | VM Capacity |
|-------|-------------|-------------|
| `new` | Just joined, unverified | Limited (2 VMs) |
| `basic` | Verified uptime > 24h | Standard (10 VMs) |
| `trusted` | Manually promoted by admin | Full capacity |
| `admin` | Operator nodes | Unlimited |

Trust levels affect:
- How many VMs can be scheduled on the node
- Whether sensitive workloads are placed there
- Priority in the scheduling queue

Admins promote nodes:

```
> admin set-trust node-01 trusted
```

## Security

- All agent-to-control-plane communication is encrypted (mTLS)
- VM network traffic is isolated per-user via WireGuard + nftables
- The agent runs with minimal privileges (only KVM and network access)
- VMs are jailed using Firecracker's jailer for defense-in-depth
- Node identity is verified via PKI certificates

## Uninstalling

```bash
# Stop and disable the service
systemctl stop ussyverse-agent
systemctl disable ussyverse-agent

# Remove files
rm /etc/systemd/system/ussyverse-agent.service
rm /usr/local/bin/ussyverse-agent
rm -rf /var/lib/ussyverse-agent

systemctl daemon-reload
```

## FAQ

**Q: Will contributing a node give me more resources on ussyco.de?**
A: The community pool benefits all users. Contributors may receive priority scheduling in the future.

**Q: Can I control which VMs run on my node?**
A: The control plane scheduler decides placement. Admins can pin workloads to specific nodes.

**Q: What if my node goes offline?**
A: The control plane detects missing heartbeats and migrates VMs to other nodes. Your node will rejoin automatically when it comes back online.

**Q: Is my data safe?**
A: VM data is stored on ZFS with snapshots. The control plane coordinates data replication for important workloads.
