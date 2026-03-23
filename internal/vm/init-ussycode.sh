#!/bin/bash
# init-ussycode.sh -- runs on VM boot to configure the environment.
# Called by ussycode-init.service (systemd oneshot).
#
# Responsibilities:
#   1. Fetch SSH authorized keys from the metadata service (CRITICAL)
#   2. Configure hostname, env vars, and gateway from metadata
#   3. Mount the persistent data disk at /data
#   4. Symlink /home/ussycode/projects -> /data/projects
#
# Design: SSH key injection runs first and must not be blocked by
# data-disk failures. The data-disk section is non-fatal so that a
# dirty filesystem from a forced shutdown never prevents SSH access.

set -uo pipefail
# NOTE: set -e is intentionally omitted. Each section handles its own
# errors so that a failure in one area (e.g. data disk mount) does not
# abort critical setup (e.g. SSH keys).

METADATA_URL="http://169.254.169.254"

log() {
    echo "[ussycode-init] $*" | systemd-cat -t ussycode-init
}

# ── Fetch metadata ───────────────────────────────────────────────────
# The metadata service may not be registered immediately after the VM
# process starts, so retry a few times with short sleeps.

fetch_metadata() {
    local path="$1"
    local attempt
    for attempt in 1 2 3 4 5; do
        local result
        result=$(curl -sf --connect-timeout 2 "${METADATA_URL}${path}" 2>/dev/null) && {
            echo "$result"
            return 0
        }
        sleep 1
    done
    echo ""
    return 1
}

mask_to_prefix() {
    case "$1" in
        255.255.255.255) echo 32 ;;
        255.255.255.254) echo 31 ;;
        255.255.255.252) echo 30 ;;
        255.255.255.248) echo 29 ;;
        255.255.255.240) echo 28 ;;
        255.255.255.224) echo 27 ;;
        255.255.255.192) echo 26 ;;
        255.255.255.128) echo 25 ;;
        255.255.255.0) echo 24 ;;
        255.255.254.0) echo 23 ;;
        255.255.252.0) echo 22 ;;
        255.255.248.0) echo 21 ;;
        255.255.240.0) echo 20 ;;
        255.255.224.0) echo 19 ;;
        255.255.192.0) echo 18 ;;
        255.255.128.0) echo 17 ;;
        255.255.0.0) echo 16 ;;
        255.254.0.0) echo 15 ;;
        255.252.0.0) echo 14 ;;
        255.248.0.0) echo 13 ;;
        255.240.0.0) echo 12 ;;
        255.224.0.0) echo 11 ;;
        255.192.0.0) echo 10 ;;
        255.128.0.0) echo 9 ;;
        255.0.0.0) echo 8 ;;
        254.0.0.0) echo 7 ;;
        252.0.0.0) echo 6 ;;
        248.0.0.0) echo 5 ;;
        240.0.0.0) echo 4 ;;
        224.0.0.0) echo 3 ;;
        192.0.0.0) echo 2 ;;
        128.0.0.0) echo 1 ;;
        0.0.0.0) echo 0 ;;
        *) echo 24 ;;
    esac
}

