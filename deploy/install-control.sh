#!/usr/bin/env bash
#
# install-control.sh — ussycode control plane installer
#
# Installs the complete ussycode control plane on a single node:
#   - System dependencies (Go, ZFS, Firecracker kernel)
#   - Caddy reverse proxy with wildcard TLS
#   - ussycode binary (built from source or downloaded)
#   - Systemd service
#   - ZFS pool for VM storage
#
# Usage:
#   curl -sL https://get.ussyco.de/install | sudo bash
#
#   Or download and run manually:
#     chmod +x install-control.sh
#     ./install-control.sh [options]
#
# Options:
#   --domain <DOMAIN>     Base domain (default: ussyco.de)
#   --email <EMAIL>       ACME email for TLS certs
#   --zfs-device <DEV>    Block device for ZFS pool (e.g. /dev/sdb)
#   --zfs-pool <NAME>     ZFS pool name (default: vmpool)
#   --skip-zfs            Skip ZFS setup (use existing pool or LVM)
#   --skip-caddy          Skip Caddy installation
#   --binary-url <URL>    Download pre-built binary from URL
#   --build               Build from source (requires Go)
#   --dry-run             Print what would be done
#   --help                Show this message

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
USSYCODE_DOMAIN="${USSYCODE_DOMAIN:-ussyco.de}"
USSYCODE_ACME_EMAIL="${USSYCODE_ACME_EMAIL:-}"
ZFS_DEVICE=""
ZFS_POOL="vmpool"
SKIP_ZFS=false
SKIP_CADDY=false
BUILD_FROM_SOURCE=false
BINARY_URL=""
DRY_RUN=false
GO_VERSION="1.25.7"
FIRECRACKER_VERSION="1.7.0"
DATA_DIR="/var/lib/ussycode"
CONFIG_DIR="/etc/ussycode"

# ---------------------------------------------------------------------------
# Colors
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
fatal() { error "$*"; exit 1; }
step()  { echo -e "\n${CYAN}${BOLD}── $* ──${NC}"; }

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --domain)
                USSYCODE_DOMAIN="$2"; shift 2 ;;
            --email)
                USSYCODE_ACME_EMAIL="$2"; shift 2 ;;
            --zfs-device)
                ZFS_DEVICE="$2"; shift 2 ;;
            --zfs-pool)
                ZFS_POOL="$2"; shift 2 ;;
            --skip-zfs)
                SKIP_ZFS=true; shift ;;
            --skip-caddy)
                SKIP_CADDY=true; shift ;;
            --binary-url)
                BINARY_URL="$2"; shift 2 ;;
            --build)
                BUILD_FROM_SOURCE=true; shift ;;
            --dry-run)
                DRY_RUN=true; shift ;;
            --help|-h)
                show_help; exit 0 ;;
            *)
                fatal "Unknown option: $1. Use --help for usage." ;;
        esac
    done
}

show_help() {
    cat <<'HELP'
ussycode control plane installer

Usage:
  install-control.sh [options]
  curl -sL https://get.ussyco.de/install | sudo bash

Options:
  --domain <DOMAIN>     Base domain for VM subdomains (default: ussyco.de)
  --email <EMAIL>       ACME email for Let's Encrypt TLS certificates
  --zfs-device <DEV>    Block device for ZFS pool (e.g. /dev/sdb)
  --zfs-pool <NAME>     ZFS pool name (default: vmpool)
  --skip-zfs            Skip ZFS pool setup
  --skip-caddy          Skip Caddy installation
  --binary-url <URL>    URL to download pre-built ussycode binary
  --build               Build ussycode from source (installs Go if needed)
  --dry-run             Print planned actions without executing
  --help                Show this help message

Environment variables:
  USSYCODE_DOMAIN       Same as --domain
  USSYCODE_ACME_EMAIL   Same as --email

Examples:
  # Minimal install (build from source, auto-detect ZFS device)
  sudo ./install-control.sh --build --domain myhost.dev --email admin@myhost.dev

  # Install with specific ZFS device
  sudo ./install-control.sh --build --zfs-device /dev/sdb --domain myhost.dev

  # Install with pre-built binary
  sudo ./install-control.sh --binary-url https://releases.ussyco.de/ussycode-latest-linux-amd64
HELP
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
check_root() {
    if [[ $EUID -ne 0 ]]; then
        fatal "This script must be run as root. Try: sudo $0"
    fi
}

check_os() {
    if [[ ! -f /etc/os-release ]]; then
        fatal "Cannot detect OS. /etc/os-release not found."
    fi

    # shellcheck source=/dev/null
    . /etc/os-release

    info "Detected OS: ${PRETTY_NAME}"

    if [[ "${ID}" != "ubuntu" ]]; then
        warn "This installer is designed for Ubuntu. Detected: ${ID}"
        warn "Proceeding anyway, but some steps may fail."
    fi

    if [[ "${ID}" == "ubuntu" && "${VERSION_ID}" != "24.04" ]]; then
        warn "Recommended: Ubuntu 24.04 LTS. Detected: ${VERSION_ID}"
    fi
}

detect_arch() {
    local arch
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)   echo "arm64" ;;
        *)               fatal "Unsupported architecture: $arch" ;;
    esac
}

