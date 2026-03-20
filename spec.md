# USSYCODE -- Product Specification v2.0

## Self-Hosted Dev Environment Platform for the Ussyverse

**Project:** ussycode
**Repository:** github.com/mojomast/ussycode
**Domain:** ussyco.de
**License:** MIT
**Created by:** Kyle Durepos ([@mojomast](https://github.com/mojomast)) & shuv (co-creator)
**Part of:** [The Ussyverse](https://ussy.host) -- an ever-expanding open-source ecosystem

---

## TABLE OF CONTENTS

1. [What Is Ussycode](#1-what-is-ussycode)
2. [Why This Exists](#2-why-this-exists)
3. [Architecture Overview](#3-architecture-overview)
4. [Tech Stack & Dependencies](#4-tech-stack--dependencies)
5. [Existing Codebase Inventory](#5-existing-codebase-inventory)
6. [Features & UX Design](#6-features--ux-design)
7. [The Ussyverse Server Pool](#7-the-ussyverse-server-pool)
8. [Security Model](#8-security-model)
9. [Base Image: ussyuntu](#9-base-image-ussyuntu)
10. [Parallel Development Tracks](#10-parallel-development-tracks)
11. [Track Specifications](#11-track-specifications)
12. [Circular Development Protocol](#12-circular-development-protocol)
13. [Known Blockers & Mitigations](#13-known-blockers--mitigations)
14. [Testing Strategy](#14-testing-strategy)
15. [Deployment](#15-deployment)
16. [Success Metrics](#16-success-metrics)

---

## 1. WHAT IS USSYCODE

ussycode is an open-source, self-hosted platform that gives anyone instant SSH-accessible dev environments with persistent disks, automatic HTTPS endpoints, and built-in AI agent support. It is the infrastructure backbone for the Ussyverse -- providing free compute to community members who want to learn development, run agents, and build backends.

The magic moment: anyone runs `ssh ussyco.de` and gets a dev environment in seconds. No signup forms. No credit cards. SSH keys are identity.

The twist: anyone can contribute their own server to the **Ussyverse Server Pool** by deploying a single agent binary. The pool grows organically as community members donate compute.

---

## 2. WHY THIS EXISTS

- New developers in the Ussyverse need to learn CLI, SSH, git, and backend dev
- Setting up local dev environments is a wall for beginners
- The Ussyverse needs its own compute layer for agents, BattleBussy arenas, and dev tooling
- Existing solutions are either proprietary, expensive ($20/mo+), or not self-hostable
- We want to provide free community infrastructure funded by donated iron, not per-user billing
- shuv identified the UX gap: the experience of instant VM provisioning via SSH is unmatched and should be open source

---

## 3. ARCHITECTURE OVERVIEW

### 3.1 System Diagram

```
                         INTERNET
                            |
              +-------------+-------------+
              |                           |
     SSH (port 22/2222)          HTTPS (port 443)
              |                           |
    +---------+---------+       +---------+---------+
    |   SSH Gateway     |       |   Caddy Reverse   |
    |   (Go, custom)    |       |   Proxy (auto TLS)|
    +---------+---------+       +---------+---------+
              |                           |
    +---------+---------+       +---------+---------+
    |   Control Plane   |-------|   Auth Proxy      |
    |   (commands, db)  |       |   (token verify)  |
    +---------+---------+       +---------+---------+
              |                           |
    +---------+---------------------------+---------+
    |              VM Manager                       |
    |  (Firecracker microVMs, lifecycle, images)    |
    +---------+---------+---------+---------+-------+
              |         |         |         |
         +----+---+ +---+----+ +-+------+ +-+--------+
         |microVM | |microVM | |microVM | |microVM   |
         |tap0    | |tap1    | |tap2    | |tap3      |
         |10.0.0.2| |10.0.0.6| |10.0.0.| |10.0.0.  |
         +----+---+ +---+----+ +-+------+ +-+--------+
              |         |         |         |
         +----+---+ +---+----+ +-+------+ +-+--------+
         |ZFS zvol| |ZFS zvol| |ZFS zvol| |ZFS zvol  |
         |persist | |persist | |persist | |persist   |
         +--------+ +--------+ +--------+ +----------+

    +-----------------------------------------------+
    |   Metadata Service (169.254.169.254:80)       |
    |   LLM Gateway | Email | VM Info | SSH Keys    |
    +-----------------------------------------------+
```

### 3.2 Multi-Node Architecture (Ussyverse Server Pool)

```
    +--------------------------------------------------+
    |              CONTROL PLANE (ussyco.de)            |
    |                                                   |
    |  SSH Gateway | Scheduler | WG Coordinator | DB   |
    |  Caddy Proxy | DERP Relay | Admin Panel          |
    +-----+--------+--------+--------+-----------------+
          |  gRPC/mTLS      |  WireGuard UDP
          |                  |
   +------+------+   +------+------+   +------+------+
   | Agent Node A|   | Agent Node B|   | Agent Node C|
   | (bare metal)|   | (VPS w/KVM) |   | (homelab)   |
   | VMs: 0-15   |   | VMs: 0-8    |   | VMs: 0-4    |
   | WG: 100.64. |   | WG: 100.64. |   | WG: 100.64. |
   +-------------+   +-------------+   +-------------+
```

---

## 4. TECH STACK & DEPENDENCIES

### 4.1 Core Stack

| Component | Technology | Version | Why |
|---|---|---|---|
| **Language** | Go | 1.25+ | Single binary, fast, ussyverse standard |
| **Database** | SQLite (WAL mode) | via modernc.org/sqlite | Zero deps, embedded, sufficient for thousands of VMs |
| **Migrations** | goose | embedded SQL | Already integrated |
| **VMM** | Firecracker | latest | microVM boot in <2s, minimal attack surface, Go SDK available |
| **Reverse Proxy** | Caddy | v2 | Auto TLS, wildcard certs, runtime API for route management |
| **SSH Server** | gliderlabs/ssh | latest | Already integrated, handles pubkey auth + PTY + session management |
| **OCI Images** | go-containerregistry | latest | Pull Docker/OCI images, extract rootfs layers |
| **Storage** | ZFS (zvols) | kernel module | Instant COW clones, compression, quotas, zfs send/receive backups |
| **Networking** | TAP + nftables | kernel | Per-VM isolation, NAT, metadata service interception |
| **Mesh Network** | WireGuard (wireguard-go) | embedded | Encrypted overlay for multi-node, NAT traversal |
| **Node Comms** | gRPC + mTLS | latest | Agent-to-control-plane, bidirectional streaming |
| **NAT Traversal** | STUN + DERP relay | tailscale.com/derp | Handles 100% of NAT scenarios |

### 4.2 Go Module Dependencies

```go
// go.mod
module github.com/mojomast/ussycode

go 1.25

require (
    // SSH
    github.com/gliderlabs/ssh              latest
    golang.org/x/crypto                    latest
    golang.org/x/term                      latest

    // Database
    modernc.org/sqlite                     latest
    github.com/pressly/goose/v3            latest

    // VM Management
    github.com/firecracker-microvm/firecracker-go-sdk  latest
    github.com/google/go-containerregistry              latest

    // Networking
    golang.zx2c4.com/wireguard             latest
    golang.zx2c4.com/wireguard/wgctrl      latest
    tailscale.com                           latest  // magicsock + DERP

    // Control Plane
    google.golang.org/grpc                 latest
    google.golang.org/protobuf             latest

    // Observability
    github.com/prometheus/client_golang    latest
    log/slog                               // stdlib
)
```

### 4.3 Host Requirements

**Control Plane Node:**
- Linux x86_64 with KVM support (`/dev/kvm`)
- 8+ cores, 32GB+ RAM, 500GB+ NVMe
- Public IP with wildcard DNS (`*.ussyco.de`)
- ZFS kernel module installed
- Caddy v2 installed or managed via ussycode

**Agent Node (community-contributed):**
- Linux x86_64 with KVM support (`/dev/kvm`)
- Minimum 2 cores, 4GB RAM, 20GB disk
- Outbound internet access (gRPC + WireGuard UDP)
- Root access (for Firecracker jailer, network namespaces)
- No public IP required (NAT traversal handles it)

### 4.4 Kernel & Guest Requirements

- **Host kernel:** 5.10+ (Firecracker requirement, KVM support)
- **Guest kernel:** Pre-built vmlinux from Firecracker project (5.10 LTS recommended)
  - Source: `https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md`
  - Config: must enable virtio-net, virtio-blk, ext4, networking stack
  - ~8MB compressed, embedded in agent binary or downloaded on first run
- **Rootfs:** ext4 filesystem built from OCI container image layers
  - Created via `mkfs.ext4 -d <extracted_layers_dir> rootfs.ext4`
  - Guest network configured via kernel `ip=` boot arg (no iproute2 needed in guest)

---

## 5. EXISTING CODEBASE INVENTORY

Phases 1-2 of the original spec are substantially complete. The following code exists, compiles, and passes tests.

### 5.1 What's Built & Working

> **Note:** This inventory was updated after Tracks A-G completion. See PROGRESS-*.md files for
> detailed implementation notes. 62 Go files, 20,442 lines, 80+ tests across 12 suites.

| Package | Key Files | Status | What It Does |
|---|---|---|---|
| `cmd/ussycode` | `main.go` | **Working** | Entry point, wires DB + SSH + proxy + API + admin + metadata + email + LLM, graceful shutdown. Config package fully integrated. |
| `cmd/ussyverse-agent` | `main.go` | **Working** | Agent binary with join/run/status/version subcommands |
| `internal/db` | `db.go`, `models.go`, `queries.go` + 4 test files | **Working + Tested** | SQLite WAL, 9 migrations (001-009), 40+ queries, full CRUD, quota enforcement |
| `internal/auth` | `token.go`, `token_test.go`, `middleware.go` | **Working + Tested** | SSH key-based stateless tokens, HTTP Bearer middleware, handle generation |
| `internal/ssh` | `gateway.go`, `shell.go`, `commands.go`, `browser.go`, `tutorial.go`, `arena.go`, `community.go`, `register.go` | **Working + Tested** | SSH server, 17+ commands, tutorial (10 lessons), arena, community |
| `internal/vm` | `manager.go`, `firecracker.go`, `network.go`, `image.go`, `nftables.go` | **Working + Tested** | VM lifecycle, Firecracker SDK, TAP/bridge networking, OCI pull + rootfs, nftables firewall |
| `internal/proxy` | `caddy.go`, `auth.go` | **Implemented (no tests)** | Caddy admin API integration, forward-auth proxy with identity headers |
| `internal/gateway` | `metadata.go`, `llm.go`, `email.go`, `email_send.go`, `crypto.go` | **Working + Tested** | Metadata service, LLM gateway (5 providers, BYOK, rate limiting), inbound SMTP + Maildir, outbound email |
| `internal/api` | `handler.go`, `handler_test.go`, `ratelimit.go` | **Working + Tested** | POST /exec, GET /health, GET /version; usy0/usy1 tokens; rate limiting. **Note: executor/KeyResolver/Config nil in main.go** |
| `internal/admin` | `admin.go`, `admin_test.go`, `embed.go` | **Working + Tested** | Web panel with login, dashboard, users, VMs, nodes; magic link auth; 27 tests |
| `internal/config` | `config.go`, `config_test.go` | **Working** | 30+ config fields, env var + CLI flag precedence, validation. Wired into main.go. |
| `internal/storage` | `zfs.go`, `zfs_test.go`, `zfs_bench_test.go` | **Working + Tested** | StorageBackend interface, ZFSBackend with clone/destroy/resize/usage. 14 tests. **Not yet used by VM manager.** |
| `internal/pki` | `ca.go`, `ca_test.go` | **Working + Tested** | Ed25519 CA chain, cert issuance, join tokens, verification. 7 tests. |
| `internal/scheduler` | `scheduler.go`, `scheduler_test.go` | **Working + Tested** | Two-phase filter+score placement algorithm. 10 tests. |
| `internal/controlplane` | `nodemanager.go` | **Implemented (no tests)** | Node tracking, health states, timeout checker, command queue |
| `internal/agent` | `agent.go`, `heartbeat.go` | **Scaffolded (no tests)** | Agent join/run structure. gRPC transport not yet implemented. Heartbeat loop defined but not invoked. |
| `internal/mesh` | `wireguard.go`, `allocator.go` | **Stub + Working** | WireGuard: stub (in-memory). Subnet allocator: working (/24 from 100.64.0.0/10). |
| `images/ussyuntu` | `Dockerfile`, `init-ussycode.sh`, configs | **Working** | Ubuntu 24.04 base image with Go 1.24, Python 3, Node 22, systemd |
| `deploy/` | Ansible roles, installers | **Working** | 6 Ansible roles, agent/control-plane installers |

### 5.2 Remaining Work

> Most items from the original "NOT BUILT" list are now complete. See PLAN-exe-dev-parity-roadmap.md
> for the current parity roadmap.

| Component | Status | Reference |
|---|---|---|
| API runtime wiring (executor/KeyResolver/Config nil) | **BLOCKED** | Phase 0 in parity plan |
| Browser auth URL handler (magic-link 404) | **BLOCKED** | Phase 0 in parity plan |
| `doc` command | **NOT STARTED** | Track C pending |
| `new --env/--command/--prompt` support | **NOT STARTED** | Phase 1 in parity plan |
| Share link redemption in proxy | **NOT STARTED** | Phase 2 in parity plan |
| Telemetry/observability | **NOT STARTED** | Phase 0 in parity plan |
| gRPC transport for agent join | **NOT STARTED** | Phase 7 in parity plan |
| Production WireGuard (tailscale/DERP) | **NOT STARTED** | Phase 7 in parity plan |
| VM manager ↔ StorageBackend integration | **NOT STARTED** | Phase 7 in parity plan |
| Team model | **NOT STARTED** | Phase 6 in parity plan |

### 5.3 Rename Status: ✅ COMPLETE

The codebase rename from `exedevussy` to `ussycode` was completed in Track A.1:
- Module: `github.com/mojomast/ussycode` ✅
- Binary: `ussycode` ✅
- Directory: `cmd/ussycode/` ✅
- Internal references: `ussycode` throughout ✅
- Base image user: `ussycode` ✅
- Config env prefix: `USSYCODE_` ✅

---

## 6. FEATURES & UX DESIGN

### 6.1 First Contact: `ssh ussyco.de`

**New user flow:**
```
$ ssh ussyco.de

  ╔══════════════════════════════════════════╗
  ║         welcome to the ussyverse         ║
  ║                                          ║
  ║   looks like you're new here.            ║
  ║                                          ║
  ║   your ssh key is your identity.         ║
  ║   no passwords. no email. just keys.     ║
  ║                                          ║
  ║   pick a handle:                         ║
  ╚══════════════════════════════════════════╝

  > handle: _
```

**Returning user flow:**
```
$ ssh ussyco.de

  welcome back, shuv.
  you have 3 vms running. type 'ls' to see them.
  type 'help' for commands. type 'tutorial' if you're new.

  ussy>
```

### 6.2 Full Command Set

```
ussy> help

=== USSYCODE COMMANDS ===

BASICS
  help              show this help
  tutorial          guided walkthrough for beginners
  doc [slug]        browse documentation

VM LIFECYCLE
  new               create a new dev environment
  ls [-la]          list your environments
  rm <vm>           delete an environment
  restart <vm>      restart an environment
  start <vm>        start a stopped environment
  stop <vm>         stop a running environment
  cp <vm> [name]    clone an environment
  rename <old> <new>  rename an environment
  tag <vm> <tag>    tag an environment

ACCESS
  ssh <vm>          connect to an environment
  share ...         share access with others
  browser           get a magic link for web dashboard

IDENTITY
  whoami            show your info
  ssh-key ...       manage your SSH keys

USSYVERSE
  projects          browse ussyverse project templates
  arena             connect to BattleBussy

Every command supports --json for automation.
```

### 6.3 Guided Tutorial Mode

The `tutorial` command is an interactive, progressive walkthrough teaching CLI from zero:

**Lesson 1:** Create your first VM (`new --name=mybox`)
**Lesson 2:** Connect to it (`ssh mybox`)
**Lesson 3:** Linux basics (ls, cd, cat, nano)
**Lesson 4:** Run a web server (`python3 -m http.server 8080`)
**Lesson 5:** Access via HTTPS (`https://mybox.ussyco.de`)
**Lesson 6:** Install packages (`apt install`)
**Lesson 7:** Use git (clone, commit, push)
**Lesson 8:** Run an AI agent
**Lesson 9:** Share your work (`share set-public mybox`)
**Lesson 10:** Clean up (`rm mybox`)

### 6.4 HTTPS Proxy

Every VM gets `https://vmname.ussyco.de/` with automatic TLS.

- Private by default (authenticated users only)
- `share set-public <vm>` for public access
- Port auto-detection from container EXPOSE directives
- Ports 3000-9999 transparently forwarded (`vmname.ussyco.de:3456`)
- Auth headers injected: `X-Ussy-UserID`, `X-Ussy-Handle`, `X-Ussy-Email`
- WebSocket support for live apps, terminal sharing, BattleBussy feeds

### 6.5 Sharing & Collaboration

- Share by email: `share add <vm> user@example.com`
- Share by link: `share add-link <vm>` (generates token URL)
- Share with team: `share add <vm> team`
- Public access: `share set-public <vm>`
- QR code generation: `--qr` flag on share commands

### 6.6 Custom Domains

```
ussy> share cname myproject app.example.com
```
Point a CNAME to `myproject.ussyco.de`, Caddy auto-issues TLS.

### 6.7 Project Templates

```
ussy> projects

USSYVERSE TEMPLATES
  geoffrussy         AI dev orchestrator (Go)
  battlebussy-agent  autonomous CTF agent
  openclawssy        security-first agent runtime
  ragussy            self-hosted RAG chatbot
  swarmussy          multi-agent orchestration
  blank              empty ubuntu environment

usage: new --template=<name>
```

### 6.8 Email

- **Receive:** `*@vmname.ussyco.de` delivers to `~/Maildir/new/`
- **Send:** `curl -X POST http://169.254.169.254/gateway/email/send` (owner-only)

### 6.9 LLM Gateway

Available inside every VM at `http://169.254.169.254/gateway/llm/`:

| Backend | Endpoint | Notes |
|---|---|---|
| Self-hosted Ollama | `/gateway/llm/ollama` | Operator deploys Ollama on GPU node |
| Self-hosted vLLM | `/gateway/llm/vllm` | Alternative self-hosted backend |
| Anthropic (BYOK) | `/gateway/llm/anthropic` | User provides API key |
| OpenAI (BYOK) | `/gateway/llm/openai` | User provides API key |
| Fireworks (BYOK) | `/gateway/llm/fireworks` | User provides API key |

### 6.10 HTTPS API

```
POST https://ussyco.de/exec
Authorization: Bearer <token>
Content-Type: text/plain

ls --json
```

Tokens are self-signed with SSH keys. No server-side token database needed.

---

## 7. THE USSYVERSE SERVER POOL

The Ussyverse Server Pool is the distributed compute network powering ussycode. Anyone can contribute their server.

### 7.1 How It Works

1. Operator generates a join token: `ussycode generate-join-token --ttl 24h`
2. Contributor installs the agent: `curl -sL https://get.ussyco.de/agent | sudo sh`
3. Contributor joins: `ussyverse-agent join --token <TOKEN> --control https://ussyco.de`
4. Agent registers via gRPC, gets mTLS certificate, joins WireGuard mesh
5. Agent reports resources (CPU, RAM, disk) via heartbeat every 10s
6. Control plane schedules VMs onto the node based on available resources

### 7.2 Agent Binary

The `ussyverse-agent` is a single Go binary that runs on any Linux x86_64 with KVM:

```
ussyverse-agent contains:
  - Agent daemon (gRPC client, heartbeat, VM lifecycle)
  - Embedded WireGuard (wireguard-go)
  - Firecracker binary (embedded or auto-downloaded)
  - Default Linux kernel (vmlinux, embedded or auto-downloaded)
  - Network manager (TAP, nftables)
  - Storage manager (ZFS operations)
  - Metadata service (per-VM HTTP on 169.254.169.254)
```

### 7.3 Node Trust Levels

| Level | Name | Requirements | Capabilities |
|---|---|---|---|
| 0 | Community | Join token + GitHub account | Non-sensitive workloads only |
| 1 | Verified | Identity verified, consistent uptime | Standard workloads |
| 2 | Attested | Hardware attestation (TPM/SEV) | Sensitive workloads |
| 3 | Operated | Run by ussycode team / partners | All workloads, control plane eligible |

### 7.4 Resource Limits (Per User)

| Trust Level | VMs | CPU | RAM | Disk | LLM Tokens |
|---|---|---|---|---|---|
| newbie | 3 | 1 vCPU | 2 GB | 5 GB | Operator-set |
| citizen | 10 | 4 vCPU | 8 GB | 25 GB | Operator-set |
| operator | 25 | 8 vCPU | 16 GB | 100 GB | Operator-set |
| admin | unlimited | unlimited | unlimited | unlimited | unlimited |

### 7.5 VM Scheduling

Scoring-based placement (Nomad-style bin packing):

```
Filter (hard constraints):
  - Node.Status == Ready
  - Node.AvailableRAM >= vm.RequestedRAM
  - Node.AvailableCPU >= vm.RequestedCPU
  - Node.TrustLevel >= vm.RequiredTrust

Score (soft preferences, weighted):
  0.4 - BinPacking: prefer partially-full nodes (maximize utilization)
  0.2 - Spread: fewer VMs from same user on same node (fault isolation)
  0.2 - Locality: prefer nodes near user's proxy (lower latency)
  0.1 - Freshness: prefer recently-heartbeated nodes
  0.1 - TrustScore: prefer higher-trust nodes
```

### 7.6 Networking (WireGuard Mesh)

- Each agent node gets a `/24` from the overlay network (e.g., `100.64.x.0/24`)
- VMs on that node get IPs within the `/24`
- All inter-node traffic encrypted via WireGuard
- NAT traversal: STUN + DERP relay (using tailscale.com/derp, MIT licensed)
- Control plane acts as WireGuard coordinator (Headscale-inspired)

### 7.7 Node Lifecycle

```
States: Joining -> Ready -> Draining -> Offline -> Removed

Heartbeat: every 10s via gRPC bidirectional stream
  - CPU/RAM/disk usage, running VMs, network throughput, agent version

Timeouts:
  - No heartbeat for 30s -> node marked "Unknown"
  - No heartbeat for 5min -> node marked "Offline"
  - Offline for 1hr -> VMs rescheduled to other nodes

Graceful shutdown:
  - Agent sends DrainRequest
  - Control plane migrates VMs off before deregistering
```

---

## 8. SECURITY MODEL

### 8.1 VM Isolation
- Firecracker microVMs provide hardware-level KVM isolation
- Each VM has its own kernel, filesystem, network namespace
- No public IPs; all traffic routes through authenticated proxy
- VMs cannot see or communicate with each other (isolated TAP devices, no bridge)
- Firecracker jailer runs VMs as unprivileged user

### 8.2 Authentication
- SSH keys are identity (no passwords, no OAuth)
- HTTPS proxy injects auth headers (apps don't need their own auth)
- API tokens self-signed with SSH keys (stateless server-side verification)
- Agent nodes authenticate via mTLS (short-lived certs, 24h, auto-renewed)

### 8.3 Agent Node Security
- Node operators cannot read VM memory on TrustLevel 2+ (AMD SEV/Intel TDX)
- VM disks encrypted at rest with keys held by control plane
- Inter-node traffic encrypted via WireGuard (node operator sees only encrypted packets)
- Compromised nodes are revoked instantly (cert not renewed, WireGuard key removed)

### 8.4 Network Security
- nftables `policy drop` on forward chain (default deny)
- Inter-VM traffic explicitly blocked: `iifname "fc-tap*" oifname "fc-tap*" drop`
- Metadata service (169.254.169.254) uses source IP to identify requesting VM
- Conntrack for stateful packet inspection

---

## 9. BASE IMAGE: USSYUNTU

Ubuntu 24.04 base with systemd, configured for ussycode:

**Pre-installed:**
- Core: git, curl, wget, jq, tmux, vim, nano, htop, tree
- Languages: Go 1.24+, Python 3, Node.js 22, TypeScript
- Tools: Docker (optional), gh (GitHub CLI), sqlite3
- Init: systemd oneshot fetches SSH keys from metadata, sets hostname, writes env

**Dockerfile and init scripts** are in `images/ussyuntu/`, published open-source.

---

## 10. PARALLEL DEVELOPMENT TRACKS

> **Status update:** Tracks A, B, E, F, G are COMPLETE. Track C is 1/5 done (tutorial only).
> Track D was merged into Track E. See PROGRESS-*.md files for implementation details.
> The next phase of work is defined in PLAN-exe-dev-parity-roadmap.md (exe.dev product parity).

The spec is organized into **7 independent development tracks** that can be executed in parallel by separate agents/developers. Each track has clear boundaries, interfaces, and test criteria.

### Track Dependency Graph

```
Track A (Core Hardening) ----+
                              |
Track B (Server Pool) --------+--> Track F (Ussyverse Integration)
                              |
Track C (UX & Onboarding) ---+
                              |
Track D (Gateway Services) ---+--> Track G (Deployment & Ops)
                              |
Track E (API & Admin) --------+
```

**Tracks A-E can run in parallel** with no blocking dependencies between them.
Track F depends on A + B + C being substantially complete.
Track G depends on all tracks being substantially complete.

### Track Overview

| Track | Name | Description | Estimated Effort |
|---|---|---|---|
| **A** | Core Hardening | Rename, config, ZFS storage, nftables, testing | 2-3 days |
| **B** | Ussyverse Server Pool | Agent binary, gRPC, WireGuard mesh, scheduler | 5-7 days |
| **C** | UX & Onboarding | Tutorial, browser, doc, templates, project browser | 2-3 days |
| **D** | Gateway Services | LLM proxy, email send/receive, BYOK | 2-3 days |
| **E** | API & Admin | HTTPS API, admin panel, trust levels, custom domains | 3-4 days |
| **F** | Ussyverse Integration | BattleBussy arena, project templates, agent presets | 2-3 days |
| **G** | Deployment & Ops | Ansible, Terraform, installer scripts, docs | 2-3 days |

---

## 11. TRACK SPECIFICATIONS

### TRACK A: Core Hardening

**Owner:** Any agent
**Dependencies:** None
**Interfaces with:** All other tracks consume the hardened core

#### A.1 Rename exedevussy -> ussycode
- Change Go module path: `github.com/mojomast/exedevussy` -> `github.com/mojomast/ussycode`
- Rename `cmd/exedevussy/` -> `cmd/ussycode/`
- Update all import paths across all .go files
- Replace user-facing strings: "exedev" -> "ussycode", "exedevussy" -> "ussycode"
- Update base image: user `exedev` -> `ussycode`, paths `/home/exedev` -> `/home/ussycode`
- Update env vars: `EXEDEV_*` -> `USSYCODE_*`
- Update init-exedev.sh -> init-ussycode.sh
- **Test:** `go build ./...` succeeds, `go test ./...` passes

#### A.2 Wire config.go into main.go
- Replace CLI flag parsing in main.go with `internal/config` package
- Support both env vars and flags (env vars take precedence)
- Required config: `USSYCODE_DOMAIN`, `USSYCODE_SSH_PORT`, `USSYCODE_DATA_DIR`, `USSYCODE_DB_PATH`
- Optional config: `USSYCODE_CADDY_API`, `USSYCODE_ACME_EMAIL`, `USSYCODE_ZFS_POOL`
- **Test:** Binary starts with env vars, starts with flags, fails with missing required config

#### A.3 ZFS Storage Backend
- Implement `internal/storage/zfs.go` wrapping `zfs`/`zpool` CLI via `os/exec`
- Operations: CreateBaseImage, SnapshotBaseImage, CloneForVM, ResizeVM, DestroyVM, SetUserQuota, GetUsage, SnapshotVM, ListVMs
- Use zvols (block devices) for VM root disks
- Instant cloning via `zfs clone snapshot target`
- Compression: lz4 by default
- User quotas via ZFS dataset hierarchy (`vmpool/users/<userid>/`)
- Integrate with VM manager (replace current disk creation)
- **Test:** Unit tests with mock exec, integration test with ZFS pool on loopback file

#### A.4 nftables Migration
- Replace iptables calls in `internal/vm/network.go` with nftables
- Create `firecracker` table with `postrouting`, `prerouting`, `forward` chains
- Per-VM rules: masquerade, forward egress/ingress, inter-VM block
- Metadata service interception: redirect 169.254.169.254:80 to host metadata server
- Use nftables sets for scalable rule management (IP sets instead of per-VM rules)
- Cleanup: delete rules by handle when VM is destroyed
- Reconciler: periodic scan for orphaned TAP devices and nftables rules
- **Test:** Integration test creating TAP, adding rules, verifying isolation

#### A.5 Enhanced Testing
- Add integration tests for VM lifecycle (requires Firecracker + root, skip in CI)
- Add integration tests for proxy routes (mock Caddy API)
- Add benchmark for VM creation time
- Ensure all existing tests pass after rename
- **Test:** `go test ./... -count=1` all green

---

### TRACK B: Ussyverse Server Pool (Agent)

**Owner:** Any agent
**Dependencies:** None (can develop against interfaces)
**Interfaces with:** Track A (storage, networking), Track E (scheduler API)

#### B.1 gRPC Protocol Definition
- Create `proto/` directory with Protocol Buffer definitions
- Services: `NodeService` (Register, Heartbeat, ReceiveCommands), `VMService` (Create, Start, Stop, Destroy, Status), `SchedulerService` (PlaceVM, DrainNode)
- Messages: `NodeStatus`, `VMSpec`, `VMStatus`, `JoinRequest`, `JoinResponse`, `HeartbeatRequest`, `HeartbeatResponse`, `Command`
- Generate Go code with `protoc-gen-go` and `protoc-gen-go-grpc`
- **Test:** Proto files compile, generated code builds

#### B.2 Agent Binary (`cmd/ussyverse-agent/`)
- Single binary that runs on contributor nodes
- Subcommands: `join`, `status`, `version`
- `join --token <TOKEN> --control <URL>`: register with control plane, receive mTLS cert
- Agent generates Ed25519 keypair locally on first run
- Stores state in local BoltDB (`/var/lib/ussyverse-agent/state.db`)
- Runs as systemd service
- **Test:** Agent binary builds, `join` against mock gRPC server succeeds

#### B.3 mTLS Certificate Management
- Control plane acts as CA (Ed25519 root -> intermediate -> node certs)
- Join tokens: time-limited, single-use, signed by control plane
- Agent certs: 24h lifetime, auto-renewed every 12h via gRPC
- Revocation: simply stop renewing (no CRL needed with short-lived certs)
- Use stdlib `crypto/x509`, `crypto/ed25519`, `crypto/tls`
- **Test:** CA generates certs, agent authenticates, expired cert rejected

#### B.4 Heartbeat & Health
- Bidirectional gRPC stream between agent and control plane
- Agent sends `NodeStatus` every 10s: CPU, RAM, disk, VM count, throughput, version
- Control plane sends `Command` messages back: StartVM, StopVM, UpdateConfig, Drain
- Lease model: if no heartbeat for 30s -> Unknown, 5min -> Offline, 1hr -> reschedule VMs
- Agent self-quarantines if it can't reach control plane for 24h
- **Test:** Mock stream, verify heartbeat timing, verify timeout transitions

#### B.5 WireGuard Mesh
- Embed `wireguard-go` in agent binary
- Control plane assigns each node a `/24` from `100.64.0.0/10`
- When a node joins, control plane distributes its WireGuard public key to all peers
- Use `tailscale.com/wgengine/magicsock` for STUN + DERP NAT traversal
- Run at least 1 DERP relay server on the control plane
- **Test:** Two agents can reach each other's VMs across WireGuard

#### B.6 VM Placement Scheduler
- Implement in `internal/scheduler/`
- Two-phase: Filter (hard constraints) then Score (soft preferences)
- Weights: BinPacking(0.4), Spread(0.2), Locality(0.2), Freshness(0.1), Trust(0.1)
- Handle rescheduling when nodes go offline
- Handle drain requests (graceful node removal)
- **Test:** Unit tests with mock node list, verify placement decisions

#### B.7 Agent Installer Script
- `https://get.ussyco.de/agent` shell script
- Detects OS/arch, downloads agent binary, verifies signature
- Checks KVM support, creates systemd service
- Prints join instructions
- **Test:** Script runs on fresh Ubuntu 24.04, agent starts

---

### TRACK C: UX & Onboarding

**Owner:** Any agent
**Dependencies:** None (works within existing SSH shell)
**Interfaces with:** Track A (command registration)

#### C.1 Tutorial Command
- Implement `internal/ssh/tutorial.go`
- 10 progressive lessons (see section 6.3)
- Each lesson: explanation text, expected command, validation of result
- Track progress per user in DB (new `tutorial_progress` table)
- Can resume where left off: `tutorial` picks up from last incomplete lesson
- Can skip: `tutorial --lesson=5`
- **Test:** Unit test for each lesson's validation logic

#### C.2 Browser Command
- Generate a one-time magic link token (expires in 5 minutes)
- Print URL: `https://ussyco.de/__auth/magic/<token>`
- Support `--qr` flag for QR code generation (use a Go QR library)
- Control plane HTTP handler validates token and sets auth cookie
- **Test:** Token generation and validation, QR output

#### C.3 Doc Command
- `doc` shows list of documentation topics
- `doc <slug>` shows a specific doc page rendered for terminal
- Store docs as markdown files in `docs/` directory
- Render to terminal with basic formatting (headers, code blocks, lists)
- **Test:** `doc` lists topics, `doc getting-started` renders content

#### C.4 Project Templates
- `projects` command lists available templates from `templates/` directory
- `new --template=geoffrussy` clones template into new VM
- Each template is a directory with: `template.json` (metadata), files to copy
- Template metadata: name, description, ports to expose, post-create script
- Pre-build templates: `geoffrussy`, `battlebussy-agent`, `blank`
- **Test:** `new --template=blank` creates VM with template files

#### C.5 Welcome & MOTD
- Customize welcome message with VM count, last login time
- Show tips/help for new users (first 3 logins)
- Show ussyverse branding and community links
- **Test:** Welcome message varies based on user state

---

### TRACK D: Gateway Services

**Owner:** Any agent
**Dependencies:** None (implements metadata service endpoints)
**Interfaces with:** Track A (metadata server), Track B (multi-node routing)

#### D.1 LLM Gateway Proxy
- Implement actual reverse proxy in `internal/gateway/llm.go`
- Route by provider: `/gateway/llm/anthropic`, `/gateway/llm/openai`, `/gateway/llm/ollama`
- Self-hosted backends: configurable upstream URLs in control plane config
- BYOK: users set API keys via `ssh ussyco.de llm-key set anthropic <key>` (stored encrypted in DB)
- Keys stored in per-user config, injected as `Authorization` header when proxying
- Rate limiting per user (token bucket, configurable by operator)
- Usage tracking: count tokens per request, store in DB for quota enforcement
- **Test:** Mock upstream, verify proxying, verify BYOK key injection, verify rate limiting

#### D.2 Email Receive
- Enable per VM: `share receive-email <vm> on`
- Accept SMTP on port 25 (or delegate to a running MTA like Postfix)
- Deliver to `~/Maildir/new/` in Maildir format inside the VM
- Inject `Delivered-To:` header
- Auto-disable if >1000 unread files accumulate
- Only accept mail for `*@vmname.ussyco.de`
- **Test:** Send test email, verify delivery to Maildir

#### D.3 Email Send
- POST to `http://169.254.169.254/gateway/email/send`
- Body: `{"to":"owner@email.com","subject":"...","body":"..."}`
- `to` must be the VM owner's email (cannot send to arbitrary addresses)
- Rate-limited (token bucket)
- Use SMTP relay (operator-configured) for actual delivery
- **Test:** Mock SMTP relay, verify send, verify rate limit, verify owner-only restriction

---

### TRACK E: API & Admin

**Owner:** Any agent
**Dependencies:** None (builds on existing DB and auth)
**Interfaces with:** Track A (auth tokens), Track B (scheduler for admin)

#### E.1 HTTPS API
- Implement `internal/api/handler.go`
- `POST /exec`: accepts SSH command in body, returns JSON result
- Authentication: Bearer token (stateless SSH-signed tokens) or HTTP Basic Auth
- Token format: `usy0.<base64url_permissions>.<base64url_ssh_signature>`
- Permissions JSON: `exp`, `nbf`, `cmds` (allowed commands), `ctx` (opaque, passed to VM)
- VM-scoped tokens: signed with namespace `v0@VMNAME.ussyco.de`
- Short tokens: `usy1.<opaque>` mapped to full `usy0` token in DB
- Rate limiting per SSH key
- Error codes: 400, 401, 403, 404, 405, 413, 422, 429, 500, 504
- **Test:** Full request/response cycle for each command, token verification, error cases

#### E.2 Admin Web Panel
- Implement `internal/admin/` with embedded web UI
- Serve at `https://admin.ussyco.de/` (authenticated, operator-only)
- Pages: Dashboard (stats), Users (list, trust levels, ban), VMs (list, status), Nodes (Ussyverse Pool health), LLM Usage, Arena (BattleBussy matches)
- Use Go `html/template` + minimal CSS (no JavaScript framework -- keep it light)
- API endpoints under `/admin/api/` returning JSON
- **Test:** Admin pages render, API returns correct data, auth required

#### E.3 Trust Levels & Quotas
- DB schema: add `trust_level` to users table
- Enforce VM count, CPU, RAM, disk limits per trust level
- `new` command checks limits before creating VM
- `ssh-key` commands check trust level for operations
- Operator can set trust level: admin panel or `ussycode admin set-trust <handle> <level>`
- **Test:** User at limit cannot create VM, upgraded user can

#### E.4 Custom Domains
- `share cname <vm> <domain>`: register custom domain for a VM
- DB schema: add `custom_domains` table
- On registration: add Caddy route for the custom domain
- Caddy handles TLS certificate issuance automatically
- Validate domain ownership via TXT record or CNAME check
- **Test:** Custom domain routes to correct VM, TLS works

---

### TRACK F: Ussyverse Integration

**Owner:** Any agent
**Dependencies:** Tracks A, B, C substantially complete
**Interfaces with:** All tracks

#### F.1 BattleBussy Arena
- `arena` subcommand set: `create-match`, `join`, `spectate`, `leaderboard`
- `arena create-match --agents=2 --scenario=web-exploit`:
  - Provisions isolated VMs for each agent
  - Sets up vulnerable target environment
  - Configures WebSocket scoring feed
  - Tears down on match end
- Arena scenarios stored in `templates/arena/` as infrastructure-as-code
- ELO ranking system in DB
- **Test:** Create match, verify VMs provisioned, verify teardown

#### F.2 Pre-configured Agent Templates
- `new --template=geoffrussy`: VM with Geoffrussy pre-installed and configured
- `new --template=battlebussy-agent`: VM with BattleBussy agent SDK, scoring client
- `new --template=openclawssy`: VM with Openclawssy runtime
- Each template: clone repo, install deps, configure agent, print getting-started
- **Test:** Each template creates a working VM

#### F.3 Ussyverse Branding & Community
- Welcome messages reference ussy.host and Discord
- `community` command shows links and stats
- README.md and docs reference the Ussyverse
- MIT license with attribution to Kyle Durepos and shuv
- **Test:** Branding present in welcome, help, and community commands

---

### TRACK G: Deployment & Ops

**Owner:** Any agent
**Dependencies:** All tracks substantially complete
**Interfaces with:** All tracks

#### G.1 Ansible Playbooks
- `deploy/ansible/site.yml`: full deployment playbook
- Roles: `ussycode` (binary + systemd), `caddy` (reverse proxy), `zfs` (storage pool), `firecracker` (VMM + kernel), `wireguard` (mesh), `monitoring` (prometheus + grafana)
- Inventory template for single-node and multi-node
- **Test:** Playbook runs on fresh Ubuntu 24.04 VM

#### G.2 Agent Installer
- `deploy/install-agent.sh`: one-liner for contributor nodes
- Detects OS, installs deps, downloads binary, creates systemd service
- Published at `https://get.ussyco.de/agent`
- **Test:** Script runs on fresh Ubuntu, agent starts and joins

#### G.3 Control Plane Installer
- `deploy/install-control.sh`: sets up control plane on a fresh server
- Installs: ussycode binary, Caddy, ZFS, Firecracker kernel, creates initial admin
- Sets up DNS, generates host SSH key, creates systemd services
- **Test:** Script runs on fresh Ubuntu, `ssh localhost -p 2222` works

#### G.4 Documentation
- `docs/getting-started.md`: quickstart for users
- `docs/self-hosting.md`: guide for operators
- `docs/contributing-compute.md`: guide for node contributors
- `docs/architecture.md`: technical overview
- `docs/api.md`: HTTPS API reference
- **Test:** Docs are complete and accurate

---

## 12. CIRCULAR DEVELOPMENT PROTOCOL

Every agent working on this project MUST follow this protocol to ensure progress is tracked and handoffs work.

### 12.1 Progress Tracking

Each track maintains a `PROGRESS.md` file at the project root:

```markdown
# TRACK [X] PROGRESS

## Status: IN_PROGRESS | BLOCKED | COMPLETE

## Completed
- [x] A.1 Rename exedevussy -> ussycode (commit abc123)
- [x] A.2 Wire config.go into main.go (commit def456)

## In Progress
- [ ] A.3 ZFS Storage Backend
  - Status: implementing CloneForVM
  - Blocker: none
  - Files modified: internal/storage/zfs.go, internal/vm/manager.go

## Not Started
- [ ] A.4 nftables Migration
- [ ] A.5 Enhanced Testing

## Handoff Notes
- ZFS integration requires updating VM manager to accept a StorageBackend interface
- The current disk creation in vm/manager.go lines 78-120 should be replaced
```

### 12.2 Handoff Protocol

When an agent completes its session or reaches a stopping point:

1. **Update PROGRESS.md** with exact state of each task
2. **Commit all work** with descriptive commit message
3. **Note any blockers** with specific details
4. **List files modified** and their purpose
5. **Describe next steps** in enough detail for a new agent to continue immediately
6. **Run tests** and note results: `go build ./... && go test ./...`

### 12.3 Interface Contracts

Tracks communicate through well-defined Go interfaces:

```go
// StorageBackend (Track A provides, Track B consumes)
type StorageBackend interface {
    CloneForVM(ctx context.Context, baseImage, vmID string) (devicePath string, err error)
    DestroyVM(ctx context.Context, vmID string) error
    ResizeVM(ctx context.Context, vmID, newSize string) error
    GetUsage(ctx context.Context, userID string) (*UsageStats, error)
}

// NetworkManager (Track A provides, Track B consumes)
type NetworkManager interface {
    SetupVM(ctx context.Context, vmIndex int) (*VMNetwork, error)
    CleanupVM(ctx context.Context, net *VMNetwork) error
    GuestBootArgs(net *VMNetwork) string
}

// Scheduler (Track B provides, Track E consumes)
type Scheduler interface {
    PlaceVM(ctx context.Context, spec VMSpec) (*Node, error)
    DrainNode(ctx context.Context, nodeID string) error
    ListNodes(ctx context.Context) ([]*NodeStatus, error)
}

// LLMGateway (Track D provides, Track C may use for tutorials)
type LLMGateway interface {
    Proxy(w http.ResponseWriter, r *http.Request, provider string)
    SetUserKey(ctx context.Context, userID, provider, key string) error
}
```

### 12.4 Commit Convention

```
track-X: short description

Longer explanation if needed.

Track: A
Task: A.3
Status: complete|partial
Next: description of what comes next
```

Example:
```
track-a: implement ZFS storage backend

Adds internal/storage/zfs.go with full VM disk lifecycle management
via ZFS zvols. Includes clone, resize, destroy, quota, and snapshot
operations. Integrated with VM manager via StorageBackend interface.

Track: A
Task: A.3
Status: complete
Next: A.4 nftables migration - replace iptables in internal/vm/network.go
```

---

## 13. KNOWN BLOCKERS & MITIGATIONS

### Critical (Must Solve)

| Blocker | Impact | Mitigation |
|---|---|---|
| **KVM required** | Eliminates most VPS providers for agent nodes | Document clearly; target bare metal, Hetzner, OVH, homelab; test nested virt on supported VPS |
| **Firecracker kernel** | Need a pre-built vmlinux compatible with our rootfs | Use Firecracker's provided kernel builds; host on CDN; embed in agent binary |
| **ZFS kernel module** | Not in mainline kernel; needs DKMS or distro package | Ubuntu 24.04 ships ZFS; provide fallback to LVM thin provisioning |
| **Root required** | Agent needs root for KVM, networking, ZFS | Document; provide hardening guide; use Firecracker jailer for least-privilege VM execution |
| **Wildcard DNS** | Control plane needs `*.ussyco.de` | DNS provider with API (Cloudflare); Caddy DNS challenge plugin |

### Significant (Should Solve)

| Blocker | Impact | Mitigation |
|---|---|---|
| **Symmetric NAT (~15%)** | Some contributor nodes can't be hole-punched | DERP relay server mandatory; adds latency but always works |
| **Large rootfs images** | Downloading GB+ images to contributor nodes is slow | Layer caching; P2P distribution between nodes (future); lazy pull |
| **Tailscale dependency tree** | Importing `tailscale.com` pulls large dep tree | Fork only needed packages (magicsock, derp); or accept the dep |
| **Guest kernel config** | Must enable virtio-net, virtio-blk, ext4, networking | Use Firecracker's tested configs; test thoroughly before release |

### Manageable (Nice to Solve)

| Blocker | Impact | Mitigation |
|---|---|---|
| Agent auto-updates | Agents need to update themselves | Blue/green systemd service; download + verify + restart |
| IPv6 support | Not all nodes have IPv6 | WireGuard works fine over IPv4; add IPv6 as enhancement |
| ARM support | No ARM Firecracker support initially | x86_64 only for v1; ARM possible via Cloud Hypervisor in future |

---

## 14. TESTING STRATEGY

### 14.1 Unit Tests (All Tracks)
- Every package has `*_test.go`
- Mock external dependencies (ZFS commands, Firecracker API, Caddy API, gRPC)
- Run with: `go test ./... -count=1 -race`
- Target: 80%+ coverage on non-integration packages

### 14.2 Integration Tests (Local)
- Tag: `//go:build integration`
- Require: KVM, ZFS pool, Firecracker binary, kernel
- Test full VM lifecycle: create -> SSH -> run command -> destroy
- Test proxy routing: create VM -> start web server -> verify HTTPS access
- Run with: `go test ./... -tags=integration -count=1`

### 14.3 Multi-Node Tests (Staging)
- Tag: `//go:build staging`
- Require: 2+ nodes with agent binary
- Test: VM creation on remote node, WireGuard connectivity, scheduling, drain
- Run with: `go test ./... -tags=staging -count=1`

### 14.4 E2E Tests
- Full SSH flow: `ssh ussyco.de` -> register -> new -> ssh -> web server -> share -> rm
- Use Go's `x/crypto/ssh` client in tests
- Run against a real instance (local or staging)

---

## 15. DEPLOYMENT

### 15.1 Single-Node (Development/Small Community)

```bash
git clone https://github.com/mojomast/ussycode.git
cd ussycode
go build -o ussycode ./cmd/ussycode
sudo ./ussycode serve \
  --domain=ussyco.de \
  --ssh-port=22 \
  --data-dir=/var/lib/ussycode \
  --zfs-pool=vmpool \
  --caddy-api=http://localhost:2019
```

### 15.2 Multi-Node (Ussyverse Server Pool)

**Control plane:**
```bash
sudo ./deploy/install-control.sh
```

**Agent nodes:**
```bash
curl -sL https://get.ussyco.de/agent | sudo sh
ussyverse-agent join --token <TOKEN> --control https://ussyco.de
```

### 15.3 Docker Compose (Development Only)

```bash
docker compose -f deploy/docker-compose.dev.yml up
```

---

## 16. SUCCESS METRICS

| Metric | Target |
|---|---|
| Time from `ssh ussyco.de` to first VM | < 30 seconds (including registration) |
| VM creation time | < 3 seconds |
| Tutorial completion rate | > 50% of new users |
| Active weekly users | 20+ within 3 months |
| Agent nodes in pool | 5+ within 3 months |
| Community PRs | 5+ within 6 months |
| Uptime | 99.5%+ |

---

## CREDITS

**ussycode** is an Ussyverse project.

Created by **Kyle Durepos** ([@mojomast](https://github.com/mojomast)) and **shuv** (co-creator).

The Ussyverse is one developer's ever-expanding universe of open-source experiments, built in public with an absurd naming convention and a genuine obsession with making AI agents that actually work. Every project ships open source under MIT.

[ussy.host](https://ussy.host) | [Discord](https://discord.gg/6b2Ej3rS3q) | [GitHub](https://github.com/mojomast)

---

*Built for the Ussyverse. MIT licensed. Ship it.*
