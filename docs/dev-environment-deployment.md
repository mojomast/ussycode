# Ussycode dev/test environment deployment guide

Everything learned from the 2026-03-21 deployment attempt on shuvdev, including what worked, what broke, and what the real deployment needs.

## Summary

We attempted to deploy ussycode + routussy inside a KVM VM on shuvdev (home workstation) behind a Cloudflare tunnel. HTTP endpoints worked externally after significant debugging, but **SSH access (the core product) is fundamentally blocked** — Cloudflare tunnels cannot proxy raw TCP without requiring `cloudflared` on every client, which defeats the purpose of an SSH-first platform.

**Conclusion:** Ussycode needs a server with a **public IP and direct port exposure**, not a home LAN behind NAT/tunnels.

---

## What we built and verified

### Code fixes (committed + pushed)

All 4 deployment blockers were resolved in commit `1b7269d`:

| Blocker | Fix | Files |
|---|---|---|
| **API handler nil wiring** | Already fixed in `5a34b22` | — |
| **SMTP test failure** | Already fixed in `5a34b22` | — |
| **Magic-link auth** | URL pattern fixed, handler added, route registered on both muxes | `browser.go`, `admin.go`, `admin_test.go`, `main.go` |
| **Share-link redemption** | 4 gaps: Caddy forward_auth missing, X-Forwarded-Host/Uri parsing, redirect URL reconstruction | `caddy.go`, `auth.go`, `auth_test.go`, `config.go`, `main.go` |

**+689 lines, 13 new tests, all tests pass.**

### Routussy deployment (validated)

The Discord bot + LLM proxy deployed cleanly as a Bun service:

- `bun install` + `bun run src/index.ts` — instant startup
- SQLite auto-migrates on first boot (10 tables)
- Discord bot logged in as `routussy#6827`, 13 slash commands registered
- Health endpoint responds correctly
- Systemd service with `Restart=always`

**.env file created at:** `/home/shuv/repos/ussyverse/ussyco.de/ussyrouter/.env` (gitignored)

### Ussycode deployment (validated)

The Go binary built and ran with all 7 listeners:

| Port | Service | Status |
|---|---|---|
| `:2222` | SSH gateway | ✅ |
| `:3000` | Routussy (co-located) | ✅ |
| `:8080` | HTTP API | ✅ |
| `:8083` | Metadata service | ✅ |
| `:9090` | Admin panel | ✅ |
| `:9876` | Auth proxy | ✅ |
| `:2525` | SMTP server | ✅ |

- DB migrations ran (all 10)
- Firecracker v1.15.0 installed, guest kernel (vmlinux-6.1.102) downloaded
- VM provisioning reported as "enabled"
- SSH auth via routussy fingerprint lookup worked end-to-end
- Ussyuntu rootfs image built from Docker export → ext4

### Cloudflare integration (partially validated)

HTTP endpoints worked externally through the CF tunnel:
- `https://ussycode-api.shuv.dev/health` → routussy ✅
- `https://ussycode.shuv.dev/health` → ussycode API ✅
- `https://ussycode-admin.shuv.dev/` → admin panel ✅

---

## Limitations and blockers we hit

### 1. Cloudflare tunnels cannot expose raw SSH

**This is the fundamental blocker.** CF tunnels proxy HTTP/HTTPS natively, but TCP (SSH) requires `cloudflared access tcp` as a ProxyCommand on every client machine. This means:

- Users cannot simply `ssh ussycode.shuv.dev -p 2222`
- Every user must install cloudflared and configure `~/.ssh/config` with a `ProxyCommand`
- This completely defeats ussycode's value proposition of "just SSH and you're in"

**Requirement:** The production server needs a **public IP with port 2222 open** for direct SSH access.

### 2. Cloudflare Universal SSL only covers one subdomain level

`*.shuv.dev` covers `ussycode.shuv.dev` but NOT `api.ussycode.shuv.dev` or `*.ussycode.shuv.dev`. We hit TLS handshake failures on 2-level deep subdomains.

**Workaround used:** Flattened to `ussycode-api.shuv.dev`, `ussycode-admin.shuv.dev`, etc.

**For production:** Either use flat subdomains, get a Cloudflare Advanced Certificate covering `*.ussycode.shuv.dev`, or use a dedicated domain like `ussyco.de` where you control the cert.

