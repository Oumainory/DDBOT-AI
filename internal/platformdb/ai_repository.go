package platformdb

// AIRepository is the durable boundary for Phase 4. It stores only normalized
// public snapshots, semantic classifications and sanitized accounting data;
// provider credentials remain in Secret Store and no raw prompt/response is
// persisted.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/classifier"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/internal/policy"
)

var (
	ErrAIUnavailable           = errors.New("platformdb: ai unavailable")
	ErrAIProviderNotFound      = errors.New("platformdb: ai provider not found")
	ErrAIProviderInvalid       = errors.New("platformdb: invalid ai provider")
	ErrReleaseNotFound         = errors.New("platformdb: classifier release not found")
	ErrNormalizedEventNotFound = errors.New("platformdb: normalized event not found")
	ErrDecisionNotFound        = errors.New("platformdb: ai decision not found")
	ErrDecisionClaimed         = errors.New("platformdb: ai decision already claimed")
	ErrDecisionStarted         = errors.New("platformdb: ai decision call already started")
	ErrAIProfileNotFound       = errors.New("platformdb: ai profile not found")
	ErrAIProfileInUse          = errors.New("platformdb: ai profile in use")
	ErrAIProfileBuiltin        = errors.New("platformdb: built-in ai profile is immutable")
	ErrAIPolicyNotFound        = errors.New("platformdb: ai policy not found")
	ErrRouteEvaluationNotFound = errors.New("platformdb: ai route evaluation not found")
	ErrEvaluationRunActive     = errors.New("platformdb: evaluation run active")
	ErrEvaluationNotFound      = errors.New("platformdb: evaluation not found")
	ErrEvaluationInvalid       = errors.New("platformdb: invalid evaluation case")
)

type AIProviderConfigRecord struct {
	ID                          string    `json:"id"`
	Kind                        string    `json:"kind"`
	BaseURL                     string    `json:"base_url"`
	Model                       string    `json:"model"`
	CredentialID                string    `json:"credential_id,omitempty"`
	StructuredOutputMode        string    `json:"structured_output_mode"`
	RequestTimeoutMS            int64     `json:"request_timeout_ms"`
	MaxConcurrency              int       `json:"max_concurrency"`
	QueueCapacity               int       `json:"queue_capacity"`
	PricingCurrency             string    `json:"pricing_currency"`
	InputPriceMicrosPerMillion  int64     `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion int64     `json:"output_price_micros_per_million"`
	RequestPriceMicros          int64     `json:"request_price_micros"`
	Enabled                     bool      `json:"enabled"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}

type NormalizedEventRecord struct {
	Event domain.NormalizedEvent `json:"event"`
}

type RouteEvaluationRecord struct {
	ID                            string            `json:"id"`
	RouteObservationID            string            `json:"route_observation_id,omitempty"`
	DecisionID                    string            `json:"ai_decision_id"`
	EffectiveMode                 string            `json:"effective_mode"`
	EffectiveProfileID            string            `json:"effective_profile_id,omitempty"`
	ClassificationSuggestedAction string            `json:"classification_suggested_action"`
	PolicySuggestedAction         string            `json:"policy_suggested_action"`
	EffectiveAction               string            `json:"effective_action"`
	HardPassReason                string            `json:"hard_pass_reason,omitempty"`
	PolicyProvenance              map[string]string `json:"policy_provenance,omitempty"`
	CreatedAt                     time.Time         `json:"created_at"`
}

type AIPolicyOverrideRecord struct {
	ID              string                            `json:"id"`
	ScopeType       string                            `json:"scope_type"`
	ScopeID         string                            `json:"scope_id"`
	Mode            policy.Mode                       `json:"mode,omitempty"`
	ProfileID       string                            `json:"profile_id,omitempty"`
	Threshold       *float64                          `json:"threshold,omitempty"`
	DefaultAction   policy.Action                     `json:"default_action,omitempty"`
	CategoryActions map[domain.Category]policy.Action `json:"category_actions,omitempty"`
	TagActions      map[string]policy.Action          `json:"tag_actions,omitempty"`
	CreatedAt       time.Time                         `json:"created_at"`
	UpdatedAt       time.Time                         `json:"updated_at"`
}

type EvaluationCaseRecord struct {
	ID                      string          `json:"id"`
	NormalizedInputSnapshot json.RawMessage `json:"normalized_input_snapshot"`
	ExpectedImportance      string          `json:"expected_importance,omitempty"`
	ExpectedAction          string          `json:"expected_action,omitempty"`
	Critical                bool            `json:"critical"`
	LabelKind               string          `json:"label_kind"`
	Notes                   string          `json:"notes,omitempty"`
	Source                  string          `json:"source,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type EvaluationRunRecord struct {
	ID                  string         `json:"id"`
	ClassifierReleaseID string         `json:"classifier_release_id"`
	Status              string         `json:"status"`
	CaseIDs             []string       `json:"case_ids,omitempty"`
	Metrics             map[string]any `json:"metrics,omitempty"`
	TotalCostMicros     int64          `json:"total_cost_micros"`
	LatencyMS           int64          `json:"latency_ms"`
	CreatedAt           time.Time      `json:"created_at"`
	CompletedAt         *time.Time     `json:"completed_at,omitempty"`
}

type EvaluationResultRecord struct {
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	CaseID          string          `json:"case_id"`
	Classification  json.RawMessage `json:"classification,omitempty"`
	SuggestedAction string          `json:"suggested_action"`
	ExpectedAction  string          `json:"expected_action,omitempty"`
	Comparison      string          `json:"comparison"`
	CostMicros      int64           `json:"cost_micros"`
	LatencyMS       int64           `json:"latency_ms"`
	CreatedAt       time.Time       `json:"created_at"`
}

type AIRepository struct{ store *Store }

func NewAIRepository(store *Store) *AIRepository {
	if store == nil {
		return nil
	}
	return &AIRepository{store: store}
}
func (r *AIRepository) require() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrAIUnavailable
	}
	return nil
}
func aiContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func aiTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func (r *AIRepository) SaveProvider(ctx context.Context, value AIProviderConfigRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	value.Kind = normalizeProviderKind(value.Kind)
	if strings.TrimSpace(value.ID) == "" || value.Kind != "openai_compatible" || strings.TrimSpace(value.BaseURL) == "" || strings.TrimSpace(value.Model) == "" {
		return ErrAIProviderInvalid
	}
	if err := classifier.ValidateBaseURL(value.BaseURL); err != nil {
		return ErrAIProviderInvalid
	}
	value.BaseURL = classifier.CanonicalBaseURL(value.BaseURL)
	if value.StructuredOutputMode == "json-schema" {
		value.StructuredOutputMode = "json_schema"
	}
	if value.StructuredOutputMode == "" {
		value.StructuredOutputMode = "json_schema"
	}
	if value.RequestTimeoutMS <= 0 {
		value.RequestTimeoutMS = 15000
	}
	if value.MaxConcurrency <= 0 {
		value.MaxConcurrency = 2
	}
	if value.QueueCapacity <= 0 {
		value.QueueCapacity = 64
	}
	if value.PricingCurrency == "" {
		value.PricingCurrency = "USD"
	}
	if value.InputPriceMicrosPerMillion < 0 || value.OutputPriceMicrosPerMillion < 0 || value.RequestPriceMicros < 0 {
		return ErrAIProviderInvalid
	}
	created, updated := aiTime(value.CreatedAt), aiTime(value.UpdatedAt)
	ctx = aiContext(ctx)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if value.Enabled {
		if _, err = tx.ExecContext(ctx, "UPDATE ai_provider_configs SET enabled=0 WHERE id <> ?", value.ID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_provider_configs
(id,kind,base_url,model,credential_id,structured_output_mode,request_timeout_ms,max_concurrency,queue_capacity,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,enabled,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,base_url=excluded.base_url,model=excluded.model,credential_id=excluded.credential_id,structured_output_mode=excluded.structured_output_mode,request_timeout_ms=excluded.request_timeout_ms,max_concurrency=excluded.max_concurrency,queue_capacity=excluded.queue_capacity,pricing_currency=excluded.pricing_currency,input_price_micros_per_million=excluded.input_price_micros_per_million,output_price_micros_per_million=excluded.output_price_micros_per_million,request_price_micros=excluded.request_price_micros,enabled=excluded.enabled,updated_at=excluded.updated_at`,
		value.ID, value.Kind, value.BaseURL, value.Model, nullIfEmpty(value.CredentialID), value.StructuredOutputMode, value.RequestTimeoutMS, value.MaxConcurrency, value.QueueCapacity, value.PricingCurrency, value.InputPriceMicrosPerMillion, value.OutputPriceMicrosPerMillion, value.RequestPriceMicros, boolInt(value.Enabled), created.Unix(), updated.Unix())
	if err != nil {
		return fmt.Errorf("platformdb: save ai provider: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *AIRepository) Provider(ctx context.Context) (AIProviderConfigRecord, error) {
	if err := r.require(); err != nil {
		return AIProviderConfigRecord{}, err
	}
	return r.scanProvider(r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,kind,base_url,model,COALESCE(credential_id,''),structured_output_mode,request_timeout_ms,max_concurrency,queue_capacity,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,enabled,created_at,updated_at FROM ai_provider_configs WHERE enabled=1 ORDER BY updated_at DESC LIMIT 1`))
}

// Providers returns public provider configuration metadata. Credential
// material is intentionally represented only by its reference; callers must
// use Secret Store to resolve it for a transport client.
func (r *AIRepository) Providers(ctx context.Context) ([]AIProviderConfigRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,kind,base_url,model,COALESCE(credential_id,''),structured_output_mode,request_timeout_ms,max_concurrency,queue_capacity,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,enabled,created_at,updated_at FROM ai_provider_configs ORDER BY enabled DESC,updated_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]AIProviderConfigRecord, 0)
	for rows.Next() {
		value, scanErr := r.scanProvider(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (r *AIRepository) ProviderByID(ctx context.Context, id string) (AIProviderConfigRecord, error) {
	if err := r.require(); err != nil {
		return AIProviderConfigRecord{}, err
	}
	return r.scanProvider(r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,kind,base_url,model,COALESCE(credential_id,''),structured_output_mode,request_timeout_ms,max_concurrency,queue_capacity,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,enabled,created_at,updated_at FROM ai_provider_configs WHERE id=?`, strings.TrimSpace(id)))
}
func (r *AIRepository) scanProvider(row interface{ Scan(...any) error }) (AIProviderConfigRecord, error) {
	var value AIProviderConfigRecord
	var enabled, created, updated int64
	err := row.Scan(&value.ID, &value.Kind, &value.BaseURL, &value.Model, &value.CredentialID, &value.StructuredOutputMode, &value.RequestTimeoutMS, &value.MaxConcurrency, &value.QueueCapacity, &value.PricingCurrency, &value.InputPriceMicrosPerMillion, &value.OutputPriceMicrosPerMillion, &value.RequestPriceMicros, &enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return AIProviderConfigRecord{}, ErrAIProviderNotFound
	}
	if err != nil {
		return AIProviderConfigRecord{}, err
	}
	value.Enabled = enabled != 0
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	return value, nil
}

