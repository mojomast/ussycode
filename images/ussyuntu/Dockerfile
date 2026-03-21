# ussyuntu -- base VM image for ussycode
# Ubuntu-based dev environment with pre-installed tools.
# Built as an OCI container, then extracted to an ext4 rootfs
# for Firecracker microVMs.
#
# Build: docker build -t ussyuntu:latest images/ussyuntu/
# Push:  docker push ghcr.io/mojomast/ussyuntu:latest

FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive

# Core system packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    # Init + services
    systemd systemd-sysv dbus \
    # SSH server
    openssh-server \
    # Networking
    iproute2 iputils-ping dnsutils curl wget ca-certificates \
    # Core CLI tools
    git jq tmux vim nano htop tree less file unzip zip \
    # Build essentials
    build-essential pkg-config \
    # Python
    python3 python3-pip python3-venv \
    # Node.js (via nodesource in next step)
    # SQLite
    sqlite3 libsqlite3-dev \
    # Misc
    sudo locales man-db \
    && rm -rf /var/lib/apt/lists/*

# Set locale
RUN locale-gen en_US.UTF-8
ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# Install Node.js 22 LTS
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install OpenCode CLI for in-VM agent workflows
RUN npm install -g opencode-ai

# Install pi coding agent and ussycode package
RUN npm install -g @mariozechner/pi-coding-agent @ussyverse/pi-ussycode

# Install Go (latest stable)
ARG GO_VERSION=1.24.4
RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

# Install GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# ──────────────────────────────────────────────────────────────────────
# Create the default user: "ussycode" with passwordless sudo
# ──────────────────────────────────────────────────────────────────────

RUN useradd -m -s /bin/bash -G sudo ussycode \
    && echo "ussycode ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/ussycode \
    && chmod 0440 /etc/sudoers.d/ussycode

# Setup ussycode user's environment
RUN mkdir -p /home/ussycode/.ssh /home/ussycode/projects \
    && chown -R ussycode:ussycode /home/ussycode

# Go environment for ussycode user
RUN echo 'export GOPATH=/home/ussycode/go' >> /home/ussycode/.bashrc \
    && echo 'export PATH=$GOPATH/bin:/usr/local/go/bin:$PATH' >> /home/ussycode/.bashrc \
    && mkdir -p /home/ussycode/go/bin \
    && chown -R ussycode:ussycode /home/ussycode/go

# ──────────────────────────────────────────────────────────────────────
# SSH server configuration
# ──────────────────────────────────────────────────────────────────────

RUN mkdir -p /run/sshd \
    && ssh-keygen -A

# SSH config: no password auth, allow pubkey, allow root login for host management
COPY sshd_config /etc/ssh/sshd_config

# Provide the VM launch helper with a shell that can see common modules
RUN printf '%s\n' '#!/bin/sh' 'modprobe virtio_blk 2>/dev/null || true' 'modprobe virtio_net 2>/dev/null || true' 'modprobe virtio_mmio 2>/dev/null || true' 'exec /usr/local/bin/init-ussycode.sh "$@"' > /usr/local/bin/ussycode-init-wrapper \
    && chmod +x /usr/local/bin/ussycode-init-wrapper

# ──────────────────────────────────────────────────────────────────────
# Systemd setup for Firecracker boot
# ──────────────────────────────────────────────────────────────────────

# Mask services that don't work / aren't needed in a microVM
RUN systemctl mask \
    systemd-resolved.service \
    systemd-timesyncd.service \
    systemd-networkd-wait-online.service \
    multipathd.service \
    ModemManager.service \
    networkd-dispatcher.service \
    systemd-udevd.service

# Enable SSH
RUN systemctl enable ssh.service

# Custom init script that mounts data disk and sets up networking
COPY init-ussycode.sh /usr/local/bin/init-ussycode.sh
RUN chmod +x /usr/local/bin/init-ussycode.sh

# Systemd service for our init script
COPY ussycode-init.service /etc/systemd/system/ussycode-init.service
RUN systemctl enable ussycode-init.service

# ──────────────────────────────────────────────────────────────────────
# Welcome message
# ──────────────────────────────────────────────────────────────────────

COPY motd /etc/motd

# Default bash profile for ussycode
COPY bash_profile /home/ussycode/.bash_profile
RUN chown ussycode:ussycode /home/ussycode/.bash_profile

# ──────────────────────────────────────────────────────────────────────
# Data disk mount point
# ──────────────────────────────────────────────────────────────────────

RUN mkdir -p /data && chown ussycode:ussycode /data

# ──────────────────────────────────────────────────────────────────────
# Cleanup
# ──────────────────────────────────────────────────────────────────────

RUN apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

# Firecracker boots with /sbin/init (systemd)
CMD ["/sbin/init"]
