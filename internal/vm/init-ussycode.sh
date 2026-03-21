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
# Ensure pi's config directory exists and has the ussycode package configured.
# This runs as root during boot, so we set ownership at the end.

PI_CONFIG_DIR="/home/ussycode/.pi/agent"
mkdir -p "$PI_CONFIG_DIR"

# Write pi settings if not already present (don't overwrite user customizations)
if [ ! -f "$PI_CONFIG_DIR/settings.json" ]; then
    cat > "$PI_CONFIG_DIR/settings.json" << 'PIEOF'
{
  "defaultProvider": "ussyrouter",
  "defaultModel": "ussyrouter/glm-4.5-flash",
  "theme": "ussyverse",
  "enableSkillCommands": true,
  "packages": [
    "npm:@ussyverse/pi-ussycode"
  ]
}
PIEOF
    log "Wrote pi settings"
fi

chown -R ussycode:ussycode "/home/ussycode/.pi"

log "Init complete"
