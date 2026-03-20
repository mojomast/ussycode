# Self-Hosting ussycode

This guide covers deploying your own ussycode instance on bare metal or a VPS.

## Requirements

### Hardware

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| CPU | 4 cores (VT-x/AMD-V) | 8+ cores |
| RAM | 4 GB | 16+ GB |
| Storage | 50 GB SSD | 200+ GB NVMe |
| Network | 100 Mbps | 1 Gbps |

**KVM is required.** ussycode uses Firecracker microVMs, which need hardware virtualization. Verify with:

```bash
ls -la /dev/kvm
# If missing: enable VT-x/AMD-V in BIOS, then modprobe kvm_intel (or kvm_amd)
```

### Operating System

- **Ubuntu 24.04 LTS** (recommended and tested)
- Ubuntu 22.04 LTS (supported)
- Debian 12 (should work, not officially tested)

### Network

- Public IPv4 address
- Ports 22, 80, 443, 2222 open
- A domain name with DNS control
- Wildcard DNS support (for VM subdomains)

## Quick Start with Ansible

The fastest way to deploy ussycode:

```bash
git clone https://github.com/mojomast/ussycode.git
cd ussycode/deploy/ansible

# Edit the inventory with your server IP
vim inventory/single-node.ini

# Run the playbook
ansible-playbook site.yml
```

### Inventory Setup

Edit `inventory/single-node.ini`:

```ini
[control_plane]
ussycode-01 ansible_host=203.0.113.10 ansible_user=root

[control_plane:vars]
ussycode_domain=dev.example.com
ussycode_acme_email=admin@example.com
ussycode_zfs_pool=vmpool
ussycode_zfs_device=/dev/sdb
```

### What Ansible Installs

The playbook runs these roles in order:

1. **common** -- System packages, UFW firewall, sysctl tuning
2. **zfs** -- ZFS pool and dataset hierarchy
3. **firecracker** -- Firecracker binary, jailer, guest kernel
4. **edge** -- nginx public edge that forwards VM traffic to internal Caddy
5. **caddy** -- Internal reverse proxy and dynamic VM route manager
6. **ussycode** -- The platform binary and systemd service
7. **monitoring** -- Prometheus node_exporter

### Run Specific Roles

```bash
# Only redeploy ussycode (after code changes)
ansible-playbook site.yml --tags ussycode

# Only update monitoring
ansible-playbook site.yml --tags monitoring
```

## One-Line Installer

For quick single-node deploys:

```bash
curl -sL https://get.ussyco.de/install | sudo bash -s -- \
  --domain dev.example.com \
  --email admin@example.com \
  --zfs-device /dev/sdb \
  --build
```

See `deploy/install-control.sh --help` for all options.

## Manual Setup

### 1. Install Dependencies

```bash
apt-get update
apt-get install -y curl wget jq git build-essential \
  zfsutils-linux linux-headers-generic ufw
```

### 2. Install Go

```bash
wget https://go.dev/dl/go1.24.4.linux-amd64.tar.gz
tar -C /usr/local -xzf go1.24.4.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

### 3. Build ussycode

```bash
git clone https://github.com/mojomast/ussycode.git
cd ussycode
go build -o /usr/local/bin/ussycode ./cmd/ussycode/
```

### 4. Set Up ZFS

```bash
# Create pool on a dedicated disk
zpool create -o ashift=12 -O compression=lz4 -O atime=off vmpool /dev/sdb

# Create dataset hierarchy
zfs create vmpool/vms
zfs create vmpool/images
zfs create vmpool/users
```

### 5. Install Firecracker

```bash
ARCH=$(uname -m)
FC_VERSION="1.7.0"
curl -sSfL -o /tmp/fc.tgz \
  "https://github.com/firecracker-microvm/firecracker/releases/download/v${FC_VERSION}/firecracker-v${FC_VERSION}-${ARCH}.tgz"
