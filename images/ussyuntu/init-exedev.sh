#!/bin/bash
# init-exedev.sh -- runs on VM boot to configure the environment.
# Called by exedev-init.service (systemd oneshot).
#
# Responsibilities:
#   1. Mount the persistent data disk at /data
#   2. Symlink /home/exedev/projects -> /data/projects
#   3. Fetch SSH authorized keys from the metadata service
#   4. Configure hostname from metadata

set -euo pipefail

METADATA_URL="http://169.254.169.254"

log() {
    echo "[exedev-init] $*" | systemd-cat -t exedev-init
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
    chown exedev:exedev "$DATA_MOUNT" "$DATA_MOUNT/projects"

    # Symlink home projects to data disk
    if [ ! -L /home/exedev/projects ]; then
        rm -rf /home/exedev/projects
        ln -s "$DATA_MOUNT/projects" /home/exedev/projects
        chown -h exedev:exedev /home/exedev/projects
    fi
else
    log "No data disk found at $DATA_DISK, using rootfs"
    mkdir -p /home/exedev/projects
    chown exedev:exedev /home/exedev/projects
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
    mkdir -p /home/exedev/.ssh
    echo "$SSH_KEYS" > /home/exedev/.ssh/authorized_keys
    chmod 700 /home/exedev/.ssh
    chmod 600 /home/exedev/.ssh/authorized_keys
    chown -R exedev:exedev /home/exedev/.ssh
fi

# Fetch environment variables
ENV_VARS=$(fetch_metadata "/env")
if [ -n "$ENV_VARS" ]; then
    log "Writing environment variables"
    echo "$ENV_VARS" > /home/exedev/.exedev-env
    chown exedev:exedev /home/exedev/.exedev-env
fi

# ── Set default gateway ─────────────────────────────────────────────

# The kernel boot args configure the IP, but we may need to set the
# default route manually if it wasn't done via kernel params
GATEWAY=$(fetch_metadata "/gateway")
if [ -n "$GATEWAY" ]; then
    ip route add default via "$GATEWAY" 2>/dev/null || true
fi

log "Init complete"
