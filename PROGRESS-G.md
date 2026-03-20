# Track G: Deployment & Ops — Progress

## Status: COMPLETE

All four subtasks finished. Build and tests verified passing.

---

## G.1: Ansible Playbooks — COMPLETE

Created full `deploy/ansible/` structure with production-ready roles.

### Files Created
- `deploy/ansible/site.yml` — Main playbook orchestrating all roles
- `deploy/ansible/ansible.cfg` — SSH pipelining, become settings
- `deploy/ansible/inventory/single-node.ini` — Single-node inventory template
- `deploy/ansible/inventory/multi-node.ini` — Multi-node inventory template

### Roles (6 total)
| Role | Purpose | Templates |
|------|---------|-----------|
| `common` | System packages, sysctl, NTP, swap | — |
| `zfs` | ZFS pool + datasets for VM storage | — |
| `firecracker` | Firecracker binary, CNI plugins, kernel, jailer setup | — |
| `caddy` | Caddy reverse proxy with auto-TLS | `Caddyfile.j2` |
| `ussycode` | Build/install ussycode, systemd service, env config | `ussycode.env.j2`, `ussycode.service.j2` |
| `monitoring` | Prometheus node_exporter + ussycode scrape target | `prometheus-ussycode.yml.j2` |

Each role has `tasks/main.yml`, `defaults/main.yml`, and `handlers/main.yml` where applicable.

---

## G.2: Agent Installer Enhancement — COMPLETE

Enhanced `deploy/install-agent.sh` with:
- `verify_checksum()` — SHA256 checksum verification for downloaded binary
- `detect_distro()` — OS distribution identification (Ubuntu, Debian, Fedora, Arch, etc.)
- Added `sha256sum` to dependency checks
- Improved `print_summary()` with structured join instructions, retry commands, monitoring tips

---

## G.3: Control Plane Installer — COMPLETE

Created `deploy/install-control.sh` (~500 lines) with:
- Full argument parsing: `--domain`, `--email`, `--zfs-device`, `--build`, `--skip-firewall`, `--version`
- Pre-flight checks: Ubuntu 24.04, KVM support, 4 GB+ RAM, root privileges
- Installs: system deps, Go 1.24.1, ZFS pool + datasets, Firecracker + kernel, Caddy
- Builds ussycode from source (`--build`) or downloads release binary
- Creates systemd service, SSH host key, UFW firewall rules, sysctl tuning
- Post-install summary with DNS setup instructions

---

## G.4: Documentation — COMPLETE

Created 5 comprehensive docs (all from scratch; `docs/` was empty):

| File | Description |
|------|-------------|
| `docs/getting-started.md` | End-user quick start, SSH commands, VM management, sharing, templates |
| `docs/self-hosting.md` | Requirements, Ansible & manual setup, full config reference table, DNS/TLS |
| `docs/contributing-compute.md` | Ussyverse pool, agent install, trust levels, monitoring, troubleshooting |
| `docs/architecture.md` | ASCII diagrams: system overview, SSH flow, VM lifecycle, networking, storage, multi-node |
| `docs/api.md` | Token formats, POST /exec endpoint, rate limiting, error codes, curl examples |

---

## Verification

```
$ go build ./...   # PASS (no errors)
$ go test ./...    # PASS (11 packages tested, 0 failures)
```

No Go source files were modified in Track G — only shell scripts, Ansible YAML, Jinja2 templates, and Markdown documentation were created/modified.
