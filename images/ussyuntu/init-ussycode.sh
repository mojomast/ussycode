#!/bin/bash
# init-ussycode.sh -- runs on VM boot to configure the environment.
# Called by ussycode-init.service (systemd oneshot).
#
# Responsibilities:
#   1. Mount the persistent data disk at /data
#   2. Symlink /home/ussycode/projects -> /data/projects
#   3. Fetch SSH authorized keys from the metadata service
#   4. Configure hostname from metadata

set -euo pipefail

METADATA_URL="http://169.254.169.254"

log() {
    echo "[ussycode-init] $*" | systemd-cat -t ussycode-init
}

# ── Mount data disk ──────────────────────────────────────────────────

DATA_DISK="/dev/vdb"
DATA_MOUNT="/data"

if [ -b "$DATA_DISK" ]; then
    log "Mounting data disk $DATA_DISK -> $DATA_MOUNT"
    mount -t ext4 "$DATA_DISK" "$DATA_MOUNT" 2>/dev/null || {
        log "Mount failed, attempting fsck"
        e2fsck -y "$DATA_DISK" || true
        mount -t ext4 "$DATA_DISK" "$DATA_MOUNT"
    }

    # Ensure projects directory exists on data disk
    mkdir -p "$DATA_MOUNT/projects"
    chown ussycode:ussycode "$DATA_MOUNT" "$DATA_MOUNT/projects"

    # Symlink home projects to data disk
    if [ ! -L /home/ussycode/projects ]; then
        rm -rf /home/ussycode/projects
        ln -s "$DATA_MOUNT/projects" /home/ussycode/projects
        chown -h ussycode:ussycode /home/ussycode/projects
    fi
else
    log "No data disk found at $DATA_DISK, using rootfs"
    mkdir -p /home/ussycode/projects
    chown ussycode:ussycode /home/ussycode/projects
fi

# ── Fetch metadata ───────────────────────────────────────────────────

# Try to reach the metadata service (may not be available in all setups)
fetch_metadata() {
    local path="$1"
    curl -sf --connect-timeout 2 "${METADATA_URL}${path}" 2>/dev/null || echo ""
}

# Set hostname from metadata
HOSTNAME=$(fetch_metadata "/hostname")
if [ -n "$HOSTNAME" ]; then
    log "Setting hostname: $HOSTNAME"
    hostnamectl set-hostname "$HOSTNAME" 2>/dev/null || echo "$HOSTNAME" > /etc/hostname
fi

# Fetch SSH authorized keys
SSH_KEYS=$(fetch_metadata "/ssh-keys")
if [ -n "$SSH_KEYS" ]; then
    log "Installing SSH authorized keys from metadata"
    mkdir -p /home/ussycode/.ssh
    echo "$SSH_KEYS" > /home/ussycode/.ssh/authorized_keys
    chmod 700 /home/ussycode/.ssh
    chmod 600 /home/ussycode/.ssh/authorized_keys
    chown -R ussycode:ussycode /home/ussycode/.ssh
fi

# Fetch environment variables
ENV_VARS=$(fetch_metadata "/env")
if [ -n "$ENV_VARS" ]; then
    log "Writing environment variables"
    echo "$ENV_VARS" > /home/ussycode/.ussycode-env
    chown ussycode:ussycode /home/ussycode/.ussycode-env
fi

# ── Set default gateway ─────────────────────────────────────────────

# The kernel boot args configure the IP, but we may need to set the
# default route manually if it wasn't done via kernel params
GATEWAY=$(fetch_metadata "/gateway")
if [ -n "$GATEWAY" ]; then
    ip route add default via "$GATEWAY" 2>/dev/null || true
fi

log "Init complete"
