# ussycode

Self-hosted dev environment platform for the Ussyverse. Instant SSH-accessible microVMs with persistent disks, automatic HTTPS, and AI agent support.

**`ssh ussyco.de`** -- get a dev environment in seconds. No signup. No credit card. SSH keys are identity.

## What is this?

ussycode gives anyone instant dev environments via SSH. Under the hood it provisions Firecracker microVMs with ZFS-backed persistent storage, automatic HTTPS via Caddy wildcard TLS, and a built-in metadata service with LLM proxy access.

Anyone can contribute compute to the **Ussyverse Server Pool** by deploying a single agent binary on their own hardware. The pool grows organically as community members donate iron.

## Architecture

```
ssh ussyco.de  -->  SSH Gateway  -->  Control Plane  -->  Firecracker microVM
                                          |                    |
                                       SQLite DB          ZFS zvol (persistent)
                                          |
                                    Caddy Reverse Proxy  -->  username.ussyco.de
```

**Single-node:** SSH gateway, VM manager, Caddy proxy, SQLite -- all in one Go binary.

**Multi-node:** Control plane coordinates agent nodes over gRPC/mTLS + WireGuard mesh. Agents run VMs on bare metal, VPS, or homelab hardware.

## Tech Stack

| Component | Technology |
|---|---|
| Language | Go |
| Database | SQLite (WAL mode, modernc.org/sqlite) |
| VMM | Firecracker microVMs |
| Storage | ZFS zvols (COW clones, snapshots) |
| Reverse Proxy | Caddy v2 (auto TLS, wildcard certs) |
| SSH | gliderlabs/ssh |
| Networking | TAP devices, nftables, WireGuard mesh |
| Base Image | Ubuntu 24.04 (Go + Python + Node pre-installed) |

## Project Status

**Phase 1-2 (partially complete):** SSH gateway, database, auth, VM manager skeleton, proxy integration, base image.

**Phase 3+ (not started):** Firecracker integration testing, ZFS storage, agent node pool, multi-node scheduling, production hardening.

See [spec.md](spec.md) for the full product specification with 7 parallel development tracks.

## Repository Structure

```
cmd/exedevussy/        Entry point (will be renamed to cmd/ussycode/)
internal/
  auth/                SSH key-based stateless token auth
  config/              Configuration (needs wiring)
  db/                  SQLite with WAL, goose migrations, 30+ queries
  gateway/             Metadata service (169.254.169.254)
  proxy/               Caddy admin API integration
  ssh/                 SSH gateway, REPL shell, 16 commands
  vm/                  Firecracker VM manager, OCI image pull, networking
images/ussyuntu/       Base VM image (Dockerfile)
deploy/                Ansible & Terraform (scaffolded)
templates/             VM templates (blank, battlebussy-agent, geoffrussy)
spec.md                Full product specification v2.0
PROMPT.md              One-shot orchestrator prompt for autonomous development
```

## Development

### Prerequisites

- Go 1.25+
- Linux with KVM support (`/dev/kvm`)
- Firecracker binary
- ZFS kernel module (for storage track)
- Root access (for VM networking and Firecracker jailer)

### Build

```bash
go build -o ussycode ./cmd/ussycode
```

### Test

```bash
go test ./...
```

### Run

```bash
sudo ./ussycode --addr :2222 --http-addr :8080 --domain ussyco.de
```

## Web Apps Inside VMs

ussycode automatically proxies each VM's port `8080` to its public subdomain.

- bind app servers to `0.0.0.0`, not `127.0.0.1`
- prefer port `8080`
- public URL format is `https://<vm-name>.<domain>`

Example:

```bash
python3 -m http.server 8080 --bind 0.0.0.0
```

## OpenCode In VMs

The `ussyuntu` VM image now includes OpenCode plus a bundled OpenCode skill for web exposure.

Inside a fresh VM, OpenCode is preconfigured with:

- `~/.config/opencode/opencode.json`
- `~/.config/opencode/instructions/ussycode-runtime.md`
- `~/.config/opencode/skills/ussycode-web-proxy/SKILL.md`

That skill teaches OpenCode to:

- bind dev servers to `0.0.0.0`
- prefer port `8080`
- report the public proxied URL instead of `localhost`

## Parallel Development Tracks

The spec defines 7 independent tracks designed for parallel execution by a swarm of agents:

| Track | Focus | Status |
|---|---|---|
| **A** | Core Cleanup | Module rename, config wiring, error handling |
| **B** | VM Lifecycle | Firecracker boot, ZFS storage, image pipeline |
| **C** | Networking | TAP/nftables, metadata service, Caddy integration |
| **D** | SSH & UX | Gateway hardening, session proxy, REPL polish |
| **E** | Agent Node Pool | gRPC service, WireGuard mesh, scheduler |
| **F** | Security | Jailer, seccomp, audit logging, rate limiting |
| **G** | CI/CD & Ops | GitHub Actions, Ansible playbooks, monitoring |

See [PROMPT.md](PROMPT.md) for the orchestrator prompt that can spin up subagents for each track.

## Credits

Created by [Kyle Durepos](https://github.com/mojomast) ([@mojomast](https://github.com/mojomast)) and [shuv](https://github.com/shuv1337) ([@shuv1337](https://github.com/shuv1337)).

Part of [The Ussyverse](https://ussy.host) -- an ever-expanding open-source ecosystem.

## License

[MIT](LICENSE)
