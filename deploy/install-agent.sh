#!/usr/bin/env bash
#
# install-agent.sh — Installs the ussyverse-agent on a Linux node.
#
# Usage:
#   curl -sSL https://install.ussyverse.dev/agent.sh | bash -s -- --token <TOKEN> --control <URL>
#
#   Or download and run manually:
#     chmod +x install-agent.sh
#     ./install-agent.sh --token <TOKEN> --control <URL>
#
# Options:
#   --token   <TOKEN>   Join token from the control plane (required)
#   --control <URL>     Control plane URL (required)
#   --version <VER>     Agent version to install (default: latest)
#   --data-dir <PATH>   Agent data directory (default: /var/lib/ussyverse-agent)
#   --skip-kvm-check    Skip KVM availability check
#   --dry-run           Print what would be done without making changes

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
AGENT_VERSION="${AGENT_VERSION:-latest}"
DATA_DIR="/var/lib/ussyverse-agent"
BASE_URL="https://releases.ussyverse.dev"
SKIP_KVM_CHECK=false
DRY_RUN=false
JOIN_TOKEN=""
CONTROL_URL=""

# ---------------------------------------------------------------------------
# Colors
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
fatal() { error "$*"; exit 1; }

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --token)
                JOIN_TOKEN="$2"; shift 2 ;;
            --control)
                CONTROL_URL="$2"; shift 2 ;;
            --version)
                AGENT_VERSION="$2"; shift 2 ;;
            --data-dir)
                DATA_DIR="$2"; shift 2 ;;
            --skip-kvm-check)
                SKIP_KVM_CHECK=true; shift ;;
            --dry-run)
                DRY_RUN=true; shift ;;
            --help|-h)
                show_help; exit 0 ;;
            *)
                fatal "Unknown option: $1" ;;
        esac
    done

    if [[ -z "$JOIN_TOKEN" ]]; then
        fatal "--token is required. Get one from: ussyverse-ctl token create"
    fi
    if [[ -z "$CONTROL_URL" ]]; then
        fatal "--control is required. Example: --control https://cp.example.com"
    fi
}

show_help() {
    cat <<'HELP'
ussyverse-agent installer

Usage:
  install-agent.sh --token <TOKEN> --control <URL> [options]

Options:
  --token   <TOKEN>   Join token from the control plane (required)
  --control <URL>     Control plane URL (required)
  --version <VER>     Agent version to install (default: latest)
  --data-dir <PATH>   Agent data directory (default: /var/lib/ussyverse-agent)
  --skip-kvm-check    Skip KVM availability check
  --dry-run           Print what would be done without making changes
  --help              Show this help message

Environment variables:
  AGENT_VERSION       Same as --version
HELP
}

# ---------------------------------------------------------------------------
# System detection
# ---------------------------------------------------------------------------
detect_os() {
    local os
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$os" in
        linux)  echo "linux" ;;
        *)      fatal "Unsupported OS: $os. Only Linux is supported." ;;
    esac
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

