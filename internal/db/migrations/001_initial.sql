-- +goose Up

-- Users identified by SSH public key
CREATE TABLE users (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    handle      TEXT    NOT NULL UNIQUE,
    trust_level TEXT    NOT NULL DEFAULT 'newbie' CHECK (trust_level IN ('newbie', 'citizen', 'operator', 'admin')),
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- SSH public keys (one user can have multiple keys)
CREATE TABLE ssh_keys (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key     TEXT    NOT NULL, -- authorized_keys format
    fingerprint    TEXT    NOT NULL, -- SHA256 fingerprint for lookup
    comment        TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(fingerprint)
);

CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys(fingerprint);
CREATE INDEX idx_ssh_keys_user_id ON ssh_keys(user_id);

-- Virtual machines
CREATE TABLE vms (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'stopped' CHECK (status IN ('creating', 'running', 'stopped', 'error')),
    image       TEXT    NOT NULL DEFAULT 'ussyuntu',
    vcpu        INTEGER NOT NULL DEFAULT 1,
    memory_mb   INTEGER NOT NULL DEFAULT 512,
    disk_gb     INTEGER NOT NULL DEFAULT 2,
    tap_device  TEXT,           -- tap interface name
    ip_address  TEXT,           -- assigned IP on bridge network
    mac_address TEXT,           -- assigned MAC
    pid         INTEGER,        -- QEMU/Firecracker PID when running
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(user_id, name)
);

CREATE INDEX idx_vms_user_id ON vms(user_id);
CREATE INDEX idx_vms_ip_address ON vms(ip_address);
CREATE INDEX idx_vms_status ON vms(status);

-- VM tags
CREATE TABLE vm_tags (
    vm_id   INTEGER NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    tag     TEXT    NOT NULL,
    PRIMARY KEY (vm_id, tag)
);

-- Sharing: who can access which VM
CREATE TABLE shares (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    vm_id       INTEGER NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    shared_with INTEGER REFERENCES users(id) ON DELETE CASCADE, -- NULL if link-based
    link_token  TEXT,           -- non-NULL for link-based sharing
    is_public   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_shares_vm_id ON shares(vm_id);
CREATE INDEX idx_shares_link_token ON shares(link_token);

-- Short-lived API tokens (opaque handle -> signed token)
CREATE TABLE tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    handle     TEXT    NOT NULL UNIQUE, -- short opaque handle
    token_data TEXT    NOT NULL,        -- full signed token
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_tokens_handle ON tokens(handle);
CREATE INDEX idx_tokens_expires_at ON tokens(expires_at);

-- +goose Down
DROP TABLE IF EXISTS tokens;
DROP TABLE IF EXISTS shares;
DROP TABLE IF EXISTS vm_tags;
DROP TABLE IF EXISTS vms;
DROP TABLE IF EXISTS ssh_keys;
DROP TABLE IF EXISTS users;