check_kvm() {
    if [[ ! -e /dev/kvm ]]; then
        warn "/dev/kvm not found. Firecracker VMs require KVM."
        warn "Enable CPU virtualization in BIOS and load the kvm module:"
        warn "  modprobe kvm_intel   # (Intel) or"
        warn "  modprobe kvm_amd     # (AMD)"
    else
        ok "KVM is available"
    fi
}

check_memory() {
    local mem_gb
    mem_gb=$(awk '/MemTotal/ {printf "%.0f", $2/1024/1024}' /proc/meminfo)
    info "System memory: ${mem_gb} GB"
    if [[ "$mem_gb" -lt 4 ]]; then
        warn "At least 4 GB RAM recommended. You have ${mem_gb} GB."
    fi
}

# ---------------------------------------------------------------------------
# Install system dependencies
# ---------------------------------------------------------------------------
install_dependencies() {
    step "Installing system dependencies"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would install: curl wget jq git build-essential zfsutils-linux"
        return
    fi

    apt-get update -qq
    apt-get install -y -qq \
        curl wget jq git build-essential \
        linux-headers-generic apt-transport-https \
        ca-certificates gnupg lsb-release \
        ufw net-tools unzip

    ok "System dependencies installed"
}

# ---------------------------------------------------------------------------
# Install Go (for building from source)
# ---------------------------------------------------------------------------
install_go() {
    step "Installing Go ${GO_VERSION}"

    if command -v go &>/dev/null; then
        local current_version
        current_version="$(go version | awk '{print $3}' | sed 's/go//')"
        info "Go is already installed: v${current_version}"
        if [[ "${current_version}" == "${GO_VERSION}" ]]; then
            ok "Go version matches (${GO_VERSION})"
            return
        fi
        info "Upgrading to Go ${GO_VERSION}..."
    fi

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would install Go ${GO_VERSION}"
        return
    fi

    local arch
    arch="$(detect_arch)"
    local go_url="https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz"

    info "Downloading Go from ${go_url}..."
    curl -sSfL -o /tmp/go.tar.gz "$go_url"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm -f /tmp/go.tar.gz

    # Ensure Go is in PATH for this script and future sessions
    export PATH="/usr/local/go/bin:$PATH"
    if ! grep -q '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null; then
        cat > /etc/profile.d/go.sh <<'EOF'
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/root/go
export PATH=$PATH:$GOPATH/bin
EOF
    fi

    ok "Go $(go version | awk '{print $3}') installed"
}

