-- DDBOT-AI current v10 schema reference (non-authoritative).
-- The authoritative, immutable history is internal/platformdb/migrations/*.sql.
-- The Core will execute these statements on the single SQLite owner
-- connection with foreign_keys=ON, WAL, and a busy timeout.

PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

-- Phase 4 AI Shadow tables are intentionally not duplicated in this legacy
-- reference file. The authoritative statements live in
-- internal/platformdb/migrations/010_ai_shadow.sql; 001-009 remain immutable.

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
    created_at INTEGER NOT NULL,
    release_state TEXT NOT NULL DEFAULT 'held'
        CHECK (release_state IN ('held', 'releasing', 'released', 'unknown', 'failed')),
    release_attempts INTEGER NOT NULL DEFAULT 0,
    claimed_at INTEGER,
    released_at INTEGER,
    last_result_code TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_delivery_migration_holds_migration
    ON delivery_migration_holds (migration_id, created_at);
CREATE INDEX IF NOT EXISTS idx_delivery_migration_holds_release
    ON delivery_migration_holds (migration_id, release_state, created_at);

-- Phase 3B additive journal, explicit mapping, pairing and audit contracts.
-- See internal/platformdb/migrations/008_connector_migration.sql and
-- 009_observation_status.sql for the authoritative, independently checksummed
-- statements.
CREATE TABLE IF NOT EXISTS connector_migrations (
    migration_id TEXT PRIMARY KEY,
    old_connector_id TEXT NOT NULL,
    new_connector_id TEXT NOT NULL,
    state TEXT NOT NULL,
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
    confirmed_at INTEGER
);

CREATE TABLE IF NOT EXISTS connector_migration_mappings (
    migration_id TEXT NOT NULL,
    old_target_id TEXT NOT NULL,
    old_connector_id TEXT NOT NULL,
    old_target_type TEXT NOT NULL,
    old_external_id TEXT NOT NULL,
    new_target_id TEXT,
    new_connector_id TEXT,
    new_target_type TEXT,
    new_external_id TEXT,
    mapping_status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (migration_id, old_target_id)
);
CREATE INDEX IF NOT EXISTS idx_connector_migration_mappings_new
    ON connector_migration_mappings (migration_id, new_target_id);

CREATE TABLE IF NOT EXISTS telegram_pairing_challenges (
    challenge_id TEXT PRIMARY KEY,
    connector_id TEXT NOT NULL,
    code_sha256 TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    failure_count INTEGER NOT NULL DEFAULT 0,
    consumed_at INTEGER,
    locked_at INTEGER,
    admin_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_telegram_pairing_active
    ON telegram_pairing_challenges (connector_id, expires_at, consumed_at);

CREATE TABLE IF NOT EXISTS audit_entries (
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
CREATE INDEX IF NOT EXISTS idx_audit_entries_occurred_at
    ON audit_entries (occurred_at, id);

CREATE INDEX IF NOT EXISTS idx_connector_migrations_state
    ON connector_migrations (state, updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_connector_migrations_one_active
    ON connector_migrations ((1))
    WHERE state IN ('draft', 'preparing', 'preflight_ready', 'committing', 'recovery_required');

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

-- P1C Secret Store reference. The Master Key and plaintext are intentionally
-- absent; internal/platformdb/migrations/005_secret_store.sql is authoritative.
CREATE TABLE IF NOT EXISTS secret_store_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    sentinel_nonce BLOB NOT NULL,
    sentinel_ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credentials (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    label TEXT NOT NULL,
    source TEXT NOT NULL,
    configured INTEGER NOT NULL DEFAULT 0 CHECK (configured IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credential_secrets (
    credential_id TEXT PRIMARY KEY,
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    secret_revision INTEGER NOT NULL CHECK (secret_revision > 0),
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_credentials_type ON credentials (type);
CREATE INDEX IF NOT EXISTS idx_credentials_source ON credentials (source);
CREATE INDEX IF NOT EXISTS idx_credentials_updated ON credentials (updated_at);

-- P2A passive observation reference. The exact authoritative statements and
-- checksum live in internal/platformdb/migrations/006_observation.sql.
CREATE TABLE IF NOT EXISTS observed_events (
    id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    platform TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    source_external_id TEXT NOT NULL DEFAULT '',
    upstream_event_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    source_event_at INTEGER,
    content_fingerprint TEXT NOT NULL,
    public_snapshot_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_observed_events_observed_at ON observed_events (observed_at);
CREATE INDEX IF NOT EXISTS idx_observed_events_platform_source ON observed_events (platform, source_external_id);
CREATE INDEX IF NOT EXISTS idx_observed_events_upstream_event ON observed_events (upstream_event_id);

CREATE TABLE IF NOT EXISTS route_observations (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    route_ordinal INTEGER NOT NULL,
    destination_kind TEXT NOT NULL,
    destination_external_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('pass', 'filtered', 'skipped', 'unknown')),
    reason_code TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (event_id) REFERENCES observed_events (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_route_observations_event ON route_observations (event_id, route_ordinal);

CREATE TABLE IF NOT EXISTS delivery_observations (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    route_observation_id TEXT NOT NULL,
    connector_kind TEXT NOT NULL,
    destination_external_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('sent', 'queued', 'not_sent', 'unknown', 'rejected', 'migration_held')),
    result_code TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (event_id) REFERENCES observed_events (id) ON DELETE CASCADE,
    FOREIGN KEY (route_observation_id) REFERENCES route_observations (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_delivery_observations_event ON delivery_observations (event_id);
CREATE INDEX IF NOT EXISTS idx_delivery_observations_route ON delivery_observations (route_observation_id);
CREATE INDEX IF NOT EXISTS idx_delivery_observations_observed_at ON delivery_observations (observed_at);

-- P3A domain metadata and rebuildable Legacy subscription projection.
CREATE TABLE IF NOT EXISTS sources (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    external_id TEXT NOT NULL,
    handle TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    canonical_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('active', 'unresolved', 'unavailable')),
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (platform, external_id)
);

CREATE INDEX IF NOT EXISTS idx_sources_platform_status ON sources (platform, status);

CREATE TABLE IF NOT EXISTS connectors (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('onebot', 'satori', 'telegram')),
    name TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('main', 'extra')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('active', 'unavailable', 'disabled', 'ambiguous')),
    endpoint TEXT NOT NULL DEFAULT '',
    credential_id TEXT,
    config_json TEXT NOT NULL DEFAULT '{}',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_connectors_one_active_main
    ON connectors (role) WHERE role = 'main' AND enabled = 1;

CREATE TABLE IF NOT EXISTS targets (
    id TEXT PRIMARY KEY,
    connector_id TEXT NOT NULL,
    target_type TEXT NOT NULL CHECK (target_type IN ('group', 'channel')),
    external_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK (status IN ('resolved', 'ambiguous', 'unresolved', 'unavailable')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (connector_id, target_type, external_id),
    FOREIGN KEY (connector_id) REFERENCES connectors (id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_targets_connector_status ON targets (connector_id, status);

CREATE TABLE IF NOT EXISTS subscription_projections (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    legacy_key TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    legacy_options_snapshot_json TEXT NOT NULL DEFAULT '{}',
    projection_status TEXT NOT NULL CHECK (projection_status IN ('active', 'stale', 'drift', 'degraded', 'unresolved')),
    projected_at INTEGER NOT NULL,
    UNIQUE (source_id, target_id, legacy_key),
    FOREIGN KEY (source_id) REFERENCES sources (id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES targets (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_subscription_projections_source ON subscription_projections (source_id, projection_status);
CREATE INDEX IF NOT EXISTS idx_subscription_projections_target ON subscription_projections (target_id, projection_status);