### 3. WARP client on shuvdev poisons local routing

The Cloudflare WARP client installs ip policy rules and nftables chains that intercept ALL outbound traffic:

- `ip rule 5209`: routes unmarked packets through WARP lookup table 65743
- `nftables inet cloudflare-warp output`: explicit accept list, everything else falls through
- SYN-ACK responses to LAN hosts (10.0.2.x) get silently eaten by the WARP routing policy
- This made `localhost` and `127.0.0.1` socat forwarding fail — cloudflared could not reach local ports
- Even `10.0.2.100` (the host's own LAN IP) was unreachable from cloudflared

**Workaround:** Gave the VM a macvtap interface with a direct LAN IP (10.0.2.149) so both tunnel connectors (shuvdev + shuvhost) could reach it without going through WARP routing.

**Lesson:** Never run ussycode on a host with WARP client installed. Use a clean server.

### 4. Dual tunnel connectors cause random routing

The HOME CF tunnel has two connectors (shuvdev + shuvhost/10.0.2.11). Cloudflare edge randomly picks which connector handles each request. Routes pointing to localhost or 192.168.122.x only work from one connector.

**Fix:** Routes must use an IP reachable from ALL connectors on the tunnel. For the home LAN, that means using a LAN IP (10.0.2.x), not localhost.

### 5. KVM VM networking on shuvdev required extensive firewall fixes

Multiple firewall layers blocked VM networking:

| Layer | Issue | Fix |
|---|---|---|
| **iptables INPUT** | `policy DROP` blocked DHCP from VM to dnsmasq | `iptables -I INPUT -i virbr0 -j ACCEPT` |
| **iptables FORWARD** | `policy DROP` blocked VM internet | `iptables -I FORWARD -i virbr0 -j ACCEPT` |
| **nftables inet ussycode** | `forward policy drop` blocked Docker in VM | Added `iifname "docker0" accept` |
| **Docker DNS** | Default Docker DNS resolver failed inside VM | Set `{"dns": ["8.8.8.8"]}` in `/etc/docker/daemon.json` |

### 6. Discord bot channel permissions

The bot got "Missing Access" (403, code 50001) when trying to post the admin review embed to the `#routussy` channel. The bot needs explicit **Send Messages** + **Embed Links** permissions on the channel.

### 7. Firecracker kernel URL changed

The S3 URL for the Firecracker CI kernel (`spec.ccfc.min/firecracker-ci/v1.12/...`) returned 404. The `v1.11` path had the same `vmlinux-6.1.102` file. Pin the kernel download URL or bundle it.

---

## Requirements for the real deployment

### Server requirements

| Requirement | Why |
|---|---|
| **Public IPv4 address** | SSH on port 2222 must be directly reachable — no NAT, no tunnel |
| **KVM support** (`/dev/kvm`) | Firecracker microVMs need hardware virtualization |
| **4+ CPU cores, 8+ GB RAM** | Host overhead + VM headroom |
| **50+ GB disk** (preferably SSD) | ZFS pool for VM images and clones |
| **Ubuntu 22.04/24.04** | Tested base OS |
| **No WARP client** | WARP routing policy breaks local service connectivity |

### Recommended providers

- **Hetzner dedicated** — cheap bare metal with KVM, static IP, good bandwidth
- **OVH/SoYouStart** — same
- **Vultr bare metal** — if you need US presence
- **Any VPS with nested virt** — works but less ideal (we proved nested virt works)

### Network requirements

| Port | Protocol | Service |
|---|---|---|
| **22** | TCP | Host SSH (admin access) |
| **2222** | TCP | Ussycode SSH gateway (public, the product) |
| **80** | TCP | Caddy HTTP (ACME + redirect) |
| **443** | TCP | Caddy HTTPS (VM subdomain proxy) |

Ports 3000, 8080, 8083, 9090, 9876, 2525 are internal only.

### DNS requirements

| Record | Type | Value |
|---|---|---|
| `ussycode.shuv.dev` | A | `<server-ip>` |
| `*.ussycode.shuv.dev` | A | `<server-ip>` |

Or use a dedicated domain (e.g., `dev.ussyco.de` + `*.dev.ussyco.de`).

**If using `*.ussycode.shuv.dev`:** Need Cloudflare Advanced Certificate or use Caddy for TLS (Caddy handles its own ACME and doesn't need CF Universal SSL).

### Domain layout (recommended)

| Domain | Service |
|---|---|
| `ussycode.shuv.dev` | SSH gateway (port 2222) + HTTP API (port 8080 behind Caddy) |
| `*.ussycode.shuv.dev` | Per-VM HTTPS subdomains (Caddy wildcard) |
| `ussycode-api.shuv.dev` | Routussy LLM proxy (if staying on shuv.dev) |

Or with a dedicated domain:
| `dev.ussyco.de` | SSH + HTTP |
| `*.dev.ussyco.de` | VM subdomains |
| `api.ussyco.de` | Routussy |

---

## Deployment checklist (for the real server)

### Pre-deployment

- [ ] Server provisioned with public IP + KVM
- [ ] DNS A records created (base + wildcard)
- [ ] SSH access to server confirmed
- [ ] Ports 22, 2222, 80, 443 open in provider firewall

### Credentials needed

- [ ] `DISCORD_TOKEN` — bot token from Discord Developer Portal
- [ ] `DISCORD_CLIENT_ID` — application ID
- [ ] `UPSTREAM_API_KEY` — Z.AI or LLM provider key
- [ ] `USSYCODE_INTERNAL_KEY` — generated shared secret (we have: `36924a78d2c5b15b8b3b3e6366fb6cb52d5d387b50214b96f8cdcced235907e9`)
- [ ] Cloudflare API token (for DNS-01 ACME challenges if using Caddy + wildcard TLS)
- [ ] Discord channel ID for admin review

### Deploy routussy

```bash
git clone <ussyrouter-repo> ~/ussyrouter
cp .env ~/ussyrouter/.env  # use the .env we already generated
cd ~/ussyrouter
bun install
# Create systemd service, enable, start
```

### Deploy ussycode

```bash
git clone <ussycode-repo> ~/ussycode
cd ~/ussycode
go build -o /usr/local/bin/ussycode ./cmd/ussycode
# Set up:
#   - ZFS pool (zpool create vmpool /dev/sdX)
#   - Network bridge (ussy0, 10.0.0.1/24)
#   - nftables (NAT masquerade, metadata DNAT, inter-VM isolation)
#   - Caddy with wildcard TLS
#   - Firecracker binary + kernel
#   - Env file + systemd service
```

Or use the one-liner installer:
```bash
curl -sL https://get.ussyco.de/install | sudo bash -s -- \
  --domain ussycode.shuv.dev --email admin@shuv.dev \
  --zfs-device /dev/sdb --build
```

### Build ussyuntu rootfs

```bash
docker build -t ussyuntu:latest ./images/ussyuntu/
# Then extract to ext4 (the ussycode binary can do this via OCI image pull)
```

### Verify end-to-end

1. `curl https://ussycode.shuv.dev/health` — API health
2. `curl https://ussycode-api.shuv.dev/health` — routussy health
3. `ssh ussycode.shuv.dev -p 2222` — SSH gateway (should show welcome)
4. Discord `/ussycode-request` — request access with SSH key
5. Admin approves → SSH key registered in routussy
6. SSH in → `new test-vm` → VM boots in <3s
7. `ssh test-vm` → inside the microVM
8. `https://test-vm.ussycode.shuv.dev` → HTTPS proxy works

---

## Files and artifacts from this session

| File | Location | Purpose |
|---|---|---|
| Routussy `.env` | `/home/shuv/repos/ussyverse/ussyco.de/ussyrouter/.env` | Production-ready env (gitignored) |
| Code fixes | Commit `1b7269d` on `main` | Magic-link + share-link fixes |
| Upstream merge | Commit `d7c8ceb` on `main` | PTY terminal sizing, nftables, env injection |
| This document | `docs/dev-environment-deployment.md` | You're reading it |

## Discord bot

- **Bot invite link:** https://discord.com/api/oauth2/authorize?client_id=1485131011606974605&permissions=149504&scope=bot+applications.commands
- **Bot name:** routussy#6827
- **Required channel permission:** Send Messages + Embed Links in the review channel
- **Channel ID configured:** `1485132239195996312` (for all three: admin review, media alerts, routussy)
