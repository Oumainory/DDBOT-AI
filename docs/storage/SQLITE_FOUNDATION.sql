-- DDBOT-AI Phase 0 durable command/migration contracts.
-- The Core will execute these statements on the single SQLite owner
-- connection with foreign_keys=ON, WAL, and a busy timeout.

PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS idempotency_records (
    principal TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    method TEXT NOT NULL,
    normalized_path TEXT NOT NULL,
    body_sha256 TEXT NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    response_headers_json TEXT NOT NULL DEFAULT '{}',
    response_body BLOB NOT NULL DEFAULT X'',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (principal, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_expiry
    ON idempotency_records (expires_at);

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
    status TEXT NOT NULL DEFAULT 'migration_held'
        CHECK (status = 'migration_held'),
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_delivery_migration_holds_migration
    ON delivery_migration_holds (migration_id, created_at);

-- The release coordinator must delete/transition only rows owned by its
-- migration_id. `unknown` deliveries are intentionally absent from this
-- release path and are never automatically requeued.
