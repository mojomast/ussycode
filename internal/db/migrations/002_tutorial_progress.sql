-- +goose Up
CREATE TABLE tutorial_progress (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lesson_number INTEGER NOT NULL,
    completed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(user_id, lesson_number)
);

CREATE INDEX idx_tutorial_progress_user_id ON tutorial_progress(user_id);

-- +goose Down
DROP TABLE IF EXISTS tutorial_progress;
