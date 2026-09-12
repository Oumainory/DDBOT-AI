-- DDBOT-AI Phase 4 AI Shadow durable contracts.
-- 001-009 are immutable. This migration never stores prompts, provider
-- responses, API keys, tokens, cookies, or other secret material.

CREATE TABLE ai_provider_configs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind = 'openai_compatible'),
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    credential_id TEXT,
    structured_output_mode TEXT NOT NULL DEFAULT 'json_schema'
        CHECK (structured_output_mode IN ('json_schema', 'json_object')),
    request_timeout_ms INTEGER NOT NULL DEFAULT 15000,
    max_concurrency INTEGER NOT NULL DEFAULT 2,
    queue_capacity INTEGER NOT NULL DEFAULT 64,
    pricing_currency TEXT NOT NULL DEFAULT 'USD',
    input_price_micros_per_million INTEGER NOT NULL DEFAULT 0,
    output_price_micros_per_million INTEGER NOT NULL DEFAULT 0,
    request_price_micros INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id)
);

CREATE UNIQUE INDEX idx_ai_provider_primary_enabled
    ON ai_provider_configs ((1)) WHERE enabled = 1;

CREATE TABLE classifier_releases (
    id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL,
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    prompt_digest TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    schema_digest TEXT NOT NULL,
    structured_output_mode TEXT NOT NULL,
    preprocessor_version TEXT NOT NULL,
    normalizer_version TEXT NOT NULL,
    pricing_currency TEXT NOT NULL DEFAULT 'USD',
    input_price_micros_per_million INTEGER NOT NULL DEFAULT 0,
    output_price_micros_per_million INTEGER NOT NULL DEFAULT 0,
    request_price_micros INTEGER NOT NULL DEFAULT 0,
    active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    created_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_classifier_releases_active
    ON classifier_releases ((1)) WHERE active = 1;

CREATE TABLE normalized_events (
    id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    observed_event_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    source_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    author_id TEXT NOT NULL DEFAULT '',
    author_name TEXT NOT NULL DEFAULT '',
    source_display_name TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    related_body TEXT NOT NULL DEFAULT '',
    public_url TEXT NOT NULL DEFAULT '',
    public_urls_json TEXT NOT NULL DEFAULT '[]',
    media_json TEXT NOT NULL DEFAULT '[]',
    source_event_at INTEGER,
    observed_at INTEGER NOT NULL,
    normalizer_version TEXT NOT NULL,
    preprocessor_version TEXT NOT NULL,
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    normalization_flags_json TEXT NOT NULL DEFAULT '[]',
    snapshot_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_normalized_events_observed_at
    ON normalized_events (observed_at, id);
CREATE INDEX idx_normalized_events_source
    ON normalized_events (source_id, observed_at, id);

CREATE TABLE ai_decisions (
    id TEXT PRIMARY KEY,
    normalized_event_id TEXT NOT NULL,
    classifier_release_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'queued', 'running', 'completed', 'provider_error', 'parse_error',
        'timeout', 'queue_dropped', 'skipped', 'provider_unavailable',
        'uncertain_call'
    )),
    mode_at_schedule TEXT NOT NULL CHECK (mode_at_schedule IN ('off', 'shadow', 'enforce')),
    classification_json TEXT NOT NULL DEFAULT '{}',
    suggested_action TEXT NOT NULL DEFAULT 'pass' CHECK (suggested_action IN ('pass', 'drop')),
    effective_action TEXT NOT NULL DEFAULT 'pass' CHECK (effective_action = 'pass'),
    hard_pass_reason TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER,
    output_tokens INTEGER,
    total_tokens INTEGER,
    cost_micros INTEGER,
    cost_currency TEXT NOT NULL DEFAULT '',
    latency_ms INTEGER,
    error_code TEXT NOT NULL DEFAULT '',
    scheduled_at INTEGER NOT NULL,
    call_started_at INTEGER,
    completed_at INTEGER,
    reviewed INTEGER NOT NULL DEFAULT 0 CHECK (reviewed IN (0, 1)),
    reviewed_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (normalized_event_id, classifier_release_id),
    FOREIGN KEY (normalized_event_id) REFERENCES normalized_events (id) ON DELETE CASCADE,
    FOREIGN KEY (classifier_release_id) REFERENCES classifier_releases (id) ON DELETE RESTRICT
);

CREATE INDEX idx_ai_decisions_created
    ON ai_decisions (created_at DESC, id DESC);
CREATE INDEX idx_ai_decisions_status
    ON ai_decisions (status, created_at DESC);
CREATE INDEX idx_ai_decisions_reviewed
    ON ai_decisions (reviewed, suggested_action, created_at DESC);

