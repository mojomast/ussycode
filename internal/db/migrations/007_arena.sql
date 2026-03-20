-- +goose Up

-- Arena matches for CTF/agent competitions
CREATE TABLE arena_matches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    match_id TEXT NOT NULL UNIQUE,
    scenario TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'running', 'completed', 'cancelled')),
    max_agents INTEGER NOT NULL DEFAULT 2,
    created_by INTEGER NOT NULL REFERENCES users(id),
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_arena_matches_status ON arena_matches(status);
CREATE INDEX idx_arena_matches_created_by ON arena_matches(created_by);

-- Arena match participants
CREATE TABLE arena_participants (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    match_id TEXT NOT NULL REFERENCES arena_matches(match_id),
    user_id INTEGER NOT NULL REFERENCES users(id),
    vm_id INTEGER REFERENCES vms(id),
    score INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'joined' CHECK (status IN ('joined', 'ready', 'playing', 'finished', 'disconnected')),
    joined_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(match_id, user_id)
);

CREATE INDEX idx_arena_participants_match ON arena_participants(match_id);
CREATE INDEX idx_arena_participants_user ON arena_participants(user_id);

-- ELO ratings for arena competition
CREATE TABLE arena_elo (
    user_id INTEGER PRIMARY KEY REFERENCES users(id),
    rating INTEGER NOT NULL DEFAULT 1200,
    wins INTEGER NOT NULL DEFAULT 0,
    losses INTEGER NOT NULL DEFAULT 0,
    draws INTEGER NOT NULL DEFAULT 0,
    last_match_at TEXT
);

-- +goose Down
DROP TABLE IF EXISTS arena_elo;
DROP TABLE IF EXISTS arena_participants;
DROP TABLE IF EXISTS arena_matches;
