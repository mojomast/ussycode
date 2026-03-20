# Architecture

## System Overview

ussycode is a platform that provides instant, SSH-accessible dev environments powered by Firecracker microVMs. Users connect via SSH, and the platform handles VM lifecycle, networking, storage, and HTTPS routing automatically.

```
                    ┌──────────────────────────────────────┐
                    │           Internet                    │
                    └─────┬──────────┬───────────┬─────────┘
                          │          │           │
                     SSH :2222    HTTPS :443   WireGuard :51820
                          │          │           │
            ┌─────────────┴──────────┴───────────┴─────────────┐
            │                Control Plane Node                 │
            │                                                   │
            │  ┌───────────┐  ┌─────────┐  ┌───────────────┐  │
            │  │ SSH Gateway│  │  Caddy  │  │  WireGuard    │  │
            │  │ (gliderlabs│  │ (reverse│  │  Mesh Manager │  │
            │  │  /ssh)     │  │  proxy) │  │               │  │
            │  └─────┬──────┘  └────┬────┘  └───────┬───────┘  │
            │        │              │               │           │
            │  ┌─────┴──────────────┴───────────────┴────────┐ │
            │  │              ussycode binary                  │ │
            │  │                                               │ │
            │  │  ┌─────────┐ ┌──────┐ ┌────────┐ ┌────────┐ │ │
            │  │  │ VM Mgr  │ │  DB  │ │  API   │ │ Sched  │ │ │
            │  │  │         │ │SQLite│ │Handler │ │        │ │ │
            │  │  └────┬────┘ └──────┘ └────────┘ └────────┘ │ │
            │  └───────┼──────────────────────────────────────┘ │
            │          │                                        │
            │  ┌───────┴────────┐  ┌─────────────────────────┐ │
            │  │  Firecracker   │  │      ZFS Storage         │ │
            │  │  microVMs      │  │  vmpool/vms              │ │
            │  │  (+ jailer)    │  │  vmpool/images           │ │
            │  └───────┬────────┘  │  vmpool/users            │ │
            │          │           └─────────────────────────┘ │
            │  ┌───────┴────────┐                              │
            │  │  nftables      │                              │
            │  │  (per-VM NAT)  │                              │
            │  └────────────────┘                              │
            └──────────────────────────────────────────────────┘
                          │
                    WireGuard mesh
                          │
            ┌─────────────┴─────────────┐
            │      Agent Nodes          │
            │  ┌─────────┐ ┌─────────┐  │
            │  │ Agent 01│ │ Agent 02│  │
            │  └─────────┘ └─────────┘  │
            └───────────────────────────┘
```

## Components

### SSH Gateway (`internal/ssh/`)

The SSH gateway is the primary user interface. It:
- Listens on port 2222 (configurable)
- Authenticates users via SSH public key
- Auto-registers new users on first connection
- Provides an interactive shell with built-in commands
- Routes direct SSH connections (`ssh vmname@host`) to the correct VM

**Key files:**
- `gateway.go` -- SSH server setup, connection handling
- `shell.go` -- Interactive shell, command dispatch
- `commands.go` -- All built-in commands (new, ls, rm, ssh, share, etc.)
- `register.go` -- First-time user registration
- `tutorial.go` -- Interactive tutorial (10 lessons)
- `browser.go` -- Magic link generation for web dashboard
- `arena.go` -- CTF/competition mode
- `community.go` -- Ussyverse community info

### VM Manager (`internal/vm/`)

Manages the lifecycle of Firecracker microVMs:
- Create, start, stop, destroy VMs
- Configure CPU, memory, disk per VM
- Manage VM network interfaces (tap devices)

**Key files:**
- `manager.go` -- High-level VM lifecycle orchestration
- `firecracker.go` -- Firecracker process management, API socket
- `network.go` -- Tap interface creation, bridge attachment
- `image.go` -- Container image -> rootfs conversion
- `nftables.go` -- Per-VM firewall rules and NAT

