# ussycode

Self-hosted dev environment platform for the Ussyverse. Instant SSH-accessible microVMs with persistent disks, automatic HTTPS, and AI agent support.

**`ssh ussyco.de`** — get a dev environment in seconds. No signup. No credit card. SSH keys are identity.

## What is this?

ussycode gives anyone instant dev environments via SSH. Under the hood it provisions Firecracker microVMs with ZFS-backed persistent storage, automatic HTTPS via Caddy wildcard TLS, and a built-in metadata service with LLM proxy access.

Anyone can contribute compute to the **Ussyverse Server Pool** by deploying a single agent binary on their own hardware. The pool grows organically as community members donate iron.

## Product Vision

ussycode targets **feature parity with [exe.dev](https://exe.dev)** — a self-hosted, Ussyverse-native version of the full product loop:

1. SSH in → create a VM
2. Run your app inside the VM
3. Get an automatic HTTPS URL (`username.ussyco.de`)
4. Share it (public link, email invite, or access-controlled)
5. Control access (public / private / invite-only)
6. Script it via API (`POST /exec`, tokens, BYOK LLM access)

Where exe.dev is a closed hosted product, ussycode is **open-source and self-hostable** while staying first-class within the Ussyverse (trust tiers, community arena, email gateway, agent support).

See [`PLAN-exe-dev-parity-roadmap.md`](PLAN-exe-dev-parity-roadmap.md) for the full gap analysis and phased delivery plan.

## Architecture

```
ssh ussyco.de  -->  SSH Gateway  -->  Control Plane  -->  Firecracker microVM
                                          |                    |
                                       SQLite DB          ZFS zvol (persistent)
                                          |
                                    Caddy Reverse Proxy  -->  username.ussyco.de
```

**Single-node:** SSH gateway, VM manager, Caddy proxy, SQLite — all in one Go binary.

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

| Area | Status |
|---|---|
| Core platform (SSH gateway, VM lifecycle, proxy, auth, metadata, API) | ✅ Implemented |
| LLM gateway (5 providers, BYOK, rate limiting) | ✅ Implemented |
| Email (inbound SMTP + outbound) | ✅ Implemented |
| Trust/quota system (4 tiers) | ✅ Implemented |
| Admin panel | ✅ Implemented |
| Custom domains | ✅ Implemented |
| Tutorial (10 lessons) | ✅ Implemented |
| Arena / community features | ✅ Implemented |
| API wiring (`POST /exec`) | ⚠️ Needs fix (nil executor) |
| Browser auth flow | ⚠️ Needs fix (URL mismatch) |
| Multi-node cluster | 🔧 Scaffolded, not integrated |
| Telemetry / observability | ❌ Not yet implemented |

## Development Roadmap

Full plan: [`PLAN-exe-dev-parity-roadmap.md`](PLAN-exe-dev-parity-roadmap.md)

| Phase | Goal |
|---|---|
| **0 — Stabilize** | Fix API wiring, browser auth, failing tests, stale docs |
| **1 — Core UX loop** | Verified boot → run → HTTPS URL end-to-end |
| **2 — Sharing & auth** | Public/private/invite-only URL sharing, access control |
| **3 — API** | `/exec` + token auth fully wired, scriptable from agents |
| **4 — Email** | Inbound routing + outbound delivery integrated into product flows |
| **5 — Agents** | AGENTS.md support, prompt-on-create, Shelley-compatible tooling |
| **6 — Teams** | Multi-user orgs, SSO, per-org resource quotas |
| **7 — Cluster** | Multi-node agent pool fully integrated and schedulable |

## Repository Structure

```
cmd/ussycode/          Main server entry point
cmd/ussyverse-agent/   Multi-node agent binary
internal/
  admin/               Operator/admin web panel
  api/                 HTTPS /exec API
  auth/                SSH-signed token primitives
  config/              Configuration loading and validation
  db/                  SQLite models, queries, embedded migrations
  gateway/             Metadata + LLM/email gateway services
  proxy/               Caddy auth/proxy integration
  ssh/                 SSH gateway, REPL shell, shared command executor
  vm/                  Firecracker VM manager, images, networking
images/ussyuntu/       Base VM image (Dockerfile + init script)
deploy/                Deployment assets
spec.md                Full product specification
PLAN-exe-dev-parity-roadmap.md  exe.dev parity gap analysis and roadmap
```

## Development

### Prerequisites

- Go 1.25+
- Linux with KVM support (`/dev/kvm`)
- Firecracker binary
- ZFS kernel module (for storage)
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
sudo ./ussycode --addr :2222 --http-addr :8080 --admin-addr :9090 --domain ussyco.de
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

## Credits

Created by [Kyle Durepos](https://github.com/mojomast) ([@mojomast](https://github.com/mojomast)) and [shuv](https://github.com/shuv1337) ([@shuv1337](https://github.com/shuv1337)).

Part of [The Ussyverse](https://ussy.host) — an ever-expanding open-source ecosystem.

## License

[MIT](LICENSE)
