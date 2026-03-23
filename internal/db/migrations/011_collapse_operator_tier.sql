-- +goose Up
UPDATE users
SET trust_level = 'admin'
WHERE trust_level = 'operator';

-- Rebuild users table to remove the old operator CHECK constraint.
CREATE TABLE users_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    handle      TEXT    NOT NULL UNIQUE,
    trust_level TEXT    NOT NULL DEFAULT 'newbie' CHECK (trust_level IN ('newbie', 'citizen', 'admin')),
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    email       TEXT    NOT NULL DEFAULT '',
    vm_limit    INTEGER NOT NULL DEFAULT 1,
    cpu_limit   INTEGER NOT NULL DEFAULT 1,
    ram_limit_mb INTEGER NOT NULL DEFAULT 2048,
    disk_limit_mb INTEGER NOT NULL DEFAULT 5120
);

INSERT INTO users_new (id, handle, trust_level, created_at, updated_at, email, vm_limit, cpu_limit, ram_limit_mb, disk_limit_mb)
SELECT id, handle,
       CASE WHEN trust_level = 'operator' THEN 'admin' ELSE trust_level END,
       created_at, updated_at, COALESCE(email, ''),
       CASE WHEN trust_level = 'admin' OR trust_level = 'operator' THEN -1
            WHEN trust_level = 'citizen' THEN 2
            ELSE 1 END,
       CASE WHEN trust_level = 'admin' OR trust_level = 'operator' THEN -1
            WHEN trust_level = 'citizen' THEN 2
            ELSE 1 END,
       CASE WHEN trust_level = 'admin' OR trust_level = 'operator' THEN -1
            WHEN trust_level = 'citizen' THEN 4096
            ELSE 2048 END,
       CASE WHEN trust_level = 'admin' OR trust_level = 'operator' THEN -1
            WHEN trust_level = 'citizen' THEN 25600
            ELSE 5120 END
FROM users;

DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

CREATE INDEX idx_users_handle ON users(handle);

-- +goose Down
CREATE TABLE users_old (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    handle      TEXT    NOT NULL UNIQUE,
    trust_level TEXT    NOT NULL DEFAULT 'newbie' CHECK (trust_level IN ('newbie', 'citizen', 'operator', 'admin')),
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    email       TEXT    NOT NULL DEFAULT '',
    vm_limit    INTEGER NOT NULL DEFAULT 3,
    cpu_limit   INTEGER NOT NULL DEFAULT 1,
    ram_limit_mb INTEGER NOT NULL DEFAULT 2048,
    disk_limit_mb INTEGER NOT NULL DEFAULT 5120
);

INSERT INTO users_old (id, handle, trust_level, created_at, updated_at, email, vm_limit, cpu_limit, ram_limit_mb, disk_limit_mb)
SELECT id, handle, trust_level, created_at, updated_at, COALESCE(email, ''), vm_limit, cpu_limit, ram_limit_mb, disk_limit_mb
FROM users;

DROP TABLE users;
ALTER TABLE users_old RENAME TO users;

CREATE INDEX idx_users_handle ON users(handle);