### Storage (`internal/storage/`)

ZFS-based storage backend providing:
- Copy-on-write VM disk cloning (instant `cp`)
- Per-VM ZFS datasets
- Compression (lz4) and deduplication
- Snapshot support for backups
- Usage tracking and resize

**Interface (`StorageBackend`):**
```go
type StorageBackend interface {
    CloneForVM(ctx context.Context, baseImage, vmName string) error
    DestroyVM(ctx context.Context, vmName string) error
    ResizeVM(ctx context.Context, vmName string, sizeGB int) error
    GetUsage(ctx context.Context, vmName string) (usedBytes, totalBytes int64, err error)
}
```

### Database (`internal/db/`)

SQLite database with goose migrations:
- Users and SSH keys
- VMs and their metadata
- Templates
- Collaboration grants
- Custom domains
- User quotas and trust levels
- API tokens

### Reverse Proxy (`internal/proxy/`)

Manages Caddy routes via its admin API:
- Adds/removes per-VM reverse proxy routes
- Handles wildcard TLS certificates
- Custom domain support
- Authentication proxy for VM web access

### HTTPS API (`internal/api/`)

RESTful API for programmatic access:
- `POST /exec` -- Execute SSH commands via HTTP
- `GET /health` -- Health check
- `GET /version` -- Version info
- Token-based authentication (SSH-signed stateless tokens)
- Per-fingerprint rate limiting

### Scheduler (`internal/scheduler/`)

Distributes VM workloads across the node pool:
- Bin-packing algorithm for resource allocation
- Considers CPU, memory, disk, and network
- Respects node trust levels
- Handles node failures and VM migration

### Mesh Network (`internal/mesh/`)

WireGuard-based mesh connecting all nodes:
- IP allocation for nodes and VMs
- Peer management (add/remove)
- NAT traversal
- Cross-node VM connectivity

### Control Plane (`internal/controlplane/`)

Manages the multi-node cluster:
- Node registration and health monitoring
- Heartbeat processing
- Token-based node authentication

### PKI (`internal/pki/`)

Certificate authority for node authentication:
- Generates CA and node certificates
- Mutual TLS between control plane and agents

## SSH Flow

```
User                    SSH Gateway              VM Manager           Firecracker
  │                         │                        │                     │
  │  SSH connect :2222      │                        │                     │
  │─────────────────────────>                        │                     │
  │                         │                        │                     │
  │  Public key auth        │                        │                     │
  │<─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ >                        │                     │
  │                         │                        │                     │
  │  (new user?)            │                        │                     │
  │  ─ register & assign    │                        │                     │
  │    handle               │                        │                     │
  │                         │                        │                     │
  │  Interactive shell      │                        │                     │
  │<========================>                        │                     │
  │                         │                        │                     │
  │  > new --name=myvm      │                        │                     │
  │─────────────────────────>  CreateVM("myvm")      │                     │
  │                         │───────────────────────>│                     │
  │                         │                        │  Clone rootfs (ZFS) │
  │                         │                        │  Create tap iface   │
  │                         │                        │  Start Firecracker  │
  │                         │                        │────────────────────>│
  │                         │                        │                     │
  │                         │  Add Caddy route       │                     │
  │                         │  (myvm.domain)         │                     │
  │                         │                        │                     │
  │  > ssh myvm             │                        │                     │
  │─────────────────────────>  ProxySSH(myvm)        │                     │
  │                         │───────────────────────>│  SSH to VM IP       │
  │                         │                        │────────────────────>│
  │  <═══ proxied SSH ═══════════════════════════════════════════════════> │
```

## VM Lifecycle

```
                 ┌───────────┐
                 │  Created  │
                 └─────┬─────┘
                       │ start
                       v
    stop ┌─────────────────────────┐ restart
    ┌────│        Running          │────┐
    │    └─────────────────────────┘    │
    v              │                    v
┌───────┐         │ rm            ┌────────┐
│Stopped│         │               │Restart │
└───┬───┘         v               │(stop+  │
    │      ┌──────────────┐       │ start) │
    │      │   Destroyed  │       └────────┘
    │      └──────────────┘
    │             ^
    │  rm         │
    └─────────────┘
```