# ---------------------------------------------------------------------------
# Setup ZFS
# ---------------------------------------------------------------------------
setup_zfs() {
    if [[ "$SKIP_ZFS" == "true" ]]; then
        info "Skipping ZFS setup (--skip-zfs)"
        return
    fi

    step "Setting up ZFS storage"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would install ZFS and create pool ${ZFS_POOL}"
        return
    fi

    # Install ZFS
    apt-get install -y -qq zfsutils-linux
    modprobe zfs 2>/dev/null || true
    ok "ZFS packages installed"

    # Check if pool already exists
    if zpool list "$ZFS_POOL" &>/dev/null; then
        ok "ZFS pool '${ZFS_POOL}' already exists"
    else
        # Find or use specified device
        if [[ -z "$ZFS_DEVICE" ]]; then
            info "No --zfs-device specified. Looking for available block devices..."
            auto_detect_zfs_device
        fi

        if [[ -z "$ZFS_DEVICE" ]]; then
            warn "No suitable block device found for ZFS pool."
            warn "Re-run with --zfs-device /dev/sdX to specify a device."
            warn "Or use --skip-zfs to skip ZFS setup."
            return
        fi

        if [[ ! -b "$ZFS_DEVICE" ]]; then
            fatal "ZFS device ${ZFS_DEVICE} is not a block device."
        fi

        info "Creating ZFS pool '${ZFS_POOL}' on ${ZFS_DEVICE}..."
        zpool create \
            -o ashift=12 \
            -O compression=lz4 \
            -O atime=off \
            -O mountpoint="/${ZFS_POOL}" \
            "$ZFS_POOL" "$ZFS_DEVICE"
        ok "ZFS pool '${ZFS_POOL}' created"
    fi

    # Create dataset hierarchy
    for ds in vms images users; do
        if ! zfs list "${ZFS_POOL}/${ds}" &>/dev/null; then
            zfs create -o compression=lz4 -o atime=off "${ZFS_POOL}/${ds}"
            ok "Created dataset ${ZFS_POOL}/${ds}"
        else
            ok "Dataset ${ZFS_POOL}/${ds} already exists"
        fi
    done
}

auto_detect_zfs_device() {
    # Look for unused block devices (no partitions, not mounted)
    local candidates
    candidates=$(lsblk -ndo NAME,TYPE,MOUNTPOINT 2>/dev/null | \
        awk '$2 == "disk" && $3 == "" {print "/dev/" $1}')

    if [[ -z "$candidates" ]]; then
        return
    fi

    # Filter out the boot disk
    local boot_disk
    boot_disk=$(lsblk -ndo PKNAME "$(findmnt -n -o SOURCE /)" 2>/dev/null || true)

    for dev in $candidates; do
        local devname
        devname=$(basename "$dev")
        if [[ "$devname" != "$boot_disk" ]]; then
            warn "Found available disk: ${dev}"
            warn "To use it: re-run with --zfs-device ${dev}"
            # Don't auto-select to avoid data loss
        fi
    done
}

# ---------------------------------------------------------------------------
# Install Firecracker
# ---------------------------------------------------------------------------
install_firecracker() {
    step "Installing Firecracker ${FIRECRACKER_VERSION}"

    local arch
    arch="$(uname -m)"

    if [[ -x /usr/local/bin/firecracker ]]; then
        local current
        current="$(/usr/local/bin/firecracker --version 2>/dev/null | head -1 || echo 'unknown')"
        info "Firecracker already installed: ${current}"
        if echo "$current" | grep -q "$FIRECRACKER_VERSION"; then
            ok "Version matches"
            return
        fi
    fi

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would install Firecracker ${FIRECRACKER_VERSION}"
        return
    fi

    local fc_url="https://github.com/firecracker-microvm/firecracker/releases/download/v${FIRECRACKER_VERSION}/firecracker-v${FIRECRACKER_VERSION}-${arch}.tgz"
    local tmp_dir
    tmp_dir="$(mktemp -d)"

    info "Downloading Firecracker..."
    curl -sSfL -o "${tmp_dir}/firecracker.tgz" "$fc_url"
    tar -xzf "${tmp_dir}/firecracker.tgz" -C "${tmp_dir}/"

    local release_dir="${tmp_dir}/release-v${FIRECRACKER_VERSION}-${arch}"
    install -m 0755 "${release_dir}/firecracker-v${FIRECRACKER_VERSION}-${arch}" /usr/local/bin/firecracker
    install -m 0755 "${release_dir}/jailer-v${FIRECRACKER_VERSION}-${arch}" /usr/local/bin/jailer
    rm -rf "$tmp_dir"

    ok "Firecracker installed: $(/usr/local/bin/firecracker --version 2>/dev/null | head -1)"

    # Download guest kernel
    install_guest_kernel

    # Set up jailer directories
    mkdir -p /srv/jailer/firecracker /srv/jailer/rootfs
    chmod 711 /srv/jailer

    # KVM permissions
    if [[ -e /dev/kvm ]]; then
        chmod 666 /dev/kvm
        cat > /etc/udev/rules.d/99-kvm.rules <<'EOF'
KERNEL=="kvm", GROUP="kvm", MODE="0666"
EOF
        udevadm control --reload-rules 2>/dev/null || true
    fi
}

