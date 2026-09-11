-- DDBOT-AI Phase 3B durable connector migration, pairing, and audit contracts.
-- 001-007 are immutable. This migration contains only additive tables/columns;
-- no credential or access token is stored in any of these records.

CREATE TABLE connector_migrations (
    migration_id TEXT PRIMARY KEY,
    old_connector_id TEXT NOT NULL,
    new_connector_id TEXT NOT NULL,
    state TEXT NOT NULL
        CHECK (state IN ('draft', 'preparing', 'preflight_ready', 'committing', 'completed', 'failed', 'rolled_back', 'recovery_required')),
    started_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER,
    old_topology_snapshot_json TEXT NOT NULL DEFAULT '{}',
    new_topology_snapshot_json TEXT NOT NULL DEFAULT '{}',
    mapping_snapshot_json TEXT NOT NULL DEFAULT '[]',
    affected_target_ids_json TEXT NOT NULL DEFAULT '[]',
    rollback_expires_at INTEGER,
    error_code TEXT NOT NULL DEFAULT '',
    progress_marker TEXT NOT NULL DEFAULT 'draft',
    confirmed_at INTEGER,
    FOREIGN KEY (old_connector_id) REFERENCES connectors (id),
    FOREIGN KEY (new_connector_id) REFERENCES connectors (id)
);

CREATE INDEX idx_connector_migrations_state
    ON connector_migrations (state, updated_at);

CREATE UNIQUE INDEX idx_connector_migrations_one_active
    ON connector_migrations ((1))
    WHERE state IN ('draft', 'preparing', 'preflight_ready', 'committing', 'recovery_required');

CREATE TABLE connector_migration_mappings (
    migration_id TEXT NOT NULL,
    old_target_id TEXT NOT NULL,
    old_connector_id TEXT NOT NULL,
    old_target_type TEXT NOT NULL,
    old_external_id TEXT NOT NULL,
    new_target_id TEXT,
    new_connector_id TEXT,
    new_target_type TEXT,
    new_external_id TEXT,
    mapping_status TEXT NOT NULL
        CHECK (mapping_status IN ('required', 'confirmed', 'unavailable', 'ambiguous')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (migration_id, old_target_id),
    FOREIGN KEY (migration_id) REFERENCES connector_migrations (migration_id) ON DELETE CASCADE,
    FOREIGN KEY (old_target_id) REFERENCES targets (id),
    FOREIGN KEY (new_target_id) REFERENCES targets (id)
);

CREATE INDEX idx_connector_migration_mappings_new
    ON connector_migration_mappings (migration_id, new_target_id);

ALTER TABLE delivery_migration_holds
    ADD COLUMN release_state TEXT NOT NULL DEFAULT 'held'
        CHECK (release_state IN ('held', 'releasing', 'released', 'unknown', 'failed'));

ALTER TABLE delivery_migration_holds
    ADD COLUMN release_attempts INTEGER NOT NULL DEFAULT 0;

ALTER TABLE delivery_migration_holds
    ADD COLUMN claimed_at INTEGER;

ALTER TABLE delivery_migration_holds
    ADD COLUMN released_at INTEGER;

ALTER TABLE delivery_migration_holds
    ADD COLUMN last_result_code TEXT NOT NULL DEFAULT '';

ALTER TABLE delivery_migration_holds
    ADD COLUMN payload_json TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_delivery_migration_holds_release
    ON delivery_migration_holds (migration_id, release_state, created_at);

CREATE TABLE telegram_pairing_challenges (
    challenge_id TEXT PRIMARY KEY,
    connector_id TEXT NOT NULL,
    code_sha256 TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    failure_count INTEGER NOT NULL DEFAULT 0,
    consumed_at INTEGER,
    locked_at INTEGER,
    admin_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (connector_id) REFERENCES connectors (id) ON DELETE CASCADE
);

CREATE INDEX idx_telegram_pairing_active
    ON telegram_pairing_challenges (connector_id, expires_at, consumed_at);

CREATE TABLE audit_entries (
    id TEXT PRIMARY KEY,
    occurred_at INTEGER NOT NULL,
    principal_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    prev_hash TEXT NOT NULL DEFAULT '',
    entry_hash TEXT NOT NULL
);

CREATE INDEX idx_audit_entries_occurred_at
    ON audit_entries (occurred_at, id);