func (r *AIRepository) SaveRelease(ctx context.Context, value classifier.Release) error {
	if err := r.require(); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	ctx = aiContext(ctx)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if value.Active {
		if _, err = tx.ExecContext(ctx, `UPDATE classifier_releases SET active=0`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO classifier_releases
(id,fingerprint,provider_type,base_url,model,prompt_version,prompt_digest,schema_version,schema_digest,structured_output_mode,preprocessor_version,normalizer_version,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,active,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET active=excluded.active,pricing_currency=excluded.pricing_currency,input_price_micros_per_million=excluded.input_price_micros_per_million,output_price_micros_per_million=excluded.output_price_micros_per_million,request_price_micros=excluded.request_price_micros`,
		value.ID, value.Fingerprint, value.ProviderType, value.BaseURL, value.Model, value.PromptVersion, value.PromptDigest, value.SchemaVersion, value.SchemaDigest, normalizeOutputMode(value.StructuredOutputMode), value.PreprocessorVersion, value.NormalizerVersion, value.PricingCurrency, value.InputPriceMicrosPerMillion, value.OutputPriceMicrosPerMillion, value.RequestPriceMicros, boolInt(value.Active), value.CreatedAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("platformdb: save classifier release: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
func (r *AIRepository) ActivateRelease(ctx context.Context, id string) error {
	if err := r.require(); err != nil {
		return err
	}
	ctx = aiContext(ctx)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, "UPDATE classifier_releases SET active=0"); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE classifier_releases SET active=1 WHERE id=?", strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrReleaseNotFound
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// DeactivateReleases clears the active classifier release when the provider is
// explicitly disabled. It is a small state transition, not a release queue or
// workflow; a later provider save can create/activate a new release.
func (r *AIRepository) DeactivateReleases(ctx context.Context) error {
	if err := r.require(); err != nil {
		return err
	}
	_, err := r.store.db.ExecContext(aiContext(ctx), "UPDATE classifier_releases SET active=0 WHERE active=1")
	return err
}
func (r *AIRepository) ActiveRelease(ctx context.Context) (classifier.Release, error) {
	if err := r.require(); err != nil {
		return classifier.Release{}, err
	}
	return r.scanRelease(r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,fingerprint,provider_type,base_url,model,prompt_version,prompt_digest,schema_version,schema_digest,structured_output_mode,preprocessor_version,normalizer_version,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,active,created_at FROM classifier_releases WHERE active=1 LIMIT 1`))
}
func (r *AIRepository) Release(ctx context.Context, id string) (classifier.Release, error) {
	if err := r.require(); err != nil {
		return classifier.Release{}, err
	}
	return r.scanRelease(r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,fingerprint,provider_type,base_url,model,prompt_version,prompt_digest,schema_version,schema_digest,structured_output_mode,preprocessor_version,normalizer_version,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,active,created_at FROM classifier_releases WHERE id=?`, id))
}

func (r *AIRepository) Releases(ctx context.Context) ([]classifier.Release, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,fingerprint,provider_type,base_url,model,prompt_version,prompt_digest,schema_version,schema_digest,structured_output_mode,preprocessor_version,normalizer_version,pricing_currency,input_price_micros_per_million,output_price_micros_per_million,request_price_micros,active,created_at FROM classifier_releases ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]classifier.Release, 0)
	for rows.Next() {
		value, scanErr := r.scanRelease(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (r *AIRepository) scanRelease(row interface{ Scan(...any) error }) (classifier.Release, error) {
	var v classifier.Release
	var active, created int64
	err := row.Scan(&v.ID, &v.Fingerprint, &v.ProviderType, &v.BaseURL, &v.Model, &v.PromptVersion, &v.PromptDigest, &v.SchemaVersion, &v.SchemaDigest, &v.StructuredOutputMode, &v.PreprocessorVersion, &v.NormalizerVersion, &v.PricingCurrency, &v.InputPriceMicrosPerMillion, &v.OutputPriceMicrosPerMillion, &v.RequestPriceMicros, &active, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return classifier.Release{}, ErrReleaseNotFound
	}
	if err != nil {
		return classifier.Release{}, err
	}
	v.Active = active != 0
	v.CreatedAt = time.Unix(created, 0).UTC()
	return v, nil
}

func (r *AIRepository) PutNormalizedEvent(ctx context.Context, event domain.NormalizedEvent) error {
	if err := r.require(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if event.ID == "" {
		event.ID = event.NormalizedEventID
	}
	if event.NormalizedEventID == "" {
		event.NormalizedEventID = event.ID
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = 1
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = event.ObservedAt
		if event.CreatedAt.IsZero() {
			event.CreatedAt = aiTime(time.Time{})
		}
	}
	snapshot, _ := json.Marshal(event)
	var sourceAt any
	if event.SourceEventAt != nil {
		sourceAt = event.SourceEventAt.UTC().Unix()
	}
	_, err := r.store.db.ExecContext(aiContext(ctx), `INSERT INTO normalized_events (id,schema_version,observed_event_id,platform,source_id,external_id,event_type,author_id,author_name,source_display_name,title,body,related_body,public_url,public_urls_json,media_json,source_event_at,observed_at,normalizer_version,preprocessor_version,truncated,normalization_flags_json,snapshot_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, event.ID, event.SchemaVersion, event.ObservedEventID, string(event.Platform), event.SourceID, event.ExternalID, string(event.EventType), event.AuthorID, event.AuthorName, event.SourceDisplayName, event.Title, event.Body, event.RelatedBody, event.URL, mustJSONArray(event.PublicURLs), mustJSON(event.Media), sourceAt, event.ObservedAt.UTC().Unix(), event.NormalizerVersion, event.PreprocessorVersion, boolInt(event.Truncated), mustJSONArray(event.NormalizationFlags), string(snapshot), event.CreatedAt.UTC().Unix())
	return err
}
func (r *AIRepository) NormalizedEvent(ctx context.Context, id string) (domain.NormalizedEvent, error) {
	if err := r.require(); err != nil {
		return domain.NormalizedEvent{}, err
	}
	var raw string
	err := r.store.db.QueryRowContext(aiContext(ctx), "SELECT snapshot_json FROM normalized_events WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NormalizedEvent{}, ErrNormalizedEventNotFound
	}
	if err != nil {
		return domain.NormalizedEvent{}, err
	}
	var event domain.NormalizedEvent
	if err = json.Unmarshal([]byte(raw), &event); err != nil {
		return domain.NormalizedEvent{}, err
	}
	return event, nil
}

// EnsureDecisionClaim creates a durable queued decision and atomically claims
// it for one worker. A row with call_started_at is never claimable again.
func (r *AIRepository) EnsureDecisionClaim(ctx context.Context, eventID, releaseID, mode string, scheduledAt time.Time) (classifier.Decision, bool, error) {
	if err := r.require(); err != nil {
		return classifier.Decision{}, false, err
	}
	eventID = strings.TrimSpace(eventID)
	releaseID = strings.TrimSpace(releaseID)
	if eventID == "" || releaseID == "" {
		return classifier.Decision{}, false, ErrDecisionNotFound
	}
	ctx = aiContext(ctx)
	stamp := aiTime(scheduledAt)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return classifier.Decision{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	id, err := domain.NewID()
	if err != nil {
		return classifier.Decision{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO ai_decisions (id,normalized_event_id,classifier_release_id,status,mode_at_schedule,effective_action,scheduled_at,created_at) VALUES (?,?,?,?,?,?,?,?)`, id, eventID, releaseID, "queued", normalizeMode(mode), "pass", stamp.Unix(), stamp.Unix())
	if err != nil {
		return classifier.Decision{}, false, err
	}
	var currentID, status, modeAt, classification, suggested, effective, hard, provider, model, currency, errorCode string
	var inTok, outTok, total, cost, latency, scheduled, started, completed, created sql.NullInt64
	var reviewed sql.NullInt64
	var reviewedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id,status,mode_at_schedule,classification_json,suggested_action,effective_action,hard_pass_reason,provider,model,input_tokens,output_tokens,total_tokens,cost_micros,cost_currency,latency_ms,error_code,scheduled_at,call_started_at,completed_at,created_at,reviewed,reviewed_at FROM ai_decisions WHERE normalized_event_id=? AND classifier_release_id=?`, eventID, releaseID).Scan(&currentID, &status, &modeAt, &classification, &suggested, &effective, &hard, &provider, &model, &inTok, &outTok, &total, &cost, &currency, &latency, &errorCode, &scheduled, &started, &completed, &created, &reviewed, &reviewedAt)
	if err != nil {
		return classifier.Decision{}, false, err
	}
	claimable := status == string(classifier.StatusQueued) && !started.Valid
	if claimable {
		result, updateErr := tx.ExecContext(ctx, "UPDATE ai_decisions SET status='running' WHERE id=? AND status='queued' AND call_started_at IS NULL", currentID)
		if updateErr != nil {
			return classifier.Decision{}, false, updateErr
		}
		n, _ := result.RowsAffected()
		claimable = n == 1
		status = string(classifier.StatusRunning)
	}
	if err = tx.Commit(); err != nil {
		return classifier.Decision{}, false, err
	}
	committed = true
	return scanDecisionValues(currentID, eventID, releaseID, status, modeAt, classification, suggested, effective, hard, provider, model, currency, errorCode, inTok, outTok, total, cost, latency, scheduled, started, completed, created, reviewed, reviewedAt), claimable, nil
}

// RecordQueueDropped durably records that a bounded Shadow queue rejected a
// job before any provider call. Existing terminal/running decisions are left
// untouched so a duplicate schedule cannot rewrite its outcome.
func (r *AIRepository) RecordQueueDropped(ctx context.Context, eventID, releaseID, mode string, scheduledAt time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	eventID, releaseID = strings.TrimSpace(eventID), strings.TrimSpace(releaseID)
	if eventID == "" || releaseID == "" {
		return ErrDecisionNotFound
	}
	ctx = aiContext(ctx)
	stamp := aiTime(scheduledAt).Unix()
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	id, err := domain.NewID()
	if err != nil {
		return err
	}
	insertResult, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ai_decisions (id,normalized_event_id,classifier_release_id,status,mode_at_schedule,effective_action,scheduled_at,created_at) VALUES (?,?,?,?,?,?,?,?)`, id, eventID, releaseID, "queued", normalizeMode(mode), "pass", stamp, stamp)
	if err != nil {
		return err
	}
	inserted, _ := insertResult.RowsAffected()
	if inserted == 1 {
		if _, err = tx.ExecContext(ctx, `UPDATE ai_decisions SET status='queue_dropped',effective_action='pass',hard_pass_reason='queue_dropped',error_code='queue_dropped',completed_at=? WHERE normalized_event_id=? AND classifier_release_id=? AND status='queued' AND call_started_at IS NULL`, stamp, eventID, releaseID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// MarkDecisionCallStarted is the irreversible at-most-once boundary. It must
// succeed durably before an HTTP request is sent to the provider.
func (r *AIRepository) MarkDecisionCallStarted(ctx context.Context, decisionID string, startedAt time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), "UPDATE ai_decisions SET call_started_at=?, status='running' WHERE id=? AND status='running' AND call_started_at IS NULL", aiTime(startedAt).Unix(), strings.TrimSpace(decisionID))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrDecisionStarted
	}
	return nil
}

func (r *AIRepository) FinishDecision(ctx context.Context, decisionID string, status classifier.DecisionStatus, result classifier.Classification, suggested, hardReason, providerName, model, errorCode string, usage classifier.Usage, costMicros *int64, currency string, latencyMS *int64, completedAt time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if !validDecisionStatus(status) {
		return ErrDecisionNotFound
	}
	if err := usage.Validate(); err != nil {
		return err
	}
	raw := "{}"
	if result.Category != "" {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		raw = string(encoded)
	}
	effective := "pass"
	if suggested != "drop" {
		suggested = "pass"
	}
	var inTok, outTok, total any
	if usage.InputTokens != nil {
		inTok = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		outTok = *usage.OutputTokens
	}
	if usage.Total() != nil {
		totalVal := *usage.Total()
		total = totalVal
	}
	var cost any
	if costMicros != nil {
		cost = *costMicros
	}
	var latency any
	if latencyMS != nil {
		latency = *latencyMS
	}
	execResult, err := r.store.db.ExecContext(aiContext(ctx), `UPDATE ai_decisions SET status=?,classification_json=?,suggested_action=?,effective_action=?,hard_pass_reason=?,provider=?,model=?,input_tokens=?,output_tokens=?,total_tokens=?,cost_micros=?,cost_currency=?,latency_ms=?,error_code=?,completed_at=? WHERE id=? AND status='running' AND call_started_at IS NOT NULL`, status, raw, suggested, effective, strings.TrimSpace(hardReason), strings.TrimSpace(providerName), strings.TrimSpace(model), inTok, outTok, total, cost, strings.TrimSpace(currency), latency, strings.TrimSpace(errorCode), aiTime(completedAt).Unix(), strings.TrimSpace(decisionID))
	if err != nil {
		return err
	}
	n, _ := execResult.RowsAffected()
	if n != 1 {
		return ErrDecisionStarted
	}
	return nil
}

// ReviewDecision records an explicit administrator review of a suggested DROP
// (or its correction). Review state is durable and intentionally separate from
// the model's classification; it never changes effective delivery, which is
// fixed to PASS in Phase 4. The timestamp is set only when reviewed is true.
func (r *AIRepository) ReviewDecision(ctx context.Context, decisionID string, reviewed bool, reviewedAt time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	decisionID = strings.TrimSpace(decisionID)
	if decisionID == "" {
		return ErrDecisionNotFound
	}
	var stamp any
	if reviewed {
		stamp = aiTime(reviewedAt).Unix()
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), `UPDATE ai_decisions SET reviewed=?, reviewed_at=? WHERE id=?`, boolInt(reviewed), stamp, decisionID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrDecisionNotFound
	}
	return nil
}

func (r *AIRepository) RecoverDecisions(ctx context.Context, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	stamp := aiTime(now).Unix()
	tx, err := r.store.db.BeginTx(aiContext(ctx), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(aiContext(ctx), "UPDATE ai_decisions SET status='queued' WHERE status='running' AND call_started_at IS NULL"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(aiContext(ctx), "UPDATE ai_decisions SET status='uncertain_call',effective_action='pass',hard_pass_reason='uncertain_call',error_code='uncertain_call',completed_at=? WHERE status='running' AND call_started_at IS NOT NULL", stamp); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *AIRepository) Decision(ctx context.Context, id string) (classifier.Decision, error) {
	if err := r.require(); err != nil {
		return classifier.Decision{}, err
	}
	row := r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,normalized_event_id,classifier_release_id,status,mode_at_schedule,classification_json,suggested_action,effective_action,hard_pass_reason,provider,model,input_tokens,output_tokens,total_tokens,cost_micros,cost_currency,latency_ms,error_code,scheduled_at,call_started_at,completed_at,created_at,reviewed,reviewed_at FROM ai_decisions WHERE id=?`, id)
	return scanDecision(row)
}

// DecisionsForEvent returns the event-level decisions associated with one
// normalized event. A release may have at most one row per event, while a
// historical event can legitimately have decisions for multiple releases.
func (r *AIRepository) DecisionsForEvent(ctx context.Context, eventID string) ([]classifier.Decision, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	// The Observation API addresses an event by its Phase 2 observed row id,
	// while the Shadow runtime deliberately derives a separate stable
	// NormalizedEvent id. Accept both identities so the detail view can show
	// decisions without collapsing the two durable contracts.
	eventID = strings.TrimSpace(eventID)
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT d.id,d.normalized_event_id,d.classifier_release_id,d.status,d.mode_at_schedule,d.classification_json,d.suggested_action,d.effective_action,d.hard_pass_reason,d.provider,d.model,d.input_tokens,d.output_tokens,d.total_tokens,d.cost_micros,d.cost_currency,d.latency_ms,d.error_code,d.scheduled_at,d.call_started_at,d.completed_at,d.created_at,d.reviewed,d.reviewed_at FROM ai_decisions d JOIN normalized_events e ON e.id=d.normalized_event_id WHERE e.observed_event_id=? OR d.normalized_event_id=? ORDER BY d.created_at DESC,d.id DESC`, eventID, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]classifier.Decision, 0)
	for rows.Next() {
		value, scanErr := scanDecision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *AIRepository) ListDecisions(ctx context.Context, limit int, cursor *DecisionCursor) ([]classifier.Decision, *DecisionCursor, error) {
	if err := r.require(); err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	clauses := []string{}
	args := []any{}
	if cursor != nil {
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	query := `SELECT id,normalized_event_id,classifier_release_id,status,mode_at_schedule,classification_json,suggested_action,effective_action,hard_pass_reason,provider,model,input_tokens,output_tokens,total_tokens,cost_micros,cost_currency,latency_ms,error_code,scheduled_at,call_started_at,completed_at,created_at,reviewed,reviewed_at FROM ai_decisions`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := r.store.db.QueryContext(aiContext(ctx), query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values := make([]classifier.Decision, 0, limit)
	for rows.Next() {
		v, scanErr := scanDecision(rows)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		values = append(values, v)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *DecisionCursor
	if len(values) > limit {
		last := values[limit-1]
		next = &DecisionCursor{CreatedAt: last.CreatedAt.Unix(), ID: last.ID}
		values = values[:limit]
	}
	return values, next, nil
}

type DecisionCursor struct {
	CreatedAt int64
	ID        string
}

type DecisionQuery struct {
	Limit           int
	Cursor          *DecisionCursor
	Status          string
	Category        string
	Importance      string
	SuggestedAction string
	SourceID        string
	ReleaseID       string
	From            *time.Time
	To              *time.Time
}

type DecisionSummary struct {
	Total          int64  `json:"total"`
	Completed      int64  `json:"completed"`
	Errors         int64  `json:"errors"`
	SuggestedDrop  int64  `json:"suggested_drop"`
	ReviewedDrop   int64  `json:"reviewed_drop"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
	CostMicros     int64  `json:"cost_micros"`
	CostCurrency   string `json:"cost_currency"`
	QueueDropped   int64  `json:"queue_dropped"`
	UncertainCalls int64  `json:"uncertain_calls"`
}

// PruneAI deletes only the 90-day operational AI shadow records selected by
// the caller. Evaluation cases/runs are intentionally independent and are
// never removed by this retention operation. The normalized-event foreign key
// cascades associated decisions and route evaluations.
func (r *AIRepository) PruneAI(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), `DELETE FROM normalized_events WHERE id IN (SELECT id FROM normalized_events WHERE observed_at < ? ORDER BY observed_at,id LIMIT ?)`, before.UTC().Unix(), limit)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	// OFF/ineligible route evaluations intentionally have no event FK. Prune
	// their operational rows by the same retention boundary without touching
	// the independent Evaluation dataset.
	if _, err := r.store.db.ExecContext(aiContext(ctx), `DELETE FROM ai_route_evaluations WHERE created_at < ? LIMIT ?`, before.UTC().Unix(), limit); err != nil {
		return int(count), err
	}
	return int(count), nil
}

// ListDecisionPage provides stable cursor pagination and optional allowlisted
// filters for the Shadow dashboard. It never uses OFFSET, so retention or
// concurrent inserts cannot reorder a previously issued cursor.
func (r *AIRepository) ListDecisionPage(ctx context.Context, query DecisionQuery) ([]classifier.Decision, *DecisionCursor, error) {
	if err := r.require(); err != nil {
		return nil, nil, err
	}
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 200 {
		query.Limit = 200
	}
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 12)
	if strings.TrimSpace(query.Status) != "" {
		clauses = append(clauses, "d.status = ?")
		args = append(args, strings.TrimSpace(query.Status))
	}
	if strings.TrimSpace(query.SuggestedAction) != "" {
		clauses = append(clauses, "d.suggested_action = ?")
		args = append(args, strings.TrimSpace(query.SuggestedAction))
	}
	if strings.TrimSpace(query.ReleaseID) != "" {
		clauses = append(clauses, "d.classifier_release_id = ?")
		args = append(args, strings.TrimSpace(query.ReleaseID))
	}
	if strings.TrimSpace(query.Category) != "" {
		clauses = append(clauses, "json_extract(d.classification_json, '$.category') = ?")
		args = append(args, strings.TrimSpace(query.Category))
	}
	if strings.TrimSpace(query.Importance) != "" {
		clauses = append(clauses, "json_extract(d.classification_json, '$.importance') = ?")
		args = append(args, strings.TrimSpace(query.Importance))
	}
	if strings.TrimSpace(query.SourceID) != "" {
		clauses = append(clauses, "e.source_id = ?")
		args = append(args, strings.TrimSpace(query.SourceID))
	}
	if query.From != nil {
		clauses = append(clauses, "d.created_at >= ?")
		args = append(args, query.From.UTC().Unix())
	}
	if query.To != nil {
		clauses = append(clauses, "d.created_at <= ?")
		args = append(args, query.To.UTC().Unix())
	}
	if query.Cursor != nil {
		clauses = append(clauses, "(d.created_at < ? OR (d.created_at = ? AND d.id < ?))")
		args = append(args, query.Cursor.CreatedAt, query.Cursor.CreatedAt, query.Cursor.ID)
	}
	statement := `SELECT d.id,d.normalized_event_id,d.classifier_release_id,d.status,d.mode_at_schedule,d.classification_json,d.suggested_action,d.effective_action,d.hard_pass_reason,d.provider,d.model,d.input_tokens,d.output_tokens,d.total_tokens,d.cost_micros,d.cost_currency,d.latency_ms,d.error_code,d.scheduled_at,d.call_started_at,d.completed_at,d.created_at,d.reviewed,d.reviewed_at FROM ai_decisions d LEFT JOIN normalized_events e ON e.id=d.normalized_event_id`
	if len(clauses) > 0 {
		statement += " WHERE " + strings.Join(clauses, " AND ")
	}
	statement += " ORDER BY d.created_at DESC,d.id DESC LIMIT ?"
	args = append(args, query.Limit+1)
	rows, err := r.store.db.QueryContext(aiContext(ctx), statement, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values := make([]classifier.Decision, 0, query.Limit)
	for rows.Next() {
		value, scanErr := scanDecision(rows)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *DecisionCursor
	if len(values) > query.Limit {
		last := values[query.Limit-1]
		next = &DecisionCursor{CreatedAt: last.CreatedAt.Unix(), ID: last.ID}
		values = values[:query.Limit]
	}
	return values, next, nil
}

func (r *AIRepository) ShadowSummary(ctx context.Context) (DecisionSummary, error) {
	if err := r.require(); err != nil {
		return DecisionSummary{}, err
	}
	var result DecisionSummary
	var currency sql.NullString
	row := r.store.db.QueryRowContext(aiContext(ctx), `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status IN ('provider_error','parse_error','timeout','provider_unavailable','uncertain_call','queue_dropped','skipped') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN suggested_action='drop' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN suggested_action='drop' AND reviewed=1 THEN 1 ELSE 0 END),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(total_tokens),0),COALESCE(SUM(cost_micros),0),MAX(NULLIF(cost_currency,'')),COALESCE(SUM(CASE WHEN status='queue_dropped' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='uncertain_call' THEN 1 ELSE 0 END),0) FROM ai_decisions`)
	if err := row.Scan(&result.Total, &result.Completed, &result.Errors, &result.SuggestedDrop, &result.ReviewedDrop, &result.InputTokens, &result.OutputTokens, &result.TotalTokens, &result.CostMicros, &currency, &result.QueueDropped, &result.UncertainCalls); err != nil {
		return DecisionSummary{}, err
	}
	if currency.Valid {
		result.CostCurrency = currency.String
	}
	return result, nil
}

type EnforceReadiness struct {
	Ready                    bool     `json:"ready"`
	RegressionCases          int64    `json:"regression_cases"`
	ImportantPassCases       int64    `json:"important_pass_cases"`
	KnownImportantFalseDrops int64    `json:"known_important_false_drops"`
	DropPrecision            float64  `json:"drop_precision"`
	ParseSuccess             float64  `json:"parse_success"`
	ShadowDecisions          int64    `json:"shadow_decisions"`
	ReviewedSuggestedDrop    int64    `json:"reviewed_suggested_drop"`
	UnresolvedCriticalDrops  int64    `json:"unresolved_critical_false_drops"`
	Reasons                  []string `json:"reasons"`
}

// EnforceReadiness reports Phase 5 metrics without enabling ENFORCE. Counts
// are derived from durable real_reviewed cases and current-release decisions;
// synthetic fixtures never satisfy the readiness thresholds.
func (r *AIRepository) EnforceReadiness(ctx context.Context) (EnforceReadiness, error) {
	if err := r.require(); err != nil {
		return EnforceReadiness{}, err
	}
	var value EnforceReadiness
	queries := []struct {
		query string
		dest  any
	}{
		{`SELECT COUNT(*) FROM ai_evaluation_cases WHERE label_kind='real_reviewed'`, &value.RegressionCases},
		{`SELECT COUNT(*) FROM ai_evaluation_cases WHERE label_kind='real_reviewed' AND critical=1 AND expected_importance IN ('high','critical') AND expected_action='pass'`, &value.ImportantPassCases},
		{`SELECT COUNT(*) FROM ai_evaluation_results r JOIN ai_evaluation_cases c ON c.id=r.case_id WHERE c.label_kind='real_reviewed' AND c.critical=1 AND r.expected_action='pass' AND r.suggested_action='drop'`, &value.KnownImportantFalseDrops},
		{`SELECT COUNT(*) FROM ai_decisions d JOIN classifier_releases cr ON cr.id=d.classifier_release_id WHERE cr.active=1`, &value.ShadowDecisions},
		{`SELECT COUNT(*) FROM ai_decisions d JOIN classifier_releases cr ON cr.id=d.classifier_release_id WHERE cr.active=1 AND d.suggested_action='drop' AND d.reviewed=1`, &value.ReviewedSuggestedDrop},
	}
	for _, item := range queries {
		if err := r.store.db.QueryRowContext(aiContext(ctx), item.query).Scan(item.dest); err != nil {
			return EnforceReadiness{}, err
		}
	}
	var dropTotal, dropCorrect int64
	if err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT COALESCE(SUM(CASE WHEN r.suggested_action='drop' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN r.suggested_action='drop' AND r.expected_action='drop' THEN 1 ELSE 0 END),0) FROM ai_evaluation_results r JOIN ai_evaluation_cases c ON c.id=r.case_id WHERE c.label_kind='real_reviewed'`).Scan(&dropTotal, &dropCorrect); err != nil {
		return EnforceReadiness{}, err
	}
	// A critical case labelled PASS but observed as DROP is unresolved until an
	// administrator has corrected the dataset/policy. Evaluation results do
	// not carry a separate review bit, so keep the readiness gate conservative:
	// every known critical false drop remains unresolved.
	value.UnresolvedCriticalDrops = value.KnownImportantFalseDrops
	if dropTotal == 0 {
		value.DropPrecision = 1
	} else {
		value.DropPrecision = float64(dropCorrect) / float64(dropTotal)
	}
	var total, parsed int64
	if err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END),0) FROM ai_decisions`).Scan(&total, &parsed); err != nil {
		return EnforceReadiness{}, err
	}
	if total > 0 {
		value.ParseSuccess = float64(parsed) / float64(total)
	} else {
		value.ParseSuccess = 1
	}
	if value.RegressionCases < 100 {
		value.Reasons = append(value.Reasons, "regression_cases")
	}
	if value.ImportantPassCases < 40 {
		value.Reasons = append(value.Reasons, "important_pass_cases")
	}
	if value.KnownImportantFalseDrops != 0 {
		value.Reasons = append(value.Reasons, "known_important_false_drops")
	}
	if value.DropPrecision < 0.90 {
		value.Reasons = append(value.Reasons, "drop_precision")
	}
	if value.ParseSuccess < 0.99 {
		value.Reasons = append(value.Reasons, "parse_success")
	}
	if value.ShadowDecisions < 200 {
		value.Reasons = append(value.Reasons, "shadow_decisions")
	}
	if value.ReviewedSuggestedDrop < 50 {
		value.Reasons = append(value.Reasons, "reviewed_suggested_drop")
	}
	if value.UnresolvedCriticalDrops != 0 {
		value.Reasons = append(value.Reasons, "unresolved_critical_false_drops")
	}
	value.Ready = len(value.Reasons) == 0
	return value, nil
}

func (r *AIRepository) PutRouteEvaluation(ctx context.Context, value RouteEvaluationRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID, _ = domain.NewID()
	}
	if value.EffectiveAction == "" {
		value.EffectiveAction = "pass"
	}
	if value.ClassificationSuggestedAction == "" {
		value.ClassificationSuggestedAction = "pass"
	}
	if value.PolicySuggestedAction == "" {
		value.PolicySuggestedAction = "pass"
	}
	// Route evaluation is a best-effort diagnostic record. A direct runtime
	// caller may supply an in-memory policy profile that has not been persisted
	// yet; do not turn that harmless condition into a failed write or invent a
	// durable profile row. Keep the identity only when it is a real profile.
	if value.EffectiveProfileID != "" {
		var exists int
		if err := r.store.db.QueryRowContext(aiContext(ctx), "SELECT EXISTS (SELECT 1 FROM ai_profiles WHERE id=?)", value.EffectiveProfileID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			value.EffectiveProfileID = ""
		}
	}
	raw, _ := json.Marshal(value.PolicyProvenance)
	decisionID := nullIfEmpty(value.DecisionID)
	args := []any{value.ID, value.RouteObservationID, decisionID, value.EffectiveMode, nullIfEmpty(value.EffectiveProfileID), value.ClassificationSuggestedAction, value.PolicySuggestedAction, "pass", value.HardPassReason, string(raw), aiTime(value.CreatedAt).Unix()}
	statement := `INSERT INTO ai_route_evaluations (id,route_observation_id,ai_decision_id,effective_mode,effective_profile_id,classification_suggested_action,policy_suggested_action,effective_action,hard_pass_reason,policy_provenance_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`
	if value.DecisionID != "" {
		statement += ` ON CONFLICT(route_observation_id,ai_decision_id) DO UPDATE SET effective_mode=excluded.effective_mode,effective_profile_id=excluded.effective_profile_id,classification_suggested_action=excluded.classification_suggested_action,policy_suggested_action=excluded.policy_suggested_action,effective_action='pass',hard_pass_reason=excluded.hard_pass_reason,policy_provenance_json=excluded.policy_provenance_json`
	} else {
		statement += ` ON CONFLICT DO UPDATE SET effective_mode=excluded.effective_mode,effective_profile_id=excluded.effective_profile_id,classification_suggested_action=excluded.classification_suggested_action,policy_suggested_action=excluded.policy_suggested_action,effective_action='pass',hard_pass_reason=excluded.hard_pass_reason,policy_provenance_json=excluded.policy_provenance_json`
	}
	_, err := r.store.db.ExecContext(aiContext(ctx), statement, args...)
	return err
}

