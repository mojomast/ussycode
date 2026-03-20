-- +goose Up
ALTER TABLE users ADD COLUMN vm_limit INTEGER NOT NULL DEFAULT 3;
ALTER TABLE users ADD COLUMN cpu_limit INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN ram_limit_mb INTEGER NOT NULL DEFAULT 2048;
ALTER TABLE users ADD COLUMN disk_limit_mb INTEGER NOT NULL DEFAULT 5120;

-- +goose Down
-- SQLite doesn't support DROP COLUMN easily in older versions.
-- These columns are safe to leave in place.
