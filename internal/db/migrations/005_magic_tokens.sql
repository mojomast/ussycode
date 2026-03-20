-- +goose Up
CREATE TABLE magic_tokens (
    token TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    used INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_magic_tokens_user_id ON magic_tokens(user_id);
CREATE INDEX idx_magic_tokens_expires_at ON magic_tokens(expires_at);

-- +goose Down
DROP TABLE IF EXISTS magic_tokens;