tar -xzf /tmp/fc.tgz -C /tmp/
cp /tmp/release-v${FC_VERSION}-${ARCH}/firecracker-v${FC_VERSION}-${ARCH} /usr/local/bin/firecracker
cp /tmp/release-v${FC_VERSION}-${ARCH}/jailer-v${FC_VERSION}-${ARCH} /usr/local/bin/jailer
chmod +x /usr/local/bin/firecracker /usr/local/bin/jailer

# Download guest kernel
mkdir -p /var/lib/ussycode
curl -sSfL -o /var/lib/ussycode/vmlinux \
  "https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/kernels/vmlinux-5.10.217"

# Set up jailer
mkdir -p /srv/jailer/firecracker /srv/jailer/rootfs
chmod 666 /dev/kvm
```

### 6. Install Caddy

```bash
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | \
  gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | \
  tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update && apt-get install -y caddy
```

### 7. Configure

```bash
mkdir -p /etc/ussycode /var/lib/ussycode/{images,vms,keys}

cat > /etc/ussycode/ussycode.env <<'EOF'
USSYCODE_DOMAIN=dev.example.com
USSYCODE_SSH_ADDR=:2222
USSYCODE_HTTP_ADDR=:8080
USSYCODE_DATA_DIR=/var/lib/ussycode
USSYCODE_DB_PATH=/var/lib/ussycode/ussycode.db
USSYCODE_CADDY_ADMIN=http://localhost:2019
USSYCODE_METADATA_ADDR=:8083
USSYCODE_STORAGE=zfs
USSYCODE_STORAGE_POOL=vmpool
USSYCODE_VMM=firecracker
USSYCODE_KERNEL=/var/lib/ussycode/vmlinux
USSYCODE_FIRECRACKER_BIN=/usr/local/bin/firecracker
USSYCODE_TLS_EMAIL=admin@example.com
USSYCODE_DNS_PROVIDER=cloudflare
USSYCODE_DNS_API_TOKEN=your_token_here
EOF

# Generate SSH host key
ssh-keygen -t ed25519 -f /var/lib/ussycode/ssh_host_ed25519_key -N ""
```

### 8. Create Systemd Service

```bash
cat > /etc/systemd/system/ussycode.service <<'EOF'
[Unit]
Description=ussycode - SSH dev environment platform
After=network.target caddy.service
Requires=caddy.service

[Service]
Type=simple
User=root
EnvironmentFile=/etc/ussycode/ussycode.env
ExecStart=/usr/local/bin/ussycode serve
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ussycode
```

## Configuration Reference

All configuration is via environment variables (set in `/etc/ussycode/ussycode.env`) or CLI flags.

### Core Settings

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `USSYCODE_DOMAIN` | `--domain` | `ussy.host` | Base domain for VM subdomains |
| `USSYCODE_SSH_ADDR` | `--addr` | `:2222` | SSH gateway listen address |
| `USSYCODE_SSH_HOST_KEY` | `--host-key` | `$DATA_DIR/ssh_host_ed25519_key` | SSH host key file |
| `USSYCODE_HTTP_ADDR` | `--http-addr` | `:8080` | HTTP API listen address |
| `USSYCODE_DATA_DIR` | `--data-dir` | `/var/lib/ussycode` | Root data directory |
| `USSYCODE_DB_PATH` | `--db` | `$DATA_DIR/ussycode.db` | SQLite database path |
| `USSYCODE_DEBUG` | `--debug` | (unset) | Enable debug logging |

### Caddy / Proxy

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `USSYCODE_CADDY_ADMIN` | `--caddy-api` | `http://localhost:2019` | Caddy admin API URL |
| `USSYCODE_METADATA_ADDR` | `--metadata-addr` | `:8083` | Internal metadata service listen address |
| `USSYCODE_AUTH_PROXY_ADDR` | `--auth-proxy-addr` | `:9876` | Auth proxy listen address |

