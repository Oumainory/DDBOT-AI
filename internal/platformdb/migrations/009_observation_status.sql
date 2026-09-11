-- DDBOT-AI Phase 3B extends the passive delivery observation vocabulary with
-- the explicit controlled-maintenance outcome used by connector migration.
-- Migration 006 is immutable; recreate only this table so existing rows and
-- indexes remain intact while the durable CHECK constraint admits
-- migration_held. This is telemetry only and never becomes a retry queue.

DROP INDEX IF EXISTS idx_delivery_observations_event;
DROP INDEX IF EXISTS idx_delivery_observations_route;
DROP INDEX IF EXISTS idx_delivery_observations_observed_at;

ALTER TABLE delivery_observations RENAME TO delivery_observations_v6;

CREATE TABLE delivery_observations (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    route_observation_id TEXT NOT NULL,
    connector_kind TEXT NOT NULL,
    destination_external_id TEXT NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN ('sent', 'queued', 'not_sent', 'unknown', 'rejected', 'migration_held')),
    result_code TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (event_id) REFERENCES observed_events (id) ON DELETE CASCADE,
    FOREIGN KEY (route_observation_id) REFERENCES route_observations (id) ON DELETE CASCADE
);

INSERT INTO delivery_observations
    (id, event_id, route_observation_id, connector_kind,
     destination_external_id, status, result_code, observed_at, created_at)
SELECT id, event_id, route_observation_id, connector_kind,
       destination_external_id, status, result_code, observed_at, created_at
FROM delivery_observations_v6;

DROP TABLE delivery_observations_v6;

CREATE INDEX idx_delivery_observations_event
    ON delivery_observations (event_id);

CREATE INDEX idx_delivery_observations_route
    ON delivery_observations (route_observation_id);

CREATE INDEX idx_delivery_observations_observed_at
    ON delivery_observations (observed_at);
