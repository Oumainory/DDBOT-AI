-- DDBOT-AI P2A passive observation facts.
-- These tables are append-oriented telemetry and never participate in Legacy
-- subscription, filtering, rendering, delivery or retry decisions.

CREATE TABLE observed_events (
    id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL
        CHECK (schema_version = 1),
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

CREATE INDEX idx_observed_events_observed_at
    ON observed_events (observed_at);

CREATE INDEX idx_observed_events_platform_source
    ON observed_events (platform, source_external_id);

CREATE INDEX idx_observed_events_upstream_event
    ON observed_events (upstream_event_id);

CREATE TABLE route_observations (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    route_ordinal INTEGER NOT NULL,
    destination_kind TEXT NOT NULL,
    destination_external_id TEXT NOT NULL,
    outcome TEXT NOT NULL
        CHECK (outcome IN ('pass', 'filtered', 'skipped', 'unknown')),
    reason_code TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (event_id) REFERENCES observed_events (id) ON DELETE CASCADE
);

CREATE INDEX idx_route_observations_event
    ON route_observations (event_id, route_ordinal);

CREATE TABLE delivery_observations (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    route_observation_id TEXT NOT NULL,
    connector_kind TEXT NOT NULL,
    destination_external_id TEXT NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN ('sent', 'queued', 'not_sent', 'unknown', 'rejected')),
    result_code TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (event_id) REFERENCES observed_events (id) ON DELETE CASCADE,
    FOREIGN KEY (route_observation_id) REFERENCES route_observations (id) ON DELETE CASCADE
);

CREATE INDEX idx_delivery_observations_event
    ON delivery_observations (event_id);

CREATE INDEX idx_delivery_observations_route
    ON delivery_observations (route_observation_id);

CREATE INDEX idx_delivery_observations_observed_at
    ON delivery_observations (observed_at);