detect_init() {
    if command -v systemctl &>/dev/null && [[ -d /run/systemd/system ]]; then
        echo "systemd"
    else
        echo "unknown"
    fi
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
check_root() {
    if [[ $EUID -ne 0 ]]; then
        fatal "This script must be run as root. Try: sudo $0 $*"
    fi
}

check_kvm() {
    if [[ "$SKIP_KVM_CHECK" == "true" ]]; then
        warn "Skipping KVM check (--skip-kvm-check)"
        return
    fi

    if [[ ! -e /dev/kvm ]]; then
        warn "/dev/kvm not found. VMs will not be available on this node."
        warn "To enable KVM:"
        warn "  1. Ensure CPU virtualization (VT-x/AMD-V) is enabled in BIOS"
        warn "  2. Load the kvm module: modprobe kvm_intel (or kvm_amd)"
        warn "  3. Or skip this check with --skip-kvm-check"
        echo ""
    else
        ok "KVM is available"
    fi
}

check_dependencies() {
    local missing=()
    for cmd in curl tar sha256sum; do
        if ! command -v "$cmd" &>/dev/null; then
            missing+=("$cmd")
        fi
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        fatal "Missing required commands: ${missing[*]}"
    fi
    ok "Required dependencies found"
}

detect_distro() {
    if [[ -f /etc/os-release ]]; then
        # shellcheck source=/dev/null
        . /etc/os-release
        info "Distribution: ${PRETTY_NAME:-${ID} ${VERSION_ID}}"
    else
        warn "Could not detect Linux distribution"
    fi
}

# ---------------------------------------------------------------------------
# Download and install
# ---------------------------------------------------------------------------
download_agent() {
    local os="$1" arch="$2"
    local url="${BASE_URL}/ussyverse-agent-${AGENT_VERSION}-${os}-${arch}.tar.gz"
    local checksum_url="${BASE_URL}/ussyverse-agent-${AGENT_VERSION}-${os}-${arch}.tar.gz.sha256"
    local tmp_dir

    tmp_dir="$(mktemp -d)"
    trap 'rm -rf "$tmp_dir"' EXIT

    info "Downloading ussyverse-agent ${AGENT_VERSION} for ${os}/${arch}..."
    info "URL: ${url}"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would download from: $url"
        return
    fi

    if ! curl -sSfL -o "${tmp_dir}/agent.tar.gz" "$url" 2>/dev/null; then
        warn "Download failed (release server not yet available)."
        warn "To install manually, build the agent binary and copy it to /usr/local/bin/ussyverse-agent"
        warn "  go build -o /usr/local/bin/ussyverse-agent ./cmd/ussyverse-agent/"
        return 1
    fi

    # Checksum verification
    verify_checksum "${tmp_dir}/agent.tar.gz" "$checksum_url"

    tar -xzf "${tmp_dir}/agent.tar.gz" -C "${tmp_dir}/"
    install -m 0755 "${tmp_dir}/ussyverse-agent" /usr/local/bin/ussyverse-agent
    ok "Installed ussyverse-agent to /usr/local/bin/ussyverse-agent"
}

# ---------------------------------------------------------------------------
# Checksum verification
# ---------------------------------------------------------------------------
verify_checksum() {
    local file="$1"
    local checksum_url="$2"
    local tmp_checksum

    tmp_checksum="$(mktemp)"

    info "Verifying download checksum..."
    if curl -sSfL -o "$tmp_checksum" "$checksum_url" 2>/dev/null; then
        local expected actual
        expected="$(awk '{print $1}' "$tmp_checksum")"
        actual="$(sha256sum "$file" | awk '{print $1}')"

        if [[ "$expected" == "$actual" ]]; then
            ok "Checksum verified: ${actual:0:16}..."
        else
            rm -f "$tmp_checksum"
            fatal "Checksum mismatch! Expected: ${expected:0:16}... Got: ${actual:0:16}..."
        fi
    else
        warn "Checksum file not available (${checksum_url}). Skipping verification."
        warn "For production use, verify the binary manually:"
        warn "  sha256sum /usr/local/bin/ussyverse-agent"
    fi

    rm -f "$tmp_checksum"
}

# ---------------------------------------------------------------------------
# Create systemd service
# ---------------------------------------------------------------------------
create_systemd_service() {
    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would create systemd service"
        return
    fi

    cat > /etc/systemd/system/ussyverse-agent.service <<EOF
[Unit]
Description=Ussyverse Node Agent
Documentation=https://github.com/mojomast/ussycode
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ussyverse-agent run --data-dir ${DATA_DIR}
Restart=always
RestartSec=5
StartLimitIntervalSec=60
StartLimitBurst=5

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=${DATA_DIR}
PrivateTmp=yes

# Resource limits
LimitNOFILE=65536
LimitNPROC=4096

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=ussyverse-agent

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    ok "Created systemd service: ussyverse-agent"
}

# ---------------------------------------------------------------------------
# Setup data directory
# ---------------------------------------------------------------------------
setup_data_dir() {
    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would create ${DATA_DIR}"
        return
    fi

    mkdir -p "${DATA_DIR}"
    chmod 700 "${DATA_DIR}"
    ok "Created data directory: ${DATA_DIR}"
}

# ---------------------------------------------------------------------------
# Join the cluster
# ---------------------------------------------------------------------------
join_cluster() {
    if [[ "$DRY_RUN" == "true" ]]; then
        info "[DRY RUN] Would join cluster at ${CONTROL_URL}"
        return
    fi

    if ! command -v ussyverse-agent &>/dev/null; then
        warn "Agent binary not found. Skipping join step."
        warn "After installing the binary, run:"
        warn "  ussyverse-agent join --token '${JOIN_TOKEN}' --control '${CONTROL_URL}'"
        return
    fi

    info "Joining cluster..."
    ussyverse-agent join \
        --token "${JOIN_TOKEN}" \
        --control "${CONTROL_URL}" \
        --data-dir "${DATA_DIR}"

    ok "Successfully joined cluster"
}

# ---------------------------------------------------------------------------
# Print summary
# ---------------------------------------------------------------------------
print_summary() {
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    info "Installation complete!"
    echo ""
    echo "  Agent binary:    /usr/local/bin/ussyverse-agent"
    echo "  Data directory:  ${DATA_DIR}"
    echo "  Control plane:   ${CONTROL_URL}"
    echo ""
    echo "  ── Join Instructions ──────────────────────────────────────────"
    echo ""
    echo "  Your node has been configured to join the ussyverse pool."
    echo "  If the join step succeeded, your node is now registered with"
    echo "  the control plane and will start accepting VM workloads."
    echo ""
    echo "  If join failed, you can retry manually:"
    echo "    ussyverse-agent join --token '${JOIN_TOKEN}' --control '${CONTROL_URL}'"
    echo ""
    echo "  ── Useful Commands ─────────────────────────────────────────────"
    echo ""
    echo "    ussyverse-agent status            # Show agent status"
    echo "    ussyverse-agent version            # Print version"
    echo "    systemctl status ussyverse-agent   # Service status"
    echo "    journalctl -u ussyverse-agent -f   # Follow logs"
    echo ""

    if [[ "$(detect_init)" == "systemd" ]]; then
        echo "  ── Start the Agent ──────────────────────────────────────────"
        echo ""
        echo "    systemctl enable --now ussyverse-agent"
        echo ""
    fi

    echo "  ── Monitoring ──────────────────────────────────────────────"
    echo ""
    echo "  Your node's health is reported to the control plane via"
    echo "  periodic heartbeats. Check your node in the admin panel:"
    echo "    ${CONTROL_URL}/admin/nodes"
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
    echo ""
    echo "  ┌─────────────────────────────────────┐"
    echo "  │    ussyverse-agent installer         │"
    echo "  └─────────────────────────────────────┘"
    echo ""

    parse_args "$@"

    local os arch init_system
    os="$(detect_os)"
    arch="$(detect_arch)"
    init_system="$(detect_init)"

    info "Detected: ${os}/${arch}, init: ${init_system}"
    detect_distro

    if [[ "$DRY_RUN" != "true" ]]; then
        check_root
    fi
    check_dependencies
    check_kvm

    setup_data_dir
    download_agent "$os" "$arch" || true

    if [[ "$init_system" == "systemd" ]]; then
        create_systemd_service
    else
        warn "Non-systemd init detected. You'll need to manage the agent process manually."
    fi

    join_cluster
    print_summary
}

main "$@"