configure_network_from_cmdline() {
    local ip_arg iface actual_iface ip_addr gateway mask prefix
    ip_arg=$(sed -n 's/.*\bip=\([^ ]*\).*/\1/p' /proc/cmdline | head -n1)
    [ -n "$ip_arg" ] || return 0

    IFS=':' read -r ip_addr _ gateway mask _ iface _ <<EOF
$ip_arg
EOF

    for _try in 1 2 3 4 5; do
        for candidate in /sys/class/net/*; do
            candidate=$(basename "$candidate")
            [ "$candidate" = "lo" ] && continue
            actual_iface="$candidate"
            break
        done
        [ -n "$actual_iface" ] && break
        sleep 1
    done

    [ -n "$actual_iface" ] || return 0
    prefix=$(mask_to_prefix "$mask")

    log "Configuring guest network from kernel cmdline on ${actual_iface}: ${ip_addr}/${prefix} via ${gateway}"
    ip link set dev "$actual_iface" up 2>/dev/null || true
    ip addr replace "$ip_addr/$prefix" dev "$actual_iface" 2>/dev/null || true
    ip route replace default via "$gateway" dev "$actual_iface" 2>/dev/null || true
}

configure_network_from_cmdline

# ── SSH authorized keys (CRITICAL -- runs first) ────────────────────
# This must succeed for the user to be able to SSH into the VM.
# It runs before the data-disk section so a mount failure cannot block it.

SSH_KEYS=$(fetch_metadata "/ssh-keys")
if [ -n "$SSH_KEYS" ]; then
    log "Installing SSH authorized keys from metadata"
    mkdir -p /home/ussycode/.ssh
    echo "$SSH_KEYS" > /home/ussycode/.ssh/authorized_keys
    chmod 700 /home/ussycode/.ssh
    chmod 600 /home/ussycode/.ssh/authorized_keys
    chown -R ussycode:ussycode /home/ussycode/.ssh
    chown ussycode:ussycode /home/ussycode
else
    log "Metadata SSH keys unavailable; leaving existing keys intact"
fi

# ── Hostname ─────────────────────────────────────────────────────────

HOSTNAME=$(fetch_metadata "/hostname")
if [ -n "$HOSTNAME" ]; then
    log "Setting hostname: $HOSTNAME"
    hostnamectl set-hostname "$HOSTNAME" 2>/dev/null || echo "$HOSTNAME" > /etc/hostname
fi

# ── Environment variables ────────────────────────────────────────────

ENV_VARS=$(fetch_metadata "/env")
if [ -n "$ENV_VARS" ]; then
    log "Writing environment variables"
    echo "$ENV_VARS" > /home/ussycode/.ussycode-env
    chown ussycode:ussycode /home/ussycode/.ussycode-env

    # Also write to /etc/environment for system-wide availability
    # (ensures non-login shells and child processes inherit the vars)
    echo "$ENV_VARS" >> /etc/environment
fi

# ── Set default gateway ─────────────────────────────────────────────

GATEWAY=$(fetch_metadata "/gateway")
if [ -n "$GATEWAY" ]; then
    ip route add default via "$GATEWAY" 2>/dev/null || true
fi

# ── Mount data disk (non-fatal) ─────────────────────────────────────
# Wrapped so that a dirty filesystem from a forced shutdown does not
# prevent the rest of the init from completing.

DATA_DISK="/dev/vdb"
DATA_MOUNT="/data"

mount_data_disk() {
    if [ ! -b "$DATA_DISK" ]; then
        log "No data disk found at $DATA_DISK, using rootfs"
        mkdir -p /home/ussycode/projects
        chown ussycode:ussycode /home/ussycode/projects
        return 0
    fi

    log "Mounting data disk $DATA_DISK -> $DATA_MOUNT"
    if ! mount -t ext4 "$DATA_DISK" "$DATA_MOUNT" 2>/dev/null; then
        log "Mount failed, attempting fsck"
        e2fsck -y "$DATA_DISK" 2>/dev/null || true
        if ! mount -t ext4 "$DATA_DISK" "$DATA_MOUNT" 2>/dev/null; then
            log "ERROR: data disk mount failed after fsck, using rootfs"
            mkdir -p /home/ussycode/projects
            chown ussycode:ussycode /home/ussycode/projects
            return 1
        fi
    fi

    # Ensure projects directory exists on data disk
    mkdir -p "$DATA_MOUNT/projects"
    chown ussycode:ussycode "$DATA_MOUNT" "$DATA_MOUNT/projects"

    # Symlink home projects to data disk
    if [ ! -L /home/ussycode/projects ]; then
        rm -rf /home/ussycode/projects
        ln -s "$DATA_MOUNT/projects" /home/ussycode/projects
        chown -h ussycode:ussycode /home/ussycode/projects
    fi
}

mount_data_disk || log "WARNING: data disk setup failed, continuing without it"

# ── Pi coding agent configuration ───────────────────────────────────
# The ussycode-specific pi extension/theme/skills are injected directly into
# ~/.pi/agent by the host before boot. Here we finish runtime configuration by
# writing a concrete models.json that points Pi at the routed ussyrouter env.

PI_CONFIG_DIR="/home/ussycode/.pi/agent"
mkdir -p "$PI_CONFIG_DIR"

if [ -n "${OPENCODE_BASE_URL:-}" ] && [ -n "${OPENCODE_API_KEY:-}" ]; then
    cat > "$PI_CONFIG_DIR/models.json" <<PIEOF
{
  "providers": {
    "ussyrouter": {
      "baseUrl": "${OPENCODE_BASE_URL}",
      "apiKey": "OPENCODE_API_KEY",
      "api": "openai-completions",
      "models": [
        { "id": "glm-5", "name": "GLM-5", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-5-turbo", "name": "GLM-5 Turbo", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.7", "name": "GLM-4.7", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.7-flash", "name": "GLM-4.7 Flash", "reasoning": false, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 8192, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.6", "name": "GLM-4.6", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.6v", "name": "GLM-4.6V", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.5", "name": "GLM-4.5", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.5-air", "name": "GLM-4.5 Air", "reasoning": false, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 8192, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.5-flash", "name": "GLM-4.5 Flash", "reasoning": false, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 8192, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } },
        { "id": "glm-4.5v", "name": "GLM-4.5V", "reasoning": true, "input": ["text", "image"], "contextWindow": 128000, "maxTokens": 16384, "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } }
      ]
    }
  }
}
PIEOF
fi

chown -R ussycode:ussycode "/home/ussycode/.pi"

log "Init complete"