### VM Settings

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `USSYCODE_VMM` | `--vmm` | `firecracker` | Hypervisor backend |
| `USSYCODE_KERNEL` | `--kernel` | `$DATA_DIR/vmlinux` | Guest kernel path |
| `USSYCODE_FIRECRACKER_BIN` | `--firecracker` | `firecracker` | Firecracker binary path |
| `USSYCODE_DEFAULT_IMAGE` | `--default-image` | `ussyuntu` | Default container image |
| `USSYCODE_DEFAULT_CPU` | `--default-cpu` | `1` | Global default vCPUs per VM (shell defaults may raise this within trust limits) |
| `USSYCODE_DEFAULT_MEM` | `--default-mem` | `512` | Global default memory (MB) per VM (shell defaults may raise this within trust limits) |
| `USSYCODE_DEFAULT_DISK` | `--default-disk` | `5` | Default disk (GB) per VM |
| `USSYCODE_MAX_VMS` | `--max-vms` | `5` | Max VMs per user |

### Storage

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `USSYCODE_STORAGE` | `--storage` | `lvm` | Storage backend: `lvm` or `zfs` |
| `USSYCODE_STORAGE_POOL` | `--storage-pool` | `ussycode` | LVM VG or ZFS pool name |

### Networking

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `USSYCODE_BRIDGE` | `--bridge` | `ussy0` | Bridge interface for VMs |
| `USSYCODE_SUBNET` | `--subnet` | `10.0.0.0/24` | CIDR subnet for VM IPs |

### TLS / DNS

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `USSYCODE_TLS_EMAIL` | `--acme-email` | (empty) | ACME email for Let's Encrypt |
| `USSYCODE_DNS_PROVIDER` | `--dns-provider` | `cloudflare` | DNS provider for challenges |
| `USSYCODE_DNS_API_TOKEN` | `--dns-api-token` | (empty) | DNS provider API token |

## DNS Setup

### Wildcard DNS with Cloudflare

ussycode needs wildcard DNS for VM subdomains:

1. **A Record**: `dev.example.com` -> `YOUR_SERVER_IP`
2. **A Record**: `*.dev.example.com` -> `YOUR_SERVER_IP`

### Cloudflare API Token

For automatic wildcard TLS certificates via DNS challenge:

1. Go to Cloudflare Dashboard > My Profile > API Tokens
2. Create a token with:
   - **Zone:DNS:Edit** permission
   - Limited to your zone
3. Set `USSYCODE_DNS_API_TOKEN` in your config

### Other DNS Providers

Caddy supports many DNS providers. See [Caddy DNS modules](https://caddyserver.com/docs/modules/) for alternatives. You'll need to rebuild Caddy with the appropriate DNS plugin.

## TLS Configuration

ussycode uses Caddy for TLS termination. Caddy automatically obtains and renews certificates from Let's Encrypt.

### Wildcard Certificates

Wildcard certs (`*.dev.example.com`) require DNS challenge verification. This is why a DNS provider API token is needed.

### Custom Domains

Users can point their own domains to their VMs using the `share cname` command. ussycode verifies ownership via a TXT record and configures Caddy to serve the custom domain.

## Upgrading

```bash
# Build new version
cd /opt/ussycode-src
git pull
go build -o /usr/local/bin/ussycode ./cmd/ussycode/

# Restart service
systemctl restart ussycode
```

## Troubleshooting

### Service won't start

```bash
journalctl -u ussycode -n 50 --no-pager
```

### KVM not available

```bash
# Check if CPU supports virtualization
egrep -c '(vmx|svm)' /proc/cpuinfo

# Load kernel module
modprobe kvm_intel  # or kvm_amd

# Check permissions
ls -la /dev/kvm
```

### ZFS pool issues

```bash
zpool status vmpool
zpool scrub vmpool
```

### Caddy certificate errors

```bash
journalctl -u caddy -n 50 --no-pager
# Verify DNS is resolving correctly
dig +short *.dev.example.com
```

### VM web apps are only visible on localhost

ussycode only proxies port `8080` from each VM by default.

Run apps like this inside the VM:

```bash
python3 -m http.server 8080 --bind 0.0.0.0
```

OpenCode is bundled into fresh `ussyuntu` VMs with a preinstalled `ussycode-web-proxy` skill that teaches it to:

- bind servers to `0.0.0.0`
- use port `8080`
- return the public proxied URL instead of `localhost`