func (r *AIRepository) RouteEvaluationsForDecision(ctx context.Context, decisionID string) ([]RouteEvaluationRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,route_observation_id,ai_decision_id,effective_mode,COALESCE(effective_profile_id,''),classification_suggested_action,policy_suggested_action,effective_action,hard_pass_reason,policy_provenance_json,created_at FROM ai_route_evaluations WHERE ai_decision_id=? ORDER BY created_at,id`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []RouteEvaluationRecord
	for rows.Next() {
		var v RouteEvaluationRecord
		var provenance string
		var decisionID sql.NullString
		var ts int64
		if err := rows.Scan(&v.ID, &v.RouteObservationID, &decisionID, &v.EffectiveMode, &v.EffectiveProfileID, &v.ClassificationSuggestedAction, &v.PolicySuggestedAction, &v.EffectiveAction, &v.HardPassReason, &provenance, &ts); err != nil {
			return nil, err
		}
		v.DecisionID = decisionID.String
		_ = json.Unmarshal([]byte(provenance), &v.PolicyProvenance)
		v.CreatedAt = time.Unix(ts, 0).UTC()
		values = append(values, v)
	}
	return values, rows.Err()
}

// RouteEvaluation returns the latest durable evaluation for one route
// observation. It is useful for OFF/ineligible routes, which intentionally do
// not have an ai_decision_id and therefore cannot be looked up through a
// decision detail endpoint.
func (r *AIRepository) RouteEvaluation(ctx context.Context, routeObservationID string) (RouteEvaluationRecord, error) {
	if err := r.require(); err != nil {
		return RouteEvaluationRecord{}, err
	}
	var value RouteEvaluationRecord
	var decisionID sql.NullString
	var profileID, provenance string
	var timestamp int64
	err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,route_observation_id,ai_decision_id,effective_mode,COALESCE(effective_profile_id,''),classification_suggested_action,policy_suggested_action,effective_action,hard_pass_reason,policy_provenance_json,created_at FROM ai_route_evaluations WHERE route_observation_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, strings.TrimSpace(routeObservationID)).Scan(&value.ID, &value.RouteObservationID, &decisionID, &value.EffectiveMode, &profileID, &value.ClassificationSuggestedAction, &value.PolicySuggestedAction, &value.EffectiveAction, &value.HardPassReason, &provenance, &timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteEvaluationRecord{}, ErrRouteEvaluationNotFound
	}
	if err != nil {
		return RouteEvaluationRecord{}, err
	}
	value.DecisionID = decisionID.String
	value.EffectiveProfileID = profileID
	_ = json.Unmarshal([]byte(provenance), &value.PolicyProvenance)
	value.CreatedAt = time.Unix(timestamp, 0).UTC()
	return value, nil
}

func scanDecision(row interface{ Scan(...any) error }) (classifier.Decision, error) {
	var id, eventID, releaseID, status, modeAt, classification, suggested, effective, hard, providerName, model, currency, errorCode string
	var inTok, outTok, total, cost, latency, scheduled, started, completed, created, reviewed, reviewedAt sql.NullInt64
	err := row.Scan(&id, &eventID, &releaseID, &status, &modeAt, &classification, &suggested, &effective, &hard, &providerName, &model, &inTok, &outTok, &total, &cost, &currency, &latency, &errorCode, &scheduled, &started, &completed, &created, &reviewed, &reviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return classifier.Decision{}, ErrDecisionNotFound
	}
	if err != nil {
		return classifier.Decision{}, err
	}
	return scanDecisionValues(id, eventID, releaseID, status, modeAt, classification, suggested, effective, hard, providerName, model, currency, errorCode, inTok, outTok, total, cost, latency, scheduled, started, completed, created, reviewed, reviewedAt), nil
}

func scanDecisionValues(id, eventID, releaseID, status, modeAt, classification, suggested, effective, hard, providerName, model, currency, errorCode string, inTok, outTok, total, cost, latency, scheduled, started, completed, created, reviewed, reviewedAt sql.NullInt64) classifier.Decision {
	value := classifier.Decision{ID: id, NormalizedEventID: eventID, ClassifierReleaseID: releaseID, Status: classifier.DecisionStatus(status), ModeAtSchedule: modeAt, SuggestedAction: suggested, EffectiveAction: effective, HardPassReason: hard, Provider: providerName, Model: model, CostCurrency: currency, ErrorCode: errorCode}
	if classification != "" && classification != "{}" {
		_ = json.Unmarshal([]byte(classification), &value.Classification)
	}
	if inTok.Valid {
		v := inTok.Int64
		value.Usage.InputTokens = &v
	}
	if outTok.Valid {
		v := outTok.Int64
		value.Usage.OutputTokens = &v
	}
	if total.Valid {
		v := total.Int64
		value.Usage.TotalTokens = &v
	}
	if cost.Valid {
		v := cost.Int64
		value.CostMicros = &v
	}
	if latency.Valid {
		v := latency.Int64
		value.LatencyMS = &v
	}
	if scheduled.Valid {
		value.ScheduledAt = time.Unix(scheduled.Int64, 0).UTC()
	}
	if started.Valid {
		v := time.Unix(started.Int64, 0).UTC()
		value.CallStartedAt = &v
	}
	if completed.Valid {
		v := time.Unix(completed.Int64, 0).UTC()
		value.CompletedAt = &v
	}
	value.Reviewed = reviewed.Valid && reviewed.Int64 != 0
	if reviewedAt.Valid {
		v := time.Unix(reviewedAt.Int64, 0).UTC()
		value.ReviewedAt = &v
	}
	if created.Valid {
		value.CreatedAt = time.Unix(created.Int64, 0).UTC()
	}
	return value
}

func normalizeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "shadow", "enforce":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "shadow"
	}
}
func normalizeProviderKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai-compatible", "openai_compatible":
		return "openai_compatible"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
func normalizeOutputMode(value string) string {
	if strings.TrimSpace(value) == "json-schema" {
		return "json_schema"
	}
	return strings.TrimSpace(value)
}
func validDecisionStatus(value classifier.DecisionStatus) bool {
	switch value {
	case classifier.StatusQueued, classifier.StatusRunning, classifier.StatusCompleted, classifier.StatusProviderError, classifier.StatusParseError, classifier.StatusTimeout, classifier.StatusQueueDropped, classifier.StatusSkipped, classifier.StatusProviderUnavailable, classifier.StatusUncertainCall:
		return true
	}
	return false
}
func mustJSONArray(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func (r *AIRepository) SaveProfile(ctx context.Context, value policy.Profile, createdAt, updatedAt time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if value.ID == "" {
		return ErrAIProfileNotFound
	}
	if value.Builtin && value.ID != policy.OfficialGameProfile().ID {
		return ErrAIProfileBuiltin
	}
	var existingBuiltin int
	err := r.store.db.QueryRowContext(aiContext(ctx), "SELECT builtin FROM ai_profiles WHERE id=?", value.ID).Scan(&existingBuiltin)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingBuiltin != 0 {
		// The system profile may be re-seeded idempotently at startup, but no
		// caller may mutate or demote it through the general profile method.
		if !profileSemanticallyEqual(value, policy.OfficialGameProfile()) {
			return ErrAIProfileBuiltin
		}
		value.Builtin = true
	}
	// The built-in profile may be seeded exactly once (or re-seeded with the
	// same immutable definition). What must be rejected is using the reserved
	// identifier for a mutable, non-built-in profile.
	if existingBuiltin == 0 && value.ID == policy.OfficialGameProfile().ID && !value.Builtin {
		return ErrAIProfileBuiltin
	}
	cats, _ := json.Marshal(value.CategoryActions)
	tags, _ := json.Marshal(value.TagActions)
	safety, _ := json.Marshal(value.Safety)
	_, err = r.store.db.ExecContext(aiContext(ctx), `INSERT INTO ai_profiles (id,name,description,default_action,category_actions_json,tag_actions_json,safety_json,builtin,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,default_action=excluded.default_action,category_actions_json=excluded.category_actions_json,tag_actions_json=excluded.tag_actions_json,safety_json=excluded.safety_json,updated_at=excluded.updated_at`, value.ID, value.Name, value.Description, value.DefaultAction, string(cats), string(tags), string(safety), boolInt(value.Builtin), aiTime(createdAt).Unix(), aiTime(updatedAt).Unix())
	return err
}

func profileSemanticallyEqual(left, right policy.Profile) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Description != right.Description || left.DefaultAction != right.DefaultAction || left.Builtin != right.Builtin {
		return false
	}
	if len(left.CategoryActions) != len(right.CategoryActions) || len(left.TagActions) != len(right.TagActions) || len(left.Safety) != len(right.Safety) {
		return false
	}
	for key, value := range right.CategoryActions {
		if left.CategoryActions[key] != value {
			return false
		}
	}
	for key, value := range right.TagActions {
		if left.TagActions[key] != value {
			return false
		}
	}
	for key, value := range right.Safety {
		if left.Safety[key] != value {
			return false
		}
	}
	return true
}
func (r *AIRepository) Profile(ctx context.Context, id string) (policy.Profile, error) {
	if err := r.require(); err != nil {
		return policy.Profile{}, err
	}
	var p policy.Profile
	var cats, tags, safety string
	var builtin, created, updated int64
	err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,name,description,default_action,category_actions_json,tag_actions_json,safety_json,builtin,created_at,updated_at FROM ai_profiles WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.Description, &p.DefaultAction, &cats, &tags, &safety, &builtin, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Profile{}, ErrAIProfileNotFound
	}
	if err != nil {
		return policy.Profile{}, err
	}
	_ = json.Unmarshal([]byte(cats), &p.CategoryActions)
	_ = json.Unmarshal([]byte(tags), &p.TagActions)
	_ = json.Unmarshal([]byte(safety), &p.Safety)
	p.Builtin = builtin != 0
	return p, nil
}
func (r *AIRepository) Profiles(ctx context.Context) ([]policy.Profile, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,name,description,default_action,category_actions_json,tag_actions_json,safety_json,builtin FROM ai_profiles ORDER BY builtin DESC,name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []policy.Profile
	for rows.Next() {
		var p policy.Profile
		var cats, tags, safety string
		var builtin int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.DefaultAction, &cats, &tags, &safety, &builtin); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cats), &p.CategoryActions)
		_ = json.Unmarshal([]byte(tags), &p.TagActions)
		_ = json.Unmarshal([]byte(safety), &p.Safety)
		p.Builtin = builtin != 0
		values = append(values, p)
	}
	return values, rows.Err()
}
func (r *AIRepository) DeleteProfile(ctx context.Context, id string) error {
	if err := r.require(); err != nil {
		return err
	}
	var builtin int
	if err := r.store.db.QueryRowContext(aiContext(ctx), "SELECT builtin FROM ai_profiles WHERE id=?", id).Scan(&builtin); errors.Is(err, sql.ErrNoRows) {
		return ErrAIProfileNotFound
	} else if err != nil {
		return err
	}
	if builtin != 0 {
		return ErrAIProfileBuiltin
	}
	var inUse int
	if err := r.store.db.QueryRowContext(aiContext(ctx), "SELECT EXISTS (SELECT 1 FROM ai_policy_overrides WHERE profile_id=?)", id).Scan(&inUse); err != nil {
		return err
	}
	if inUse != 0 {
		return ErrAIProfileInUse
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), `DELETE FROM ai_profiles WHERE id=? AND builtin=0`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrAIProfileNotFound
	}
	return nil
}

func (r *AIRepository) SavePolicy(ctx context.Context, value AIPolicyOverrideRecord, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID, _ = domain.NewID()
	}
	if value.ScopeType == "" {
		return ErrAIPolicyNotFound
	}
	cats, _ := json.Marshal(value.CategoryActions)
	tags, _ := json.Marshal(value.TagActions)
	_, err := r.store.db.ExecContext(aiContext(ctx), `INSERT INTO ai_policy_overrides (id,scope_type,scope_id,mode,profile_id,threshold,default_action,category_actions_json,tag_actions_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(scope_type,scope_id) DO UPDATE SET mode=excluded.mode,profile_id=excluded.profile_id,threshold=excluded.threshold,default_action=excluded.default_action,category_actions_json=excluded.category_actions_json,tag_actions_json=excluded.tag_actions_json,updated_at=excluded.updated_at`, value.ID, value.ScopeType, value.ScopeID, nullIfEmpty(string(value.Mode)), nullIfEmpty(value.ProfileID), value.Threshold, nullIfEmpty(string(value.DefaultAction)), string(cats), string(tags), aiTime(value.CreatedAt).Unix(), aiTime(now).Unix())
	return err
}
func (r *AIRepository) Policy(ctx context.Context, scopeType, scopeID string) (AIPolicyOverrideRecord, error) {
	if err := r.require(); err != nil {
		return AIPolicyOverrideRecord{}, err
	}
	var v AIPolicyOverrideRecord
	var mode, profile, def, cats, tags string
	var threshold sql.NullFloat64
	var created, updated int64
	err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,scope_type,scope_id,COALESCE(mode,''),COALESCE(profile_id,''),threshold,COALESCE(default_action,''),category_actions_json,tag_actions_json,created_at,updated_at FROM ai_policy_overrides WHERE scope_type=? AND scope_id=?`, scopeType, scopeID).Scan(&v.ID, &v.ScopeType, &v.ScopeID, &mode, &profile, &threshold, &def, &cats, &tags, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return AIPolicyOverrideRecord{}, ErrAIPolicyNotFound
	}
	if err != nil {
		return AIPolicyOverrideRecord{}, err
	}
	v.Mode = policy.Mode(mode)
	v.ProfileID = profile
	v.DefaultAction = policy.Action(def)
	if threshold.Valid {
		x := threshold.Float64
		v.Threshold = &x
	}
	_ = json.Unmarshal([]byte(cats), &v.CategoryActions)
	_ = json.Unmarshal([]byte(tags), &v.TagActions)
	v.CreatedAt = time.Unix(created, 0).UTC()
	v.UpdatedAt = time.Unix(updated, 0).UTC()
	return v, nil
}
func (r *AIRepository) Policies(ctx context.Context) ([]AIPolicyOverrideRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT scope_type,scope_id FROM ai_policy_overrides ORDER BY scope_type,scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []AIPolicyOverrideRecord
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			return nil, err
		}
		v, e := r.Policy(ctx, typ, id)
		if e != nil {
			return nil, e
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (r *AIRepository) CreateEvaluationCase(ctx context.Context, value EvaluationCaseRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID, _ = domain.NewID()
	}
	if err := validateEvaluationCase(value); err != nil {
		return err
	}
	stamp := aiTime(value.CreatedAt)
	updated := aiTime(value.UpdatedAt)
	_, err := r.store.db.ExecContext(aiContext(ctx), `INSERT INTO ai_evaluation_cases (id,normalized_input_snapshot_json,expected_importance,expected_action,critical,label_kind,notes,source,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, value.ID, string(value.NormalizedInputSnapshot), value.ExpectedImportance, value.ExpectedAction, boolInt(value.Critical), value.LabelKind, value.Notes, value.Source, stamp.Unix(), updated.Unix())
	return err
}

// ImportEvaluationCases validates the complete batch before opening a write
// transaction, then inserts every case atomically. This keeps a malformed
// later item from leaving a partially imported evaluation dataset.
func (r *AIRepository) ImportEvaluationCases(ctx context.Context, values []EvaluationCaseRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if len(values) == 0 || len(values) > 1000 {
		return ErrEvaluationInvalid
	}
	prepared := make([]EvaluationCaseRecord, len(values))
	copy(prepared, values)
	for i := range prepared {
		if strings.TrimSpace(prepared[i].ID) == "" {
			id, err := domain.NewID()
			if err != nil {
				return ErrEvaluationInvalid
			}
			prepared[i].ID = id
		}
		if prepared[i].CreatedAt.IsZero() {
			prepared[i].CreatedAt = aiTime(time.Time{})
		}
		if prepared[i].UpdatedAt.IsZero() {
			prepared[i].UpdatedAt = prepared[i].CreatedAt
		}
		if err := validateEvaluationCase(prepared[i]); err != nil {
			return err
		}
	}
	ctx = aiContext(ctx)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, value := range prepared {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_evaluation_cases (id,normalized_input_snapshot_json,expected_importance,expected_action,critical,label_kind,notes,source,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, value.ID, string(value.NormalizedInputSnapshot), value.ExpectedImportance, value.ExpectedAction, boolInt(value.Critical), value.LabelKind, value.Notes, value.Source, aiTime(value.CreatedAt).Unix(), aiTime(value.UpdatedAt).Unix()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func validateEvaluationCase(value EvaluationCaseRecord) error {
	if len(value.NormalizedInputSnapshot) == 0 || len(value.NormalizedInputSnapshot) > 256*1024 || !json.Valid(value.NormalizedInputSnapshot) {
		return ErrEvaluationInvalid
	}
	var event domain.NormalizedEvent
	decoder := json.NewDecoder(bytes.NewReader(value.NormalizedInputSnapshot))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return ErrEvaluationInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrEvaluationInvalid
	}
	if event.Validate() != nil || event.SchemaVersion != 1 {
		return ErrEvaluationInvalid
	}
	if value.LabelKind != "real_reviewed" && value.LabelKind != "synthetic" {
		return ErrEvaluationInvalid
	}
	if value.ExpectedImportance != "" && value.ExpectedImportance != string(domain.ImportanceLow) && value.ExpectedImportance != string(domain.ImportanceMedium) && value.ExpectedImportance != string(domain.ImportanceHigh) && value.ExpectedImportance != string(domain.ImportanceCritical) {
		return ErrEvaluationInvalid
	}
	if value.ExpectedAction != "" && value.ExpectedAction != "pass" && value.ExpectedAction != "drop" {
		return ErrEvaluationInvalid
	}
	if len([]rune(value.Notes)) > 2000 || len([]rune(value.Source)) > 256 {
		return ErrEvaluationInvalid
	}
	return nil
}

// EvaluationCase returns one frozen, public normalized input snapshot.
func (r *AIRepository) EvaluationCase(ctx context.Context, id string) (EvaluationCaseRecord, error) {
	if err := r.require(); err != nil {
		return EvaluationCaseRecord{}, err
	}
	var value EvaluationCaseRecord
	var raw string
	var critical int64
	var created, updated int64
	err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,normalized_input_snapshot_json,expected_importance,expected_action,critical,label_kind,notes,source,created_at,updated_at FROM ai_evaluation_cases WHERE id=?`, strings.TrimSpace(id)).Scan(&value.ID, &raw, &value.ExpectedImportance, &value.ExpectedAction, &critical, &value.LabelKind, &value.Notes, &value.Source, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return EvaluationCaseRecord{}, ErrEvaluationNotFound
	}
	if err != nil {
		return EvaluationCaseRecord{}, err
	}
	value.NormalizedInputSnapshot = json.RawMessage(raw)
	value.Critical = critical != 0
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	return value, nil
}

// UpdateEvaluationCase changes labels/metadata while keeping the normalized
// input snapshot independently frozen. It is intentionally a small update,
// not a general dataset workflow.
func (r *AIRepository) UpdateEvaluationCase(ctx context.Context, value EvaluationCaseRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if strings.TrimSpace(value.ID) == "" {
		return ErrEvaluationNotFound
	}
	if err := validateEvaluationCase(value); err != nil {
		return err
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), `UPDATE ai_evaluation_cases SET expected_importance=?,expected_action=?,critical=?,label_kind=?,notes=?,source=?,updated_at=? WHERE id=?`, value.ExpectedImportance, value.ExpectedAction, boolInt(value.Critical), value.LabelKind, value.Notes, value.Source, aiTime(value.UpdatedAt).Unix(), value.ID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrEvaluationNotFound
	}
	return nil
}
func (r *AIRepository) EvaluationCases(ctx context.Context) ([]EvaluationCaseRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,normalized_input_snapshot_json,expected_importance,expected_action,critical,label_kind,notes,source,created_at,updated_at FROM ai_evaluation_cases ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []EvaluationCaseRecord
	for rows.Next() {
		var v EvaluationCaseRecord
		var raw string
		var critical int64
		var created, updated int64
		if err := rows.Scan(&v.ID, &raw, &v.ExpectedImportance, &v.ExpectedAction, &critical, &v.LabelKind, &v.Notes, &v.Source, &created, &updated); err != nil {
			return nil, err
		}
		v.NormalizedInputSnapshot = json.RawMessage(raw)
		v.Critical = critical != 0
		v.CreatedAt = time.Unix(created, 0).UTC()
		v.UpdatedAt = time.Unix(updated, 0).UTC()
		values = append(values, v)
	}
	return values, rows.Err()
}
func (r *AIRepository) DeleteEvaluationCase(ctx context.Context, id string) error {
	if err := r.require(); err != nil {
		return err
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), `DELETE FROM ai_evaluation_cases WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrEvaluationNotFound
	}
	return nil
}
func (r *AIRepository) CreateEvaluationRun(ctx context.Context, value EvaluationRunRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID, _ = domain.NewID()
	}
	cases, _ := json.Marshal(value.CaseIDs)
	metrics, _ := json.Marshal(value.Metrics)
	stamp := aiTime(value.CreatedAt)
	_, err := r.store.db.ExecContext(aiContext(ctx), `INSERT INTO ai_evaluation_runs (id,classifier_release_id,status,case_ids_json,metrics_json,total_cost_micros,latency_ms,created_at) VALUES (?,?,?,?,?,?,?,?)`, value.ID, value.ClassifierReleaseID, defaultEvalStatus(value.Status), string(cases), string(metrics), value.TotalCostMicros, value.LatencyMS, stamp.Unix())
	return err
}

// StartEvaluationRun moves a queued evaluation into its execution boundary.
// It is deliberately a small state transition rather than a general
// workflow engine; callers must finish it with FinishEvaluationRun.
func (r *AIRepository) StartEvaluationRun(ctx context.Context, id string) error {
	if err := r.require(); err != nil {
		return err
	}
	result, err := r.store.db.ExecContext(aiContext(ctx), `UPDATE ai_evaluation_runs SET status='running' WHERE id=? AND status='queued'`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		value, getErr := r.EvaluationRun(ctx, id)
		if getErr != nil {
			return getErr
		}
		if value.Status == "running" {
			return ErrEvaluationRunActive
		}
		return ErrEvaluationNotFound
	}
	return nil
}

func (r *AIRepository) EvaluationRun(ctx context.Context, id string) (EvaluationRunRecord, error) {
	if err := r.require(); err != nil {
		return EvaluationRunRecord{}, err
	}
	var v EvaluationRunRecord
	var cases, metrics string
	var created, completed sql.NullInt64
	err := r.store.db.QueryRowContext(aiContext(ctx), `SELECT id,classifier_release_id,status,case_ids_json,metrics_json,total_cost_micros,latency_ms,created_at,completed_at FROM ai_evaluation_runs WHERE id=?`, id).Scan(&v.ID, &v.ClassifierReleaseID, &v.Status, &cases, &metrics, &v.TotalCostMicros, &v.LatencyMS, &created, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return EvaluationRunRecord{}, ErrEvaluationNotFound
	}
	if err != nil {
		return EvaluationRunRecord{}, err
	}
	_ = json.Unmarshal([]byte(cases), &v.CaseIDs)
	_ = json.Unmarshal([]byte(metrics), &v.Metrics)
	if created.Valid {
		v.CreatedAt = time.Unix(created.Int64, 0).UTC()
	}
	if completed.Valid {
		x := time.Unix(completed.Int64, 0).UTC()
		v.CompletedAt = &x
	}
	return v, nil
}

// EvaluationRuns returns bounded, metadata-only run records for the
// Dashboard. It intentionally does not expose provider inputs or secrets.
func (r *AIRepository) EvaluationRuns(ctx context.Context, limit int) ([]EvaluationRunRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,classifier_release_id,status,case_ids_json,metrics_json,total_cost_micros,latency_ms,created_at,completed_at FROM ai_evaluation_runs ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]EvaluationRunRecord, 0, limit)
	for rows.Next() {
		var value EvaluationRunRecord
		var cases, metrics string
		var created, completed sql.NullInt64
		if err := rows.Scan(&value.ID, &value.ClassifierReleaseID, &value.Status, &cases, &metrics, &value.TotalCostMicros, &value.LatencyMS, &created, &completed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cases), &value.CaseIDs)
		_ = json.Unmarshal([]byte(metrics), &value.Metrics)
		if created.Valid {
			value.CreatedAt = time.Unix(created.Int64, 0).UTC()
		}
		if completed.Valid {
			stamp := time.Unix(completed.Int64, 0).UTC()
			value.CompletedAt = &stamp
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (r *AIRepository) FinishEvaluationRun(ctx context.Context, id, status string, metrics map[string]any, cost, latency int64, at time.Time) error {
	if status != "completed" && status != "failed" {
		status = "failed"
	}
	raw, _ := json.Marshal(metrics)
	result, err := r.store.db.ExecContext(aiContext(ctx), `UPDATE ai_evaluation_runs SET status=?,metrics_json=?,total_cost_micros=?,latency_ms=?,completed_at=? WHERE id=?`, status, string(raw), cost, latency, aiTime(at).Unix(), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrEvaluationNotFound
	}
	return nil
}
func (r *AIRepository) PutEvaluationResult(ctx context.Context, value EvaluationResultRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID, _ = domain.NewID()
	}
	if len(value.Classification) == 0 {
		value.Classification = json.RawMessage(`{}`)
	}
	_, err := r.store.db.ExecContext(aiContext(ctx), `INSERT INTO ai_evaluation_results (id,run_id,case_id,classification_json,suggested_action,expected_action,comparison,cost_micros,latency_ms,created_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,case_id) DO UPDATE SET classification_json=excluded.classification_json,suggested_action=excluded.suggested_action,expected_action=excluded.expected_action,comparison=excluded.comparison,cost_micros=excluded.cost_micros,latency_ms=excluded.latency_ms`, value.ID, value.RunID, value.CaseID, string(value.Classification), value.SuggestedAction, value.ExpectedAction, value.Comparison, value.CostMicros, value.LatencyMS, aiTime(value.CreatedAt).Unix())
	return err
}
func (r *AIRepository) EvaluationResults(ctx context.Context, runID string) ([]EvaluationResultRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(aiContext(ctx), `SELECT id,run_id,case_id,classification_json,suggested_action,expected_action,comparison,cost_micros,latency_ms,created_at FROM ai_evaluation_results WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []EvaluationResultRecord
	for rows.Next() {
		var v EvaluationResultRecord
		var raw string
		var ts int64
		if err := rows.Scan(&v.ID, &v.RunID, &v.CaseID, &raw, &v.SuggestedAction, &v.ExpectedAction, &v.Comparison, &v.CostMicros, &v.LatencyMS, &ts); err != nil {
			return nil, err
		}
		v.Classification = json.RawMessage(raw)
		v.CreatedAt = time.Unix(ts, 0).UTC()
		values = append(values, v)
	}
	return values, rows.Err()
}
func defaultEvalStatus(value string) string {
	switch value {
	case "queued", "running", "completed", "failed":
		return value
	default:
		return "queued"
	}
}
