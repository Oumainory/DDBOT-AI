-- DDBOT-AI Phase 1 P1A.1 contract closure.
-- 001_core.sql is immutable. All corrections to the published v1 schema
-- arrive as an ordered, independently checksummed migration.

ALTER TABLE idempotency_records
    ADD COLUMN canonical_query TEXT NOT NULL DEFAULT '';

ALTER TABLE idempotency_records
    ADD COLUMN command_type TEXT NOT NULL DEFAULT 'unknown';

ALTER TABLE idempotency_records
    ADD COLUMN execution_status TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (execution_status IN ('in_progress', 'completed'));

ALTER TABLE idempotency_records
    ADD COLUMN completed_at INTEGER;

-- A v1 row used status_code = 0 as its implicit in-progress marker. Preserve
-- that meaning while making the state explicit. Historical completion time is
-- intentionally unknown and remains NULL.
UPDATE idempotency_records
SET execution_status = CASE
    WHEN status_code = 0 THEN 'in_progress'
    ELSE 'completed'
END;

CREATE INDEX IF NOT EXISTS idx_idempotency_execution_status
    ON idempotency_records (execution_status);

-- Old development rows may not have a route decision identity. They remain
-- nullable rather than receiving a fabricated value; new durable holds must
-- provide route_decision_id through the persistence contract.
ALTER TABLE delivery_migration_holds
    ADD COLUMN route_decision_id TEXT
        CHECK (route_decision_id IS NULL OR length(trim(route_decision_id)) > 0);