install_guest_kernel() {
    local kernel_path="${DATA_DIR}/vmlinux"

    if [[ -f "$kernel_path" ]]; then
        ok "Guest kernel already present: ${kernel_path}"
        return
    fi

    info "Downloading guest kernel..."
    mkdir -p "$DATA_DIR"
    local kernel_url="https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/kernels/vmlinux-5.10.217"

    if curl -sSfL -o "$kernel_path" "$kernel_url" 2>/dev/null; then
        ok "Guest kernel downloaded to ${kernel_path}"
    else
        warn "Could not download guest kernel. You'll need to provide one at: ${kernel_path}"
    fi
}

# ---------------------------------------------------------------------------
# Install Caddy
# ---------------------------------------------------------------------------
install_caddy() {
    if [[ "$SKIP_CADDY" == "true" ]]; then
        info "Skipping Caddy installation (--skip-caddy)"
        return
    fi

    step "Installing Caddy"

    if command -v caddy &>/dev/null; then
        ok "Caddy is already installed: $(caddy version 2>/dev/null)"
        return
    fi

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would install Caddy"
        return
    fi

    # Add Caddy repository
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | \
        gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | \
        tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null
    apt-get update -qq
    apt-get install -y -qq caddy

    ok "Caddy installed: $(caddy version 2>/dev/null)"

    # Configure Caddyfile
    configure_caddy
}

configure_caddy() {
    info "Configuring Caddy for ${USSYCODE_DOMAIN}..."

    mkdir -p /etc/caddy

    cat > /etc/caddy/Caddyfile <<EOF
# Caddyfile — managed by ussycode installer
# ussycode dynamically adds per-VM routes via the Caddy admin API.
{
    admin localhost:2019
$([ -n "$USSYCODE_ACME_EMAIL" ] && echo "    email ${USSYCODE_ACME_EMAIL}")
}

# Wildcard cert for VM subdomains
*.${USSYCODE_DOMAIN} {
    tls {
$([ -n "$USSYCODE_ACME_EMAIL" ] && echo "        email ${USSYCODE_ACME_EMAIL}")
        dns cloudflare {env.CLOUDFLARE_API_TOKEN}
    }
    respond "VM not found. Connect via: ssh ${USSYCODE_DOMAIN}" 404
}

# Root domain
${USSYCODE_DOMAIN} {
    tls {
$([ -n "$USSYCODE_ACME_EMAIL" ] && echo "        email ${USSYCODE_ACME_EMAIL}")
        dns cloudflare {env.CLOUDFLARE_API_TOKEN}
    }
    respond "ussycode - SSH dev environments. Connect: ssh ${USSYCODE_DOMAIN}" 200
}
EOF

    systemctl enable caddy
    systemctl restart caddy
    ok "Caddy configured and started"
}

# ---------------------------------------------------------------------------
# Build / Install ussycode binary
# ---------------------------------------------------------------------------
install_ussycode_binary() {
    step "Installing ussycode"

    if [[ "$BUILD_FROM_SOURCE" == "true" ]]; then
        build_ussycode
    elif [[ -n "$BINARY_URL" ]]; then
        download_ussycode
    else
        warn "No install method specified. Use --build or --binary-url."
        warn "You can build manually:"
        warn "  git clone https://github.com/mojomast/ussycode.git"
        warn "  cd ussycode && go build -o /usr/local/bin/ussycode ./cmd/ussycode/"
        return
    fi
}

build_ussycode() {
    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would build ussycode from source"
        return
    fi

    install_go

    local src_dir="/opt/ussycode-src"

    if [[ -d "$src_dir/.git" ]]; then
        info "Updating ussycode source..."
        git -C "$src_dir" pull --ff-only 2>/dev/null || git -C "$src_dir" fetch --all
    else
        info "Cloning ussycode..."
        git clone https://github.com/mojomast/ussycode.git "$src_dir"
    fi

    info "Building ussycode binary..."
    export PATH="/usr/local/go/bin:$PATH"
    export GOPATH="/root/go"
    (cd "$src_dir" && go build -o /usr/local/bin/ussycode ./cmd/ussycode/)

    if [[ -x /usr/local/bin/ussycode ]]; then
        ok "ussycode binary built and installed"
    else
        fatal "Build failed: /usr/local/bin/ussycode not found"
    fi
}

