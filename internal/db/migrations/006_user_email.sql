-- +goose Up
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite doesn't support DROP COLUMN in older versions, but with modernc/sqlite
-- and newer SQLite, this works:
ALTER TABLE users DROP COLUMN email;
