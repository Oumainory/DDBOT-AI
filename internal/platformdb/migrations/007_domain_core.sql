-- DDBOT-AI Phase 3A domain metadata and Legacy subscription projection.
-- BuntDB remains the Legacy subscription authority; these tables are
-- rebuildable metadata/projection only.

CREATE TABLE sources (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    external_id TEXT NOT NULL,
    handle TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    canonical_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL
        CHECK (status IN ('active', 'unresolved', 'unavailable')),
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (platform, external_id)
);

CREATE INDEX idx_sources_platform_status
    ON sources (platform, status);

CREATE TABLE connectors (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL
        CHECK (kind IN ('onebot', 'satori', 'telegram')),
    name TEXT NOT NULL,
    role TEXT NOT NULL
        CHECK (role IN ('main', 'extra')),
    enabled INTEGER NOT NULL DEFAULT 1
        CHECK (enabled IN (0, 1)),
    status TEXT NOT NULL
        CHECK (status IN ('active', 'unavailable', 'disabled', 'ambiguous')),
    endpoint TEXT NOT NULL DEFAULT '',
    credential_id TEXT,
    config_json TEXT NOT NULL DEFAULT '{}',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id)
);

CREATE INDEX idx_connectors_role_enabled
    ON connectors (role, enabled);

-- The running topology may have at most one enabled main publisher. Extra
-- connectors (including Telegram) remain independently selectable.
CREATE UNIQUE INDEX idx_connectors_one_active_main
    ON connectors (role)
    WHERE role = 'main' AND enabled = 1;

CREATE TABLE targets (
    id TEXT PRIMARY KEY,
    connector_id TEXT NOT NULL,
    target_type TEXT NOT NULL
        CHECK (target_type IN ('group', 'channel')),
    external_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL
        CHECK (status IN ('resolved', 'ambiguous', 'unresolved', 'unavailable')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (connector_id, target_type, external_id),
    FOREIGN KEY (connector_id) REFERENCES connectors (id) ON DELETE RESTRICT
);

CREATE INDEX idx_targets_connector_status
    ON targets (connector_id, status);

CREATE TABLE subscription_projections (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    legacy_key TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1
        CHECK (enabled IN (0, 1)),
    legacy_options_snapshot_json TEXT NOT NULL DEFAULT '{}',
    projection_status TEXT NOT NULL
        CHECK (projection_status IN ('active', 'stale', 'drift', 'degraded', 'unresolved')),
    projected_at INTEGER NOT NULL,
    UNIQUE (source_id, target_id, legacy_key),
    FOREIGN KEY (source_id) REFERENCES sources (id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES targets (id) ON DELETE CASCADE
);

CREATE INDEX idx_subscription_projections_source
    ON subscription_projections (source_id, projection_status);

CREATE INDEX idx_subscription_projections_target
    ON subscription_projections (target_id, projection_status);
