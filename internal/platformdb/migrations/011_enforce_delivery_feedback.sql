-- DDBOT-AI Phase 5 authoritative routing, delivery, replay and feedback
-- contracts.  These tables are additive to the Phase 4 shadow schema.  No
-- provider response, credential, cookie, token or raw source response is
-- stored here.

CREATE TABLE route_decisions (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    observed_event_id TEXT NOT NULL DEFAULT '',
    route_observation_id TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    subscription_id TEXT NOT NULL DEFAULT '',
    classifier_release_id TEXT NOT NULL DEFAULT '',
    ai_decision_id TEXT NOT NULL DEFAULT '',
    configured_mode TEXT NOT NULL
        CHECK (configured_mode IN ('off', 'shadow', 'enforce')),
    effective_mode TEXT NOT NULL
        CHECK (effective_mode IN ('off', 'shadow', 'enforce')),
    profile_id TEXT NOT NULL DEFAULT '',
    policy_digest TEXT NOT NULL DEFAULT '',
    suggested_action TEXT NOT NULL
        CHECK (suggested_action IN ('pass', 'drop')),
    effective_action TEXT NOT NULL
        CHECK (effective_action IN ('pass', 'drop')),
    reason_code TEXT NOT NULL DEFAULT '',
    hard_pass_reason TEXT NOT NULL DEFAULT '',
    enforce_approval_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    decided_at INTEGER NOT NULL
);

CREATE INDEX idx_route_decisions_event
    ON route_decisions (event_id, created_at, id);
CREATE INDEX idx_route_decisions_action
    ON route_decisions (effective_action, created_at, id);
CREATE INDEX idx_route_decisions_target
    ON route_decisions (target_id, created_at, id);

CREATE TABLE enforce_approvals (
    id TEXT PRIMARY KEY,
    classifier_release_id TEXT NOT NULL,
    policy_digest TEXT NOT NULL,
    profile_digest TEXT NOT NULL,
    readiness_evidence_json TEXT NOT NULL DEFAULT '{}',
    approved_at INTEGER NOT NULL,
    approved_by TEXT NOT NULL,
    revoked_at INTEGER,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    FOREIGN KEY (classifier_release_id) REFERENCES classifier_releases (id) ON DELETE RESTRICT
);

CREATE INDEX idx_enforce_approvals_active
    ON enforce_approvals (classifier_release_id, revoked_at, approved_at DESC);

CREATE TABLE deliveries (
    id TEXT PRIMARY KEY,
    route_decision_id TEXT NOT NULL DEFAULT '',
    event_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    connector_id TEXT NOT NULL DEFAULT '',
    target_type TEXT NOT NULL DEFAULT 'group',
    external_id TEXT NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN ('planned', 'sending', 'migration_held', 'queued',
                          'sent', 'partial', 'not_sent', 'unknown', 'rejected',
                          'expired', 'abandoned_restart', 'skipped_empty')),
    result_code TEXT NOT NULL DEFAULT '',
    remote_message_id TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0),
    replay_of_route_decision_id TEXT NOT NULL DEFAULT '',
    initiated_by TEXT NOT NULL DEFAULT 'system',
    message_snapshot_json TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    sending_at INTEGER,
    completed_at INTEGER,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_deliveries_event ON deliveries (event_id, created_at, id);
CREATE INDEX idx_deliveries_route ON deliveries (route_decision_id, created_at, id);
CREATE INDEX idx_deliveries_status ON deliveries (status, updated_at, id);

CREATE TABLE replayable_events (
    id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    route_decision_id TEXT NOT NULL UNIQUE,
    event_id TEXT NOT NULL,
    observed_event_id TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    subscription_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    snapshot_json TEXT NOT NULL,
    original_classification_ref TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    FOREIGN KEY (route_decision_id) REFERENCES route_decisions (id) ON DELETE CASCADE
);

CREATE INDEX idx_replayable_events_expiry
    ON replayable_events (expires_at, created_at, id);

CREATE TABLE feedback (
    id TEXT PRIMARY KEY,
    route_decision_id TEXT NOT NULL,
    ai_decision_id TEXT NOT NULL DEFAULT '',
    feedback_type TEXT NOT NULL
        CHECK (feedback_type IN ('correct_pass', 'correct_drop', 'false_drop',
                                'false_pass', 'uncertain_review')),
    reviewed_by TEXT NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    resolved INTEGER NOT NULL DEFAULT 1 CHECK (resolved IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (route_decision_id) REFERENCES route_decisions (id) ON DELETE CASCADE
);

CREATE INDEX idx_feedback_route ON feedback (route_decision_id, created_at, id);
CREATE INDEX idx_feedback_type ON feedback (feedback_type, resolved, created_at, id);

CREATE TABLE retention_metadata (
    domain TEXT PRIMARY KEY,
    retention_days INTEGER NOT NULL CHECK (retention_days > 0),
    updated_at INTEGER NOT NULL
);

INSERT INTO retention_metadata (domain, retention_days, updated_at)
VALUES ('feedback', 365, strftime('%s', 'now')),
       ('audit', 365, strftime('%s', 'now')),
       ('route_decisions', 90, strftime('%s', 'now')),
       ('deliveries', 90, strftime('%s', 'now')),
       ('replayable_events', 90, strftime('%s', 'now'));