download_ussycode() {
    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would download ussycode from: ${BINARY_URL}"
        return
    fi

    info "Downloading ussycode binary..."
    curl -sSfL -o /usr/local/bin/ussycode "$BINARY_URL"
    chmod +x /usr/local/bin/ussycode
    ok "ussycode binary downloaded and installed"
}

# ---------------------------------------------------------------------------
# Configure ussycode
# ---------------------------------------------------------------------------
configure_ussycode() {
    step "Configuring ussycode"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would configure ussycode"
        return
    fi

    # Create directories
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$DATA_DIR"/{images,vms,keys}

    # Generate environment config
    cat > "${CONFIG_DIR}/ussycode.env" <<EOF
# /etc/ussycode/ussycode.env — generated by install-control.sh
# Modify and restart: systemctl restart ussycode

# Core
USSYCODE_DOMAIN=${USSYCODE_DOMAIN}
USSYCODE_SSH_ADDR=:2222
USSYCODE_HTTP_ADDR=:8080
USSYCODE_DATA_DIR=${DATA_DIR}
USSYCODE_DB_PATH=${DATA_DIR}/ussycode.db

# Caddy integration
USSYCODE_CADDY_ADMIN=http://localhost:2019

# Storage
USSYCODE_STORAGE=zfs
USSYCODE_STORAGE_POOL=${ZFS_POOL}

# VM defaults
USSYCODE_VMM=firecracker
USSYCODE_KERNEL=${DATA_DIR}/vmlinux
USSYCODE_FIRECRACKER_BIN=/usr/local/bin/firecracker
USSYCODE_DEFAULT_IMAGE=ussyuntu
USSYCODE_DEFAULT_CPU=1
USSYCODE_DEFAULT_MEM=512
USSYCODE_DEFAULT_DISK=5
USSYCODE_MAX_VMS=5

# Networking
USSYCODE_BRIDGE=ussy0
USSYCODE_SUBNET=10.0.0.0/24

# TLS
USSYCODE_TLS_EMAIL=${USSYCODE_ACME_EMAIL}
USSYCODE_DNS_PROVIDER=cloudflare
USSYCODE_DNS_API_TOKEN=
EOF

    chmod 600 "${CONFIG_DIR}/ussycode.env"
    ok "Config written to ${CONFIG_DIR}/ussycode.env"

    # Generate SSH host key if not present
    if [[ ! -f "${DATA_DIR}/ssh_host_ed25519_key" ]]; then
        ssh-keygen -t ed25519 -f "${DATA_DIR}/ssh_host_ed25519_key" -N "" -q
        ok "SSH host key generated"
    fi
}

# ---------------------------------------------------------------------------
# Create systemd service
# ---------------------------------------------------------------------------
create_systemd_service() {
    step "Creating systemd service"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would create ussycode systemd service"
        return
    fi

    cat > /etc/systemd/system/ussycode.service <<EOF
[Unit]
Description=ussycode - SSH dev environment platform
Documentation=https://github.com/mojomast/ussycode
After=network.target caddy.service
Requires=caddy.service

[Service]
Type=simple
User=root
EnvironmentFile=${CONFIG_DIR}/ussycode.env
ExecStart=/usr/local/bin/ussycode serve
Restart=always
RestartSec=5
StartLimitIntervalSec=60
StartLimitBurst=5
LimitNOFILE=65535
LimitNPROC=32768
StandardOutput=journal
StandardError=journal
SyslogIdentifier=ussycode
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload

    if [[ -x /usr/local/bin/ussycode ]]; then
        systemctl enable ussycode
        systemctl start ussycode
        ok "ussycode service enabled and started"
    else
        ok "Systemd service created (will start when binary is installed)"
    fi
}

# ---------------------------------------------------------------------------
# Configure firewall
# ---------------------------------------------------------------------------
setup_firewall() {
    step "Configuring firewall"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would configure UFW"
        return
    fi

    if ! command -v ufw &>/dev/null; then
        warn "UFW not found, skipping firewall setup"
        return
    fi

    ufw --force reset > /dev/null 2>&1
    ufw default deny incoming > /dev/null 2>&1
    ufw default allow outgoing > /dev/null 2>&1
    ufw allow 22/tcp comment "SSH" > /dev/null 2>&1
    ufw allow 80/tcp comment "HTTP" > /dev/null 2>&1
    ufw allow 443/tcp comment "HTTPS" > /dev/null 2>&1
    ufw allow 2222/tcp comment "ussycode SSH gateway" > /dev/null 2>&1
    ufw allow 51820/udp comment "WireGuard mesh" > /dev/null 2>&1
    ufw --force enable > /dev/null 2>&1
    ok "Firewall configured (ports: 22, 80, 443, 2222, 51820/udp)"
}