States:
- **Created**: rootfs cloned, network allocated, not yet booted
- **Running**: Firecracker process active, SSH accessible, HTTPS routed
- **Stopped**: Firecracker process terminated, disk preserved, no resources consumed
- **Destroyed**: All resources (disk, network, Caddy route) cleaned up

## Networking Model

Each VM gets:
1. **A tap interface** (`tap-<vmname>`) bridged to `ussy0`
2. **A private IP** from `10.0.0.0/24` (configurable)
3. **NAT rules** via nftables for internet access
4. **A Caddy route** at `<vmname>-<handle>.<domain>` for HTTPS
5. **SSH proxy** through the gateway

```
Internet
    │
    ├─── :2222 ──> SSH Gateway ──> tap-vm1 (10.0.0.2)
    │                              tap-vm2 (10.0.0.3)
    │
    ├─── :443 ───> Caddy ─────> vm1.user.ussyco.de -> 10.0.0.2:8080
    │                           vm2.user.ussyco.de -> 10.0.0.3:3000
    │
    └─── :51820 ─> WireGuard ──> mesh to other nodes
```

### Cross-Node Networking

For multi-node deployments, VMs on different nodes communicate via the WireGuard mesh:

```
Node A (10.100.0.1)          Node B (10.100.0.2)
  │                              │
  │  VM1 (10.0.0.2) ──WG──── VM3 (10.0.0.4)
  │  VM2 (10.0.0.3)          VM4 (10.0.0.5)
  │                              │
  └──── WireGuard tunnel ────────┘
```

## Storage Model

ussycode uses ZFS for VM storage:

```
vmpool/
├── images/          # Base images (ussyuntu, etc.)
│   ├── ussyuntu     # Ubuntu 24.04 rootfs dataset
│   └── ...
├── vms/             # Running VM datasets
│   ├── user1-myvm   # Cloned from images/ussyuntu
│   ├── user2-dev    # Each VM gets its own dataset
│   └── ...
└── users/           # Per-user data
```

Key properties:
- **Copy-on-write cloning**: `cp` is instant (ZFS clone)
- **Compression**: lz4 compression on all datasets
- **Snapshots**: Used for backups and rollback
- **Quotas**: Per-user storage limits via ZFS quotas

## Multi-Node Architecture

```
┌──────────────────────────────────────────┐
│            Control Plane                  │
│                                           │
│  ┌──────────┐  ┌────────┐  ┌──────────┐ │
│  │ SSH GW   │  │ API    │  │ Scheduler│ │
│  │          │  │        │  │          │ │
│  └──────────┘  └────────┘  └──────────┘ │
│                                           │
│  ┌──────────┐  ┌────────┐  ┌──────────┐ │
│  │ DB       │  │ PKI/CA │  │ Node Mgr │ │
│  │ (SQLite) │  │        │  │          │ │
│  └──────────┘  └────────┘  └──────────┘ │
└─────────────┬────────────────────────────┘
              │
        gRPC + mTLS
              │
    ┌─────────┴─────────┐
    │                   │
┌───┴───┐          ┌────┴──┐
│Agent 1│          │Agent 2│
│       │──WG mesh─│       │
│ VMs:  │          │ VMs:  │
│  vm-a │          │  vm-c │
│  vm-b │          │  vm-d │
└───────┘          └───────┘
```

Communication:
- **Control -> Agent**: gRPC over mTLS (schedule VM, stop VM, health check)
- **Agent -> Control**: Heartbeats with resource usage, VM status
- **Agent <-> Agent**: WireGuard mesh for cross-node VM traffic
- **User -> Control**: SSH (port 2222) and HTTPS (port 443)
