-- DDBOT-AI Phase 1 P1A.2 final contract guards.
-- 001_core.sql and 002_contracts.sql are immutable. This migration only
-- constrains future writes; it deliberately preserves legacy NULL rows.

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
