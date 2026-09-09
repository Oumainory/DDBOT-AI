-- DDBOT-AI Phase 1 P1B administrator bootstrap and authentication schema.
-- Earlier migrations are immutable. This migration owns only the single-admin
-- bootstrap, server-side sessions, and their durable state.

CREATE TABLE administrators (
    id TEXT PRIMARY KEY,
    singleton INTEGER NOT NULL DEFAULT 1
        CHECK (singleton = 1),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    password_changed_at INTEGER NOT NULL,
    disabled INTEGER NOT NULL DEFAULT 0
        CHECK (disabled IN (0, 1)),
    UNIQUE (singleton)
);

CREATE TABLE setup_state (
    singleton INTEGER PRIMARY KEY
        CHECK (singleton = 1),
    completed INTEGER NOT NULL DEFAULT 0
        CHECK (completed IN (0, 1)),
    completed_at INTEGER
);

INSERT INTO setup_state (singleton, completed)
VALUES (1, 0);

CREATE TABLE setup_tokens (
    singleton INTEGER PRIMARY KEY
        CHECK (singleton = 1),
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER
);

CREATE INDEX idx_setup_tokens_expiry
    ON setup_tokens (expires_at);

CREATE TABLE sessions (
    session_id_hash TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER,
    csrf_secret TEXT NOT NULL,
    user_agent_hash TEXT,
    FOREIGN KEY (admin_id) REFERENCES administrators (id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_sessions_id_hash
    ON sessions (session_id_hash);

CREATE INDEX idx_sessions_expiry
    ON sessions (expires_at);

CREATE INDEX idx_sessions_admin
    ON sessions (admin_id);

CREATE INDEX idx_sessions_revoked
    ON sessions (revoked_at);