CREATE TABLE ai_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    default_action TEXT NOT NULL DEFAULT 'pass' CHECK (default_action IN ('inherit', 'pass', 'drop')),
    category_actions_json TEXT NOT NULL DEFAULT '{}',
    tag_actions_json TEXT NOT NULL DEFAULT '{}',
    safety_json TEXT NOT NULL DEFAULT '{}',
    builtin INTEGER NOT NULL DEFAULT 0 CHECK (builtin IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE ai_policy_overrides (
    id TEXT PRIMARY KEY,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('system', 'global', 'source', 'target', 'subscription')),
    scope_id TEXT NOT NULL DEFAULT '',
    mode TEXT CHECK (mode IN ('inherit', 'off', 'shadow', 'enforce')),
    profile_id TEXT,
    threshold REAL,
    default_action TEXT CHECK (default_action IN ('inherit', 'pass', 'drop')),
    category_actions_json TEXT NOT NULL DEFAULT '{}',
    tag_actions_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (scope_type, scope_id),
    FOREIGN KEY (profile_id) REFERENCES ai_profiles (id) ON DELETE RESTRICT
);

CREATE INDEX idx_ai_policy_scope ON ai_policy_overrides (scope_type, scope_id);

CREATE TABLE ai_route_evaluations (
    id TEXT PRIMARY KEY,
    route_observation_id TEXT NOT NULL DEFAULT '',
    -- OFF/ineligible routes have no model decision. A nullable foreign key
    -- lets us persist their safe route evaluation without manufacturing a
    -- provider call or a fake decision row.
    ai_decision_id TEXT,
    effective_mode TEXT NOT NULL CHECK (effective_mode IN ('off', 'shadow', 'enforce')),
    effective_profile_id TEXT,
    classification_suggested_action TEXT NOT NULL CHECK (classification_suggested_action IN ('pass', 'drop')),
    policy_suggested_action TEXT NOT NULL CHECK (policy_suggested_action IN ('pass', 'drop')),
    effective_action TEXT NOT NULL CHECK (effective_action = 'pass'),
    hard_pass_reason TEXT NOT NULL DEFAULT '',
    policy_provenance_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    UNIQUE (route_observation_id, ai_decision_id),
    FOREIGN KEY (ai_decision_id) REFERENCES ai_decisions (id) ON DELETE CASCADE,
    FOREIGN KEY (effective_profile_id) REFERENCES ai_profiles (id) ON DELETE SET NULL
);

CREATE INDEX idx_ai_route_evaluations_created
    ON ai_route_evaluations (created_at DESC, id DESC);

CREATE UNIQUE INDEX idx_ai_route_evaluations_off_route
    ON ai_route_evaluations (route_observation_id)
    WHERE ai_decision_id IS NULL AND route_observation_id <> '';

CREATE TABLE ai_evaluation_cases (
    id TEXT PRIMARY KEY,
    normalized_input_snapshot_json TEXT NOT NULL,
    normalized_schema_version INTEGER NOT NULL DEFAULT 1 CHECK (normalized_schema_version = 1),
    expected_importance TEXT NOT NULL DEFAULT '',
    expected_action TEXT NOT NULL DEFAULT '' CHECK (expected_action IN ('', 'pass', 'drop')),
    critical INTEGER NOT NULL DEFAULT 0 CHECK (critical IN (0, 1)),
    label_kind TEXT NOT NULL CHECK (label_kind IN ('real_reviewed', 'synthetic')),
    notes TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE ai_evaluation_runs (
    id TEXT PRIMARY KEY,
    classifier_release_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    case_ids_json TEXT NOT NULL DEFAULT '[]',
    metrics_json TEXT NOT NULL DEFAULT '{}',
    total_cost_micros INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    completed_at INTEGER,
    FOREIGN KEY (classifier_release_id) REFERENCES classifier_releases (id) ON DELETE RESTRICT
);

CREATE TABLE ai_evaluation_results (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    case_id TEXT NOT NULL,
    classification_json TEXT NOT NULL DEFAULT '{}',
    suggested_action TEXT NOT NULL DEFAULT 'pass' CHECK (suggested_action IN ('pass', 'drop')),
    expected_action TEXT NOT NULL DEFAULT '',
    comparison TEXT NOT NULL DEFAULT 'unknown',
    cost_micros INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE (run_id, case_id),
    FOREIGN KEY (run_id) REFERENCES ai_evaluation_runs (id) ON DELETE CASCADE,
    FOREIGN KEY (case_id) REFERENCES ai_evaluation_cases (id) ON DELETE RESTRICT
);

CREATE INDEX idx_ai_evaluation_results_run ON ai_evaluation_results (run_id, id);
