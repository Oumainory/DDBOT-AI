-- DDBOT-AI current v4 schema reference (non-authoritative).
-- The authoritative, immutable history is internal/platformdb/migrations/*.sql.
-- The Core will execute these statements on the single SQLite owner
-- connection with foreign_keys=ON, WAL, and a busy timeout.

PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS idempotency_records (
    principal TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    method TEXT NOT NULL,
    normalized_path TEXT NOT NULL,
    canonical_query TEXT NOT NULL DEFAULT '',
    body_sha256 TEXT NOT NULL,
    command_type TEXT NOT NULL DEFAULT 'unknown',
    execution_status TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (execution_status IN ('in_progress', 'completed')),
    status_code INTEGER NOT NULL DEFAULT 0,
    response_headers_json TEXT NOT NULL DEFAULT '{}',
    response_body BLOB NOT NULL DEFAULT X'',
    created_at INTEGER NOT NULL,
    completed_at INTEGER,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (principal, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_expiry
    ON idempotency_records (expires_at);

CREATE INDEX IF NOT EXISTS idx_idempotency_execution_status
    ON idempotency_records (execution_status);

-- This is deliberately not a general retry queue. Rows exist only while a
-- Connector Migration owns the delivery. Every value required after a crash
-- is persisted in SQLite; no Notify, Messenger, renderer, or template object
-- is referenced here.
CREATE TABLE IF NOT EXISTS delivery_migration_holds (
    delivery_id TEXT PRIMARY KEY,
    migration_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    route_snapshot_json TEXT NOT NULL,
    logical_target_json TEXT NOT NULL,
    message_snapshot_json TEXT NOT NULL,
    payload_schema_version INTEGER NOT NULL,
    route_decision_id TEXT
        CHECK (route_decision_id IS NULL OR length(trim(route_decision_id)) > 0),
    status TEXT NOT NULL DEFAULT 'migration_held'
        CHECK (status = 'migration_held'),
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_delivery_migration_holds_migration
    ON delivery_migration_holds (migration_id, created_at);

CREATE TABLE IF NOT EXISTS administrators (
    id TEXT PRIMARY KEY,
    singleton INTEGER NOT NULL DEFAULT 1 CHECK (singleton = 1),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    password_changed_at INTEGER NOT NULL,
    disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    UNIQUE (singleton)
);

CREATE TABLE IF NOT EXISTS setup_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    completed INTEGER NOT NULL DEFAULT 0 CHECK (completed IN (0, 1)),
    completed_at INTEGER
);

CREATE TABLE IF NOT EXISTS setup_tokens (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER
);

CREATE TABLE IF NOT EXISTS sessions (
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

CREATE INDEX IF NOT EXISTS idx_setup_tokens_expiry ON setup_tokens (expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_admin ON sessions (admin_id);
CREATE INDEX IF NOT EXISTS idx_sessions_revoked ON sessions (revoked_at);

-- New holds must carry the independent route decision identity. Existing
-- legacy rows upgraded from v1/v2 may remain NULL and are intentionally not
-- backfilled with a fabricated identity.
CREATE TRIGGER IF NOT EXISTS trg_delivery_migration_holds_route_decision_insert
BEFORE INSERT ON delivery_migration_holds
FOR EACH ROW
WHEN NEW.route_decision_id IS NULL
  OR length(trim(NEW.route_decision_id)) = 0
BEGIN
    SELECT RAISE(ABORT, 'migration_held route_decision_id is required');
END;

CREATE TRIGGER IF NOT EXISTS trg_delivery_migration_holds_route_decision_update
BEFORE UPDATE OF route_decision_id ON delivery_migration_holds
FOR EACH ROW
WHEN NEW.route_decision_id IS NULL
  OR length(trim(NEW.route_decision_id)) = 0
BEGIN
    SELECT RAISE(ABORT, 'migration_held route_decision_id cannot be cleared');
END;

-- The release coordinator must delete/transition only rows owned by its
-- migration_id. `unknown` deliveries are intentionally absent from this
-- release path and are never automatically requeued.
