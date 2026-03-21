# ussycode

Self-hosted dev environment platform for the Ussyverse. Instant SSH-accessible microVMs with persistent disks, automatic HTTPS, and AI agent support.

**`ssh -p 2224 dev.ussyco.de`** -- get a dev environment in seconds. No signup. No credit card. SSH keys are identity.

## What is this?

ussycode gives anyone instant dev environments via SSH. Under the hood it provisions Firecracker microVMs with persistent storage, automatic HTTPS via Caddy wildcard TLS, and a built-in metadata service. LLM access is provided through [Routussy](https://github.com/mojomast/ussycode/../battlebussy2/routussy), a Discord-managed OpenAI-compatible proxy that handles access control and billing.

## Product Vision

ussycode targets **feature parity with [exe.dev](https://exe.dev)** -- a self-hosted, Ussyverse-native version of the full product loop:

1. SSH in -> create a VM
2. Run your app inside the VM
3. Get an automatic HTTPS URL (`vmname.dev.ussyco.de`)
4. Share it (public link, email invite, or access-controlled)
5. Control access (public / private / invite-only)
6. Script it via API (`POST /exec`, tokens, BYOK LLM access)

Where exe.dev is a closed hosted product, ussycode is **open-source and self-hostable** while staying first-class within the Ussyverse (trust tiers, community arena, email gateway, agent support).

See [`PLAN-exe-dev-parity-roadmap.md`](PLAN-exe-dev-parity-roadmap.md) for the full gap analysis and phased delivery plan.

## Architecture

```
ssh dev.ussyco.de  -->  SSH Gateway  -->  VM Manager  -->  Firecracker microVM
                            |                                    |
                         SQLite DB                          metadata service
                            |                              (169.254.169.254)
                      Caddy Reverse Proxy  -->  vmname.dev.ussyco.de
                            |
                    Routussy (api.ussyco.de)
                      - SSH key validation
                      - LLM proxy + billing
                      - Discord access control
```

**Single binary:** SSH gateway, VM manager, Caddy proxy integration, metadata service, SQLite -- all in one Go binary.

**Multi-node:** Control plane coordinates agent nodes over gRPC/mTLS + WireGuard mesh. Agents run VMs on bare metal, VPS, or homelab hardware.

## Tech Stack

| Component | Technology |
|---|---|
| Language | Go |
| Database | SQLite (WAL mode, modernc.org/sqlite) |
| VMM | Firecracker microVMs |
| Reverse Proxy | Caddy v2 (admin API, wildcard certs) |
| SSH | gliderlabs/ssh |
| Networking | TAP devices, nftables, bridge networking |
| Base Image | Ubuntu 24.04 (Go + Python + Node pre-installed) |
| LLM Proxy | [Routussy](https://github.com/mojomast/ussycode/../battlebussy2/routussy) |
| PTY Handling | creack/pty/v2 |

## Project Status

| Area | Status |
|---|---|
| Core platform (SSH gateway, VM lifecycle, proxy, auth, metadata, API) | Done |
| LLM gateway (5 providers, BYOK, rate limiting) | Done |
| Email (inbound SMTP + outbound) | Done |
| Trust/quota system (4 tiers) | Done |
| Admin panel | Done |
| Custom domains | Done |
| Tutorial (10 lessons) | Done |
| Pi default AI experience | Done |
| Arena / community features | Done |
| Routussy integration (SSH auth, fingerprint billing, Discord onboarding) | Done |
| API wiring (`POST /exec`) | Needs fix (nil executor) |
| Browser auth flow | Needs fix (URL mismatch) |
| Multi-node cluster | Scaffolded, not integrated |
| Telemetry / observability | Not yet implemented |

## Development Roadmap

Full plan: [`PLAN-exe-dev-parity-roadmap.md`](PLAN-exe-dev-parity-roadmap.md)

| Phase | Goal |
|---|---|
| **0 -- Stabilize** | Fix API wiring, browser auth, failing tests, stale docs |
| **1 -- Core UX loop** | Verified boot -> run -> HTTPS URL end-to-end |
| **2 -- Sharing & auth** | Public/private/invite-only URL sharing, access control |
| **3 -- API** | `/exec` + token auth fully wired, scriptable from agents |
| **4 -- Email** | Inbound routing + outbound delivery integrated into product flows |
| **5 -- Agents** | AGENTS.md support, prompt-on-create, Shelley-compatible tooling |
| **6 -- Teams** | Multi-user orgs, SSO, per-org resource quotas |
| **7 -- Cluster** | Multi-node agent pool fully integrated and schedulable |

## Routussy Integration

Ussycode delegates access control and LLM billing to [Routussy](https://github.com/mojomast/ussycode/../battlebussy2/routussy), a Discord bot and OpenAI-compatible proxy hosted at `api.ussyco.de`.

### How It Fits Together

1. **Onboarding**: Users request ussycode access via Discord (`/ussycode-request`) with their SSH public key. Admins approve or deny.
2. **SSH Authentication**: When a non-Tailscale user connects, the SSH gateway calls Routussy's `/ussycode/authorized-keys` endpoint to check if their SSH fingerprint is approved.
3. **LLM Access**: VMs get automatic LLM access without users copying API keys. The metadata service injects `OPENCODE_API_KEY=ussycode-fp:SHA256:<fingerprint>` into each VM. When OpenCode or pi makes requests, Routussy resolves the fingerprint back to the user and charges their budget.

### Auth Flow

```
User connects via SSH
  -> Gateway extracts SSH fingerprint
  -> Non-Tailscale IPs: GET api.ussyco.de/ussycode/authorized-keys
     (checks fingerprint against approved keys)
  -> User enters VM
  -> VM env has OPENCODE_API_KEY=ussycode-fp:SHA256:<fingerprint>
  -> AI tools send LLM requests to api.ussyco.de/v1
  -> Routussy resolves fingerprint -> user -> budget -> proxies to upstream
```

### Configuration

Set these in `/etc/ussycode-dev/ussycode.env` (or via CLI flags):

```
USSYCODE_ROUTUSSY_URL=https://api.ussyco.de
USSYCODE_ROUTUSSY_INTERNAL_KEY=<shared-secret>
```

The shared secret must match `USSYCODE_INTERNAL_KEY` in Routussy's `.env`.

## SSH Gateway

The SSH gateway (`internal/ssh/`) provides:

- **Interactive REPL**: Create, start, stop, rename, and delete VMs
- **Direct VM access**: `ssh vmname@dev.ussyco.de -p 2224` proxies into the VM with proper PTY handling (dimensions, SIGWINCH forwarding via `creack/pty/v2`)
- **Hardened auth**: Non-Tailscale connections (outside `100.64.0.0/10`) require an SSH fingerprint registered through Routussy
- **Tailscale passthrough**: Connections from the Tailscale range are trusted without Routussy validation

## Web Apps Inside VMs

ussycode automatically proxies each VM's port `8080` to its public subdomain via Caddy.

- Bind app servers to `0.0.0.0`, not `127.0.0.1`
- Prefer port `8080` (Caddy proxies this port automatically)
- Public URL: `https://<vm-name>.dev.ussyco.de`

Example:

```bash
python3 -m http.server 8080 --bind 0.0.0.0
# -> accessible at https://mild-owl.dev.ussyco.de
```

VMs have `USSYCODE_VM_NAME` and `USSYCODE_PUBLIC_DOMAIN` env vars so AI tools can construct the correct public URL automatically.

## AI Assistance In VMs

The `ussyuntu` VM image includes **pi** (default) and **OpenCode** (optional) as AI coding assistants.

### pi (Default -- auto-launches on SSH)

pi launches automatically when users SSH into a VM. It's configured with:

- `@ussyverse/pi-ussycode` package for ussycode-specific tools, skills, and theme
- `ussyrouter` provider for LLM access with budget enforcement
- Fingerprint-based authentication (no raw API keys exposed)
- First-run onboarding for new users
- `/publish` command for quick web app exposure

Users can exit pi anytime for a normal shell, and restart it with `pi`.

### OpenCode (Optional)

OpenCode is installed and manually runnable via `opencode`. Pre-configured with:

- `~/.config/opencode/opencode.json` -- provider config pointing at Routussy for GLM models
- `~/.config/opencode/instructions/ussycode-runtime.md` -- runtime instructions
- `~/.config/opencode/skills/ussycode-web-proxy/SKILL.md` -- skill that teaches OpenCode to bind to `0.0.0.0:8080` and report the correct `*.dev.ussyco.de` public URL

Uses the same Routussy proxy and budget as pi. Does not auto-launch.

## VM Networking

- Bridge `ussy0` at `10.0.0.1/24`
- VMs get IPs from `10.0.0.x`
- NAT masquerade via nftables `inet ussycode` table
- Metadata service at `169.254.169.254:80` (DNAT from VM traffic on port 80 to `:8083`)
- Metadata injects SSH keys, hostname, and env vars (including LLM credentials) into VMs at boot

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

- Go 1.22+
- Linux with KVM support (`/dev/kvm`)
- Firecracker binary
- Root access (for VM networking and TAP devices)

### Build

```bash
go build -o ussycode-dev ./cmd/ussycode
```

### Test

```bash
go test ./...
```

### Run

```bash
sudo ./ussycode-dev \
  --addr :2224 \
  --domain dev.ussyco.de \
  --admin-addr :9091 \
  --data-dir /var/lib/ussycode-dev \
  --routussy-url https://api.ussyco.de
```

### Deploy as systemd service

```bash
sudo cp ussycode-dev /usr/local/bin/
sudo systemctl restart ussycode-dev
```

### Rebuild VM base image

```bash
docker build --network host -t ussyuntu:latest images/ussyuntu/
# Go code auto-extracts to ext4 on next VM creation
```

## Infrastructure (Current Deployment)

| Service | Port | Description |
|---|---|---|
| SSH gateway | 2224 | ussycode SSH entry point |
| Caddy | 8085 | VM web subdomain proxy |
| Metadata | 8083 | VM metadata service (169.254.169.254 via DNAT) |
| Routussy | 3000 | LLM proxy + Discord bot (separate project) |
| nginx | 80/443 | TLS termination, reverse proxy to all services |

DNS: `*.dev.ussyco.de` and `*.ussyco.de` both resolve to `104.218.100.104`. Wildcard SSL via Let's Encrypt DNS-01 challenge.

## Credits

Created by [Kyle Durepos](https://github.com/mojomast) ([@mojomast](https://github.com/mojomast)) and [shuv](https://github.com/shuv1337) ([@shuv1337](https://github.com/shuv1337)).

Part of [The Ussyverse](https://ussy.host) -- an ever-expanding open-source ecosystem.

## License

[MIT](LICENSE)
