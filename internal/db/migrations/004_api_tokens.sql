-- +goose Up
CREATE TABLE api_tokens (
    token_id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    full_token TEXT NOT NULL,
    description TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    last_used_at TEXT,
    revoked INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);

-- +goose Down
DROP TABLE IF EXISTS api_tokens;