# ---------------------------------------------------------------------------
# Enable IP forwarding
# ---------------------------------------------------------------------------
setup_sysctl() {
    step "Configuring kernel parameters"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would configure sysctl"
        return
    fi

    cat > /etc/sysctl.d/99-ussycode.conf <<'EOF'
# ussycode VM networking
net.ipv4.ip_forward = 1
net.ipv4.conf.all.forwarding = 1
net.bridge.bridge-nf-call-iptables = 0
fs.inotify.max_user_instances = 1024
fs.inotify.max_user_watches = 1048576
EOF

    sysctl --system > /dev/null 2>&1
    ok "Kernel parameters configured (IP forwarding enabled)"
}

# ---------------------------------------------------------------------------
# Print summary
# ---------------------------------------------------------------------------
print_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo -e "${GREEN}${BOLD}  ussycode control plane installation complete!${NC}"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "  Domain:          ${USSYCODE_DOMAIN}"
    echo "  Config:          ${CONFIG_DIR}/ussycode.env"
    echo "  Data dir:        ${DATA_DIR}"
    echo "  ZFS pool:        ${ZFS_POOL}"
    echo "  Binary:          /usr/local/bin/ussycode"
    echo ""
    echo -e "  ${BOLD}── Next Steps ──${NC}"
    echo ""
    echo "  1. DNS Setup"
    echo "     Point these DNS records to this server's IP:"
    echo "       A    ${USSYCODE_DOMAIN}       -> $(curl -s4 ifconfig.me 2>/dev/null || echo 'YOUR_IP')"
    echo "       A    *.${USSYCODE_DOMAIN}     -> $(curl -s4 ifconfig.me 2>/dev/null || echo 'YOUR_IP')"
    echo ""
    echo "  2. TLS Setup"
    echo "     For wildcard certs, add your Cloudflare API token:"
    echo "       Edit ${CONFIG_DIR}/ussycode.env"
    echo "       Set USSYCODE_DNS_API_TOKEN=your_cloudflare_token"
    echo "       systemctl restart ussycode"
    echo ""
    echo "  3. Connect"
    echo "     ssh -p 2222 ${USSYCODE_DOMAIN}"
    echo "     Your SSH key is registered on first connect."
    echo ""
    echo -e "  ${BOLD}── Useful Commands ──${NC}"
    echo ""
    echo "    systemctl status ussycode       # Service status"
    echo "    journalctl -u ussycode -f       # Follow logs"
    echo "    systemctl status caddy           # Caddy status"
    echo "    zpool status ${ZFS_POOL}              # ZFS pool health"
    echo ""
    echo "  Admin panel: https://${USSYCODE_DOMAIN}/admin"
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
    echo ""
    echo -e "${BOLD}"
    echo "  ┌──────────────────────────────────────────┐"
    echo "  │  ussycode control plane installer        │"
    echo "  │  https://github.com/mojomast/ussycode    │"
    echo "  └──────────────────────────────────────────┘"
    echo -e "${NC}"

    parse_args "$@"

    if [[ "$DRY_RUN" != "true" ]]; then
        check_root
    fi

    check_os
    check_kvm
    check_memory

    step "Installation plan"
    info "Domain:         ${USSYCODE_DOMAIN}"
    info "ACME email:     ${USSYCODE_ACME_EMAIL:-<not set>}"
    info "ZFS pool:       ${ZFS_POOL} ${SKIP_ZFS:+(skipped)}"
    info "ZFS device:     ${ZFS_DEVICE:-<auto-detect>}"
    info "Install method: $(if $BUILD_FROM_SOURCE; then echo 'build from source'; elif [[ -n "$BINARY_URL" ]]; then echo 'download binary'; else echo 'manual'; fi)"
    info "Caddy:          $(if $SKIP_CADDY; then echo 'skipped'; else echo 'install'; fi)"
    echo ""

    install_dependencies
    setup_sysctl
    setup_firewall
    setup_zfs
    install_firecracker
    install_caddy
    install_ussycode_binary
    configure_ussycode
    create_systemd_service
    print_summary
}

main "$@"
