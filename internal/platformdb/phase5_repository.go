package platformdb

// This file owns the Phase 5 durable boundaries.  The repository is
// deliberately boring: it stores allowlisted public snapshots and state
// transitions, while orchestration (provider calls, rendering and Connector
// selection) remains in the runtime packages.  In particular, no method here
// ever stores a raw provider response or a credential.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

var (
	ErrRouteDecisionNotFound   = errors.New("platformdb: route decision not found")
	ErrDeliveryNotFound        = errors.New("platformdb: delivery not found")
	ErrReplayNotFound          = errors.New("platformdb: replay snapshot not found")
	ErrFeedbackNotFound        = errors.New("platformdb: feedback not found")
	ErrApprovalNotFound        = errors.New("platformdb: enforce approval not found")
	ErrApprovalInvalid         = errors.New("platformdb: enforce approval invalid")
	ErrEnforceNotReady         = errors.New("platformdb: enforce not ready")
	ErrReplayExpired           = errors.New("platformdb: replay snapshot expired")
	ErrDeliveryRetryNotAllowed = errors.New("platformdb: delivery retry not allowed")
	ErrPhase5Unavailable       = errors.New("platformdb: phase5 unavailable")
	ErrDeliveryTransition      = errors.New("platformdb: invalid delivery transition")
	ErrEmergencyDisabled       = errors.New("platformdb: enforce emergency disabled")
)

const (
	ReplaySnapshotSchemaVersion = 1
	ReplayRetention             = 90 * 24 * time.Hour
	FeedbackRetention           = 365 * 24 * time.Hour
)

type RouteDecisionRecord struct {
	ID                  string    `json:"id"`
	EventID             string    `json:"event_id"`
	ObservedEventID     string    `json:"observed_event_id,omitempty"`
	RouteObservationID  string    `json:"route_observation_id,omitempty"`
	SourceID            string    `json:"source_id,omitempty"`
	TargetID            string    `json:"target_id,omitempty"`
	SubscriptionID      string    `json:"subscription_id,omitempty"`
	ClassifierReleaseID string    `json:"classifier_release_id,omitempty"`
	AIDecisionID        string    `json:"ai_decision_id,omitempty"`
	ConfiguredMode      string    `json:"configured_mode"`
	EffectiveMode       string    `json:"effective_mode"`
	ProfileID           string    `json:"profile_id,omitempty"`
	PolicyDigest        string    `json:"policy_digest,omitempty"`
	SuggestedAction     string    `json:"suggested_action"`
	EffectiveAction     string    `json:"effective_action"`
	ReasonCode          string    `json:"reason_code,omitempty"`
	HardPassReason      string    `json:"hard_pass_reason,omitempty"`
	EnforceApprovalID   string    `json:"enforce_approval_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	DecidedAt           time.Time `json:"decided_at"`
}

type DeliveryRecord struct {
	ID                      string                `json:"id"`
	RouteDecisionID         string                `json:"route_decision_id,omitempty"`
	EventID                 string                `json:"event_id"`
	TargetID                string                `json:"target_id"`
	ConnectorID             string                `json:"connector_id,omitempty"`
	TargetType              string                `json:"target_type"`
	ExternalID              string                `json:"external_id"`
	Status                  domain.DeliveryStatus `json:"status"`
	ResultCode              string                `json:"result_code,omitempty"`
	RemoteMessageID         string                `json:"remote_message_id,omitempty"`
	Attempt                 int                   `json:"attempt"`
	ReplayOfRouteDecisionID string                `json:"replay_of_route_decision_id,omitempty"`
	InitiatedBy             string                `json:"initiated_by"`
	MessageSnapshotJSON     string                `json:"message_snapshot_json,omitempty"`
	CreatedAt               time.Time             `json:"created_at"`
	SendingAt               *time.Time            `json:"sending_at,omitempty"`
	CompletedAt             *time.Time            `json:"completed_at,omitempty"`
	UpdatedAt               time.Time             `json:"updated_at"`
}

type ReplayableEventRecord struct {
	ID                        string          `json:"id"`
	SchemaVersion             int             `json:"schema_version"`
	RouteDecisionID           string          `json:"route_decision_id"`
	EventID                   string          `json:"event_id"`
	ObservedEventID           string          `json:"observed_event_id,omitempty"`
	SourceID                  string          `json:"source_id,omitempty"`
	TargetID                  string          `json:"target_id,omitempty"`
	SubscriptionID            string          `json:"subscription_id,omitempty"`
	EventType                 string          `json:"event_type"`
	SnapshotJSON              json.RawMessage `json:"snapshot"`
	OriginalClassificationRef string          `json:"original_classification_ref,omitempty"`
	CreatedAt                 time.Time       `json:"created_at"`
	ExpiresAt                 time.Time       `json:"expires_at"`
}

type FeedbackRecord struct {
	ID              string    `json:"id"`
	RouteDecisionID string    `json:"route_decision_id"`
	AIDecisionID    string    `json:"ai_decision_id,omitempty"`
	FeedbackType    string    `json:"feedback_type"`
	ReviewedBy      string    `json:"reviewed_by"`
	Notes           string    `json:"notes,omitempty"`
	Resolved        bool      `json:"resolved"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type EnforceApprovalRecord struct {
	ID                    string          `json:"id"`
	ClassifierReleaseID   string          `json:"classifier_release_id"`
	PolicyDigest          string          `json:"policy_digest"`
	ProfileDigest         string          `json:"profile_digest"`
	ReadinessEvidenceJSON json.RawMessage `json:"readiness_evidence"`
	ApprovedAt            time.Time       `json:"approved_at"`
	ApprovedBy            string          `json:"approved_by"`
	RevokedAt             *time.Time      `json:"revoked_at,omitempty"`
	Reason                string          `json:"reason,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
}

type MediaCacheEntryRecord struct {
	ID             string    `json:"id"`
	SHA256         string    `json:"sha256"`
	SizeBytes      int64     `json:"size_bytes"`
	MIMEType       string    `json:"mime_type"`
	StorageKey     string    `json:"storage_key"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
}

type MediaCacheLinkRecord struct {
	EntryID         string    `json:"entry_id"`
	RouteDecisionID string    `json:"route_decision_id"`
	EventID         string    `json:"event_id"`
	SourceURL       string    `json:"source_url,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type Phase5Repository struct{ store *Store }

func NewPhase5Repository(store *Store) *Phase5Repository {
	if store == nil {
		return nil
	}
	return &Phase5Repository{store: store}
}

// NewRoutingRepository and NewDeliveryRepository are intentionally aliases so
// embedders can name the owner after the domain they use without creating a
// second SQLite handle.
func NewRoutingRepository(store *Store) *Phase5Repository  { return NewPhase5Repository(store) }
func NewDeliveryRepository(store *Store) *Phase5Repository { return NewPhase5Repository(store) }

func (r *Phase5Repository) require() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrPhase5Unavailable
	}
	return nil
}

func phase5Context(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func phase5Time(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func newPhase5ID(prefix string) string {
	id, err := domain.NewID()
	if err != nil {
		return prefix + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + id
}

func validateMode(value string) bool {
	return value == "off" || value == "shadow" || value == "enforce"
}

func validateAction(value string) bool { return value == "pass" || value == "drop" }

// ValidRouteIdentity is kept deliberately small and independent from the
// Domain repository. A Phase 5 decision may only be authoritative when the
// route has an unambiguous target identity; callers must resolve a target
// through the Domain service before asking this repository to persist a DROP.
func ValidRouteIdentity(targetID, targetType, externalID, connectorID string) bool {
	return strings.TrimSpace(targetID) != "" &&
		strings.TrimSpace(targetType) != "" &&
		strings.TrimSpace(externalID) != "" &&
		strings.TrimSpace(connectorID) != ""
}

// replaySnapshotIdentity is intentionally a small, dependency-free view of
// the public replay envelope.  platformdb must not import the replay package
// (the replay service itself depends on platformdb), but the DROP commit
// boundary still needs to verify that durable identity fields cannot drift
// between a route decision and its snapshot.  Unknown fields are ignored so
// older Phase 5 snapshots remain readable; any identity field that is present
// must match the row being committed.
type replaySnapshotIdentity struct {
	RouteDecisionID string `json:"route_decision_id"`
	EventID         string `json:"event_id"`
	ObservedEventID string `json:"observed_event_id"`
	SourceID        string `json:"source_id"`
	SubscriptionID  string `json:"subscription_id"`
	Target          struct {
		TargetID    string `json:"target_id"`
		TargetType  string `json:"target_type"`
		ExternalID  string `json:"external_id"`
		ConnectorID string `json:"connector_id"`
	} `json:"target"`
}

func validateSnapshotIdentity(decision RouteDecisionRecord, snapshot ReplayableEventRecord) error {
	if strings.TrimSpace(decision.ID) == "" || strings.TrimSpace(decision.EventID) == "" {
		return ErrApprovalInvalid
	}
	if strings.TrimSpace(snapshot.RouteDecisionID) == "" {
		snapshot.RouteDecisionID = decision.ID
	}
	if snapshot.RouteDecisionID != decision.ID || strings.TrimSpace(snapshot.EventID) == "" || snapshot.EventID != decision.EventID {
		return ErrApprovalInvalid
	}
	if snapshot.ObservedEventID != "" && decision.ObservedEventID != "" && snapshot.ObservedEventID != decision.ObservedEventID {
		return ErrApprovalInvalid
	}
	if snapshot.SourceID != "" && decision.SourceID != "" && snapshot.SourceID != decision.SourceID {
		return ErrApprovalInvalid
	}
	if snapshot.TargetID != "" && decision.TargetID != "" && snapshot.TargetID != decision.TargetID {
		return ErrApprovalInvalid
	}
	if snapshot.SubscriptionID != "" && decision.SubscriptionID != "" && snapshot.SubscriptionID != decision.SubscriptionID {
		return ErrApprovalInvalid
	}
	var identity replaySnapshotIdentity
	if err := json.Unmarshal(snapshot.SnapshotJSON, &identity); err != nil {
		return ErrApprovalInvalid
	}
	if identity.RouteDecisionID != "" && identity.RouteDecisionID != decision.ID {
		return ErrApprovalInvalid
	}
	if identity.EventID != "" && identity.EventID != decision.EventID {
		return ErrApprovalInvalid
	}
	if identity.ObservedEventID != "" && decision.ObservedEventID != "" && identity.ObservedEventID != decision.ObservedEventID {
		return ErrApprovalInvalid
	}
	if identity.SourceID != "" && decision.SourceID != "" && identity.SourceID != decision.SourceID {
		return ErrApprovalInvalid
	}
	if identity.SubscriptionID != "" && decision.SubscriptionID != "" && identity.SubscriptionID != decision.SubscriptionID {
		return ErrApprovalInvalid
	}
	if identity.Target.TargetID != "" && decision.TargetID != "" && identity.Target.TargetID != decision.TargetID {
		return ErrApprovalInvalid
	}
	return nil
}

func (r *Phase5Repository) PutRouteDecision(ctx context.Context, value RouteDecisionRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	value.ID = strings.TrimSpace(value.ID)
	if value.ID == "" {
		value.ID = newPhase5ID("route_")
	}
	value.EventID = strings.TrimSpace(value.EventID)
	if value.EventID == "" || !validateMode(value.ConfiguredMode) || !validateMode(value.EffectiveMode) || !validateAction(value.SuggestedAction) || !validateAction(value.EffectiveAction) {
		return ErrApprovalInvalid
	}
	value.CreatedAt, value.DecidedAt = phase5Time(value.CreatedAt), phase5Time(value.DecidedAt)
	_, err := r.store.db.ExecContext(phase5Context(ctx), `INSERT INTO route_decisions
(id,event_id,observed_event_id,route_observation_id,source_id,target_id,subscription_id,classifier_release_id,ai_decision_id,configured_mode,effective_mode,profile_id,policy_digest,suggested_action,effective_action,reason_code,hard_pass_reason,enforce_approval_id,created_at,decided_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET observed_event_id=excluded.observed_event_id,route_observation_id=excluded.route_observation_id,source_id=excluded.source_id,target_id=excluded.target_id,subscription_id=excluded.subscription_id,classifier_release_id=excluded.classifier_release_id,ai_decision_id=excluded.ai_decision_id,configured_mode=excluded.configured_mode,effective_mode=excluded.effective_mode,profile_id=excluded.profile_id,policy_digest=excluded.policy_digest,suggested_action=excluded.suggested_action,effective_action=excluded.effective_action,reason_code=excluded.reason_code,hard_pass_reason=excluded.hard_pass_reason,enforce_approval_id=excluded.enforce_approval_id,decided_at=excluded.decided_at`,
		value.ID, value.EventID, value.ObservedEventID, value.RouteObservationID, value.SourceID, value.TargetID, value.SubscriptionID, value.ClassifierReleaseID, value.AIDecisionID, value.ConfiguredMode, value.EffectiveMode, value.ProfileID, value.PolicyDigest, value.SuggestedAction, value.EffectiveAction, value.ReasonCode, value.HardPassReason, value.EnforceApprovalID, value.CreatedAt.Unix(), value.DecidedAt.Unix())
	if err != nil {
		return fmt.Errorf("platformdb: put route decision: %w", err)
	}
	return nil
}

func scanRouteDecision(row interface{ Scan(...any) error }) (RouteDecisionRecord, error) {
	var value RouteDecisionRecord
	var created, decided int64
	var observed, route, source, target, subscription, release, ai, profile, digest, approval sql.NullString
	if err := row.Scan(&value.ID, &value.EventID, &observed, &route, &source, &target, &subscription, &release, &ai, &value.ConfiguredMode, &value.EffectiveMode, &profile, &digest, &value.SuggestedAction, &value.EffectiveAction, &value.ReasonCode, &value.HardPassReason, &approval, &created, &decided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RouteDecisionRecord{}, ErrRouteDecisionNotFound
		}
		return RouteDecisionRecord{}, err
	}
	value.ObservedEventID, value.RouteObservationID, value.SourceID, value.TargetID, value.SubscriptionID = observed.String, route.String, source.String, target.String, subscription.String
	value.ClassifierReleaseID, value.AIDecisionID, value.ProfileID, value.PolicyDigest, value.EnforceApprovalID = release.String, ai.String, profile.String, digest.String, approval.String
	value.CreatedAt, value.DecidedAt = time.Unix(created, 0).UTC(), time.Unix(decided, 0).UTC()
	return value, nil
}

func (r *Phase5Repository) RouteDecision(ctx context.Context, id string) (RouteDecisionRecord, error) {
	if err := r.require(); err != nil {
		return RouteDecisionRecord{}, err
	}
	return scanRouteDecision(r.store.db.QueryRowContext(phase5Context(ctx), `SELECT id,event_id,observed_event_id,route_observation_id,source_id,target_id,subscription_id,classifier_release_id,ai_decision_id,configured_mode,effective_mode,profile_id,policy_digest,suggested_action,effective_action,reason_code,hard_pass_reason,enforce_approval_id,created_at,decided_at FROM route_decisions WHERE id=?`, strings.TrimSpace(id)))
}

func (r *Phase5Repository) RouteDecisionsForEvent(ctx context.Context, eventID string) ([]RouteDecisionRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(phase5Context(ctx), `SELECT id,event_id,observed_event_id,route_observation_id,source_id,target_id,subscription_id,classifier_release_id,ai_decision_id,configured_mode,effective_mode,profile_id,policy_digest,suggested_action,effective_action,reason_code,hard_pass_reason,enforce_approval_id,created_at,decided_at FROM route_decisions WHERE event_id=? ORDER BY created_at,id`, strings.TrimSpace(eventID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]RouteDecisionRecord, 0)
	for rows.Next() {
		value, scanErr := scanRouteDecision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ListRouteDecisions returns a bounded, newest-first page. It is intentionally
// cursor-friendly: callers can pass createdAt/id from the last row without
// relying on OFFSET, so retention pruning cannot reorder a long-lived page.
func (r *Phase5Repository) ListRouteDecisions(ctx context.Context, limit int, before *time.Time, beforeID string) ([]RouteDecisionRecord, bool, error) {
	if err := r.require(); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	clauses := []string{}
	args := []any{}
	if before != nil {
		stamp := before.UTC().Unix()
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, stamp, stamp, strings.TrimSpace(beforeID))
	}
	query := `SELECT id,event_id,observed_event_id,route_observation_id,source_id,target_id,subscription_id,classifier_release_id,ai_decision_id,configured_mode,effective_mode,profile_id,policy_digest,suggested_action,effective_action,reason_code,hard_pass_reason,enforce_approval_id,created_at,decided_at FROM route_decisions`
	if len(clauses) != 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := r.store.db.QueryContext(phase5Context(ctx), query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]RouteDecisionRecord, 0, limit)
	for rows.Next() {
		value, scanErr := scanRouteDecision(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasNext := len(values) > limit
	if hasNext {
		values = values[:limit]
	}
	return values, hasNext, nil
}

// PersistDrop atomically records the authoritative DROP and the replayable
// snapshot. The caller must have completed all policy/approval/hard-safety
// checks before entering this boundary. A failed transaction leaves the
// caller free to fail open and send the original message.
func (r *Phase5Repository) PersistDrop(ctx context.Context, decision RouteDecisionRecord, snapshot ReplayableEventRecord) error {
	return r.persistDrop(ctx, decision, snapshot, nil)
}

// PersistDropIfAllowed is the final ENFORCE linearization boundary. It
// rechecks the durable emergency flag, approval identity/revocation and the
// semantic digests in the same SQLite transaction that inserts the DROP and
// replay snapshot. If an operator commits disable/revocation first, this
// method rejects the DROP and the caller must fail open to PASS.
func (r *Phase5Repository) PersistDropIfAllowed(ctx context.Context, decision RouteDecisionRecord, snapshot ReplayableEventRecord, approvalID, policyDigest, profileDigest string, now time.Time) error {
	guard := &dropCommitGuard{ApprovalID: strings.TrimSpace(approvalID), PolicyDigest: strings.TrimSpace(policyDigest), ProfileDigest: strings.TrimSpace(profileDigest), At: phase5Time(now)}
	return r.persistDrop(ctx, decision, snapshot, guard)
}

type dropCommitGuard struct {
	ApprovalID    string
	PolicyDigest  string
	ProfileDigest string
	At            time.Time
}

func (r *Phase5Repository) persistDrop(ctx context.Context, decision RouteDecisionRecord, snapshot ReplayableEventRecord, guard *dropCommitGuard) error {
	if err := r.require(); err != nil {
		return err
	}
	if decision.EffectiveAction != "drop" || decision.SuggestedAction != "drop" || snapshot.SchemaVersion != ReplaySnapshotSchemaVersion || len(snapshot.SnapshotJSON) == 0 || !json.Valid(snapshot.SnapshotJSON) {
		return ErrApprovalInvalid
	}
	if snapshot.RouteDecisionID == "" {
		snapshot.RouteDecisionID = decision.ID
	}
	if snapshot.EventID == "" {
		snapshot.EventID = decision.EventID
	}
	if err := validateSnapshotIdentity(decision, snapshot); err != nil {
		return ErrApprovalInvalid
	}
	if snapshot.ID == "" {
		snapshot.ID = newPhase5ID("replay_")
	}
	decision.CreatedAt, decision.DecidedAt = phase5Time(decision.CreatedAt), phase5Time(decision.DecidedAt)
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = decision.DecidedAt
	}
	if snapshot.ExpiresAt.IsZero() {
		snapshot.ExpiresAt = snapshot.CreatedAt.Add(ReplayRetention)
	}
	tx, err := r.store.db.BeginTx(phase5Context(ctx), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if guard != nil {
		if decision.EffectiveMode != "enforce" || decision.EnforceApprovalID != guard.ApprovalID || decision.PolicyDigest != guard.PolicyDigest || guard.ApprovalID == "" || guard.PolicyDigest == "" || guard.ProfileDigest == "" {
			return ErrApprovalInvalid
		}
		var emergency string
		err := tx.QueryRowContext(phase5Context(ctx), `SELECT value FROM release_metadata WHERE key='enforce_emergency_disabled'`).Scan(&emergency)
		if err == nil && strings.EqualFold(strings.TrimSpace(emergency), "true") {
			return ErrEmergencyDisabled
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var approvedAt int64
		var revoked sql.NullInt64
		err = tx.QueryRowContext(phase5Context(ctx), `SELECT approved_at,revoked_at FROM enforce_approvals WHERE id=? AND classifier_release_id=? AND policy_digest=? AND profile_digest=?`, guard.ApprovalID, decision.ClassifierReleaseID, guard.PolicyDigest, guard.ProfileDigest).Scan(&approvedAt, &revoked)
		if errors.Is(err, sql.ErrNoRows) || err != nil || revoked.Valid || time.Unix(approvedAt, 0).UTC().After(guard.At) {
			return ErrApprovalInvalid
		}
		// The approval is bound to the release that was active when it was
		// issued.  Activating a different release must invalidate an in-flight
		// DROP even when the old approval row has not been explicitly revoked.
		var releaseActive int
		if err := tx.QueryRowContext(phase5Context(ctx), `SELECT active FROM classifier_releases WHERE id=?`, decision.ClassifierReleaseID).Scan(&releaseActive); err != nil || releaseActive != 1 {
			return ErrApprovalInvalid
		}
		var critical int64
		if err := tx.QueryRowContext(phase5Context(ctx), `SELECT COUNT(*) FROM feedback f JOIN route_decisions d ON d.id=f.route_decision_id JOIN ai_decisions a ON a.id=d.ai_decision_id WHERE d.classifier_release_id=? AND f.feedback_type='false_drop' AND f.resolved=0 AND json_extract(a.classification_json,'$.importance')='critical'`, decision.ClassifierReleaseID).Scan(&critical); err != nil {
			return err
		}
		if critical > 0 {
			return ErrApprovalInvalid
		}
	}
	if _, err = tx.ExecContext(phase5Context(ctx), `INSERT INTO route_decisions
(id,event_id,observed_event_id,route_observation_id,source_id,target_id,subscription_id,classifier_release_id,ai_decision_id,configured_mode,effective_mode,profile_id,policy_digest,suggested_action,effective_action,reason_code,hard_pass_reason,enforce_approval_id,created_at,decided_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, decision.ID, decision.EventID, decision.ObservedEventID, decision.RouteObservationID, decision.SourceID, decision.TargetID, decision.SubscriptionID, decision.ClassifierReleaseID, decision.AIDecisionID, decision.ConfiguredMode, decision.EffectiveMode, decision.ProfileID, decision.PolicyDigest, decision.SuggestedAction, decision.EffectiveAction, decision.ReasonCode, decision.HardPassReason, decision.EnforceApprovalID, decision.CreatedAt.Unix(), decision.DecidedAt.Unix()); err != nil {
		return fmt.Errorf("platformdb: persist drop decision: %w", err)
	}
	if _, err = tx.ExecContext(phase5Context(ctx), `INSERT INTO replayable_events (id,schema_version,route_decision_id,event_id,observed_event_id,source_id,target_id,subscription_id,event_type,snapshot_json,original_classification_ref,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, snapshot.ID, snapshot.SchemaVersion, snapshot.RouteDecisionID, snapshot.EventID, snapshot.ObservedEventID, snapshot.SourceID, snapshot.TargetID, snapshot.SubscriptionID, snapshot.EventType, string(snapshot.SnapshotJSON), snapshot.OriginalClassificationRef, snapshot.CreatedAt.Unix(), snapshot.ExpiresAt.Unix()); err != nil {
		return fmt.Errorf("platformdb: persist replay snapshot: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *Phase5Repository) PutReplayableEvent(ctx context.Context, value ReplayableEventRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.SchemaVersion == 0 {
		value.SchemaVersion = ReplaySnapshotSchemaVersion
	}
	if value.SchemaVersion != ReplaySnapshotSchemaVersion || strings.TrimSpace(value.RouteDecisionID) == "" || strings.TrimSpace(value.EventID) == "" || len(value.SnapshotJSON) == 0 || !json.Valid(value.SnapshotJSON) {
		return ErrReplayNotFound
	}
	decision, err := r.RouteDecision(ctx, value.RouteDecisionID)
	if err != nil {
		return err
	}
	if err := validateSnapshotIdentity(decision, value); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID = newPhase5ID("replay_")
	}
	value.CreatedAt = phase5Time(value.CreatedAt)
	if value.ExpiresAt.IsZero() {
		value.ExpiresAt = value.CreatedAt.Add(ReplayRetention)
	}
	_, err = r.store.db.ExecContext(phase5Context(ctx), `INSERT INTO replayable_events (id,schema_version,route_decision_id,event_id,observed_event_id,source_id,target_id,subscription_id,event_type,snapshot_json,original_classification_ref,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(route_decision_id) DO UPDATE SET snapshot_json=excluded.snapshot_json,expires_at=excluded.expires_at`, value.ID, value.SchemaVersion, value.RouteDecisionID, value.EventID, value.ObservedEventID, value.SourceID, value.TargetID, value.SubscriptionID, value.EventType, string(value.SnapshotJSON), value.OriginalClassificationRef, value.CreatedAt.Unix(), value.ExpiresAt.Unix())
	return err
}

func (r *Phase5Repository) ReplayableEvent(ctx context.Context, routeDecisionID string) (ReplayableEventRecord, error) {
	if err := r.require(); err != nil {
		return ReplayableEventRecord{}, err
	}
	var value ReplayableEventRecord
	var observed, source, target, subscription, class sql.NullString
	var snapshot string
	var created, expires int64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT id,schema_version,route_decision_id,event_id,observed_event_id,source_id,target_id,subscription_id,event_type,snapshot_json,original_classification_ref,created_at,expires_at FROM replayable_events WHERE route_decision_id=?`, strings.TrimSpace(routeDecisionID)).Scan(&value.ID, &value.SchemaVersion, &value.RouteDecisionID, &value.EventID, &observed, &source, &target, &subscription, &value.EventType, &snapshot, &class, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplayableEventRecord{}, ErrReplayNotFound
	}
	if err != nil {
		return ReplayableEventRecord{}, err
	}
	value.ObservedEventID, value.SourceID, value.TargetID, value.SubscriptionID, value.OriginalClassificationRef = observed.String, source.String, target.String, subscription.String, class.String
	value.SnapshotJSON = json.RawMessage(snapshot)
	value.CreatedAt, value.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	return value, nil
}

func (r *Phase5Repository) PruneReplayableEvents(ctx context.Context, now time.Time, limit int) (int, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 256
	}
	stamp := phase5Time(now).Unix()
	result, err := r.store.db.ExecContext(phase5Context(ctx), `DELETE FROM replayable_events WHERE id IN (SELECT id FROM replayable_events WHERE expires_at <= ? ORDER BY expires_at,id LIMIT ?)`, stamp, limit)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
}

func validDeliveryStatus(value domain.DeliveryStatus) bool {
	switch value {
	case domain.DeliveryPlanned, domain.DeliverySending, domain.DeliveryMigrationHeld, domain.DeliveryQueued, domain.DeliverySent, domain.DeliveryPartial, domain.DeliveryNotSent, domain.DeliveryUnknown, domain.DeliveryRejected, domain.DeliveryExpired, domain.DeliveryAbandonedRestart, domain.DeliverySkippedEmpty:
		return true
	}
	return false
}

func (r *Phase5Repository) CreateDelivery(ctx context.Context, value DeliveryRecord) error {
	_, err := r.CreateDeliveryRecord(ctx, value)
	return err
}

// CreateDeliveryRecord is the value-returning form used by replay and other
// callers that need the generated durable ID without a racy follow-up query.
func (r *Phase5Repository) CreateDeliveryRecord(ctx context.Context, value DeliveryRecord) (DeliveryRecord, error) {
	if err := r.require(); err != nil {
		return DeliveryRecord{}, err
	}
	if strings.TrimSpace(value.EventID) == "" || strings.TrimSpace(value.TargetID) == "" || strings.TrimSpace(value.ExternalID) == "" || !validDeliveryStatus(value.Status) {
		return DeliveryRecord{}, ErrDeliveryNotFound
	}
	if value.ID == "" {
		value.ID = newPhase5ID("delivery_")
	}
	if value.TargetType == "" {
		value.TargetType = "group"
	}
	if value.Attempt <= 0 {
		value.Attempt = 1
	}
	if value.InitiatedBy == "" {
		value.InitiatedBy = "system"
	}
	value.CreatedAt = phase5Time(value.CreatedAt)
	value.UpdatedAt = phase5Time(value.UpdatedAt)
	_, err := r.store.db.ExecContext(phase5Context(ctx), `INSERT INTO deliveries (id,route_decision_id,event_id,target_id,connector_id,target_type,external_id,status,result_code,remote_message_id,attempt,replay_of_route_decision_id,initiated_by,message_snapshot_json,created_at,sending_at,completed_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.RouteDecisionID, value.EventID, value.TargetID, value.ConnectorID, value.TargetType, value.ExternalID, value.Status, value.ResultCode, value.RemoteMessageID, value.Attempt, value.ReplayOfRouteDecisionID, value.InitiatedBy, value.MessageSnapshotJSON, value.CreatedAt.Unix(), nullableTime(value.SendingAt), nullableTime(value.CompletedAt), value.UpdatedAt.Unix())
	if err != nil {
		return DeliveryRecord{}, fmt.Errorf("platformdb: create delivery: %w", err)
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Unix()
}

func scanDelivery(row interface{ Scan(...any) error }) (DeliveryRecord, error) {
	var value DeliveryRecord
	var route, connector, remote, replay, snapshot sql.NullString
	var created, updated int64
	var sending, completed sql.NullInt64
	err := row.Scan(&value.ID, &route, &value.EventID, &value.TargetID, &connector, &value.TargetType, &value.ExternalID, &value.Status, &value.ResultCode, &remote, &value.Attempt, &replay, &value.InitiatedBy, &snapshot, &created, &sending, &completed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryRecord{}, ErrDeliveryNotFound
	}
	if err != nil {
		return DeliveryRecord{}, err
	}
	value.RouteDecisionID, value.ConnectorID, value.RemoteMessageID, value.ReplayOfRouteDecisionID, value.MessageSnapshotJSON = route.String, connector.String, remote.String, replay.String, snapshot.String
	value.CreatedAt, value.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if sending.Valid {
		v := time.Unix(sending.Int64, 0).UTC()
		value.SendingAt = &v
	}
	if completed.Valid {
		v := time.Unix(completed.Int64, 0).UTC()
		value.CompletedAt = &v
	}
	return value, nil
}

const deliverySelect = `SELECT id,route_decision_id,event_id,target_id,connector_id,target_type,external_id,status,result_code,remote_message_id,attempt,replay_of_route_decision_id,initiated_by,message_snapshot_json,created_at,sending_at,completed_at,updated_at FROM deliveries`

func (r *Phase5Repository) Delivery(ctx context.Context, id string) (DeliveryRecord, error) {
	if err := r.require(); err != nil {
		return DeliveryRecord{}, err
	}
	return scanDelivery(r.store.db.QueryRowContext(phase5Context(ctx), deliverySelect+" WHERE id=?", strings.TrimSpace(id)))
}

func (r *Phase5Repository) ListDeliveriesForEvent(ctx context.Context, eventID string) ([]DeliveryRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(phase5Context(ctx), deliverySelect+" WHERE event_id=? ORDER BY created_at,id", strings.TrimSpace(eventID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DeliveryRecord, 0)
	for rows.Next() {
		v, e := scanDelivery(rows)
		if e != nil {
			return nil, e
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (r *Phase5Repository) ListDeliveries(ctx context.Context, limit int, before *time.Time, beforeID string) ([]DeliveryRecord, bool, error) {
	if err := r.require(); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	clauses := []string{}
	args := []any{}
	if before != nil {
		stamp := before.UTC().Unix()
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, stamp, stamp, strings.TrimSpace(beforeID))
	}
	query := deliverySelect
	if len(clauses) != 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := r.store.db.QueryContext(phase5Context(ctx), query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]DeliveryRecord, 0, limit)
	for rows.Next() {
		value, scanErr := scanDelivery(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasNext := len(values) > limit
	if hasNext {
		values = values[:limit]
	}
	return values, hasNext, nil
}

func (r *Phase5Repository) TransitionDelivery(ctx context.Context, id string, status domain.DeliveryStatus, resultCode, remoteID string, at time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if !validDeliveryStatus(status) {
		return ErrDeliveryNotFound
	}
	stamp := phase5Time(at)
	trimmedID := strings.TrimSpace(id)
	// Keep the read and write in one transaction. The platform store is a
	// single SQLite owner, but callbacks can still race a restart or another
	// callback; the compare-and-set update prevents a stale transition from
	// rewriting a terminal result.
	tx, err := r.store.db.BeginTx(phase5Context(ctx), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var current domain.DeliveryStatus
	if err := tx.QueryRowContext(phase5Context(ctx), "SELECT status FROM deliveries WHERE id=?", trimmedID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeliveryNotFound
		}
		return err
	}
	if !validDeliveryTransition(current, status) {
		return ErrDeliveryTransition
	}
	var sending any
	var completed any
	if status == domain.DeliverySending {
		sending = stamp.Unix()
	}
	if status == domain.DeliverySent || status == domain.DeliveryPartial || status == domain.DeliveryNotSent || status == domain.DeliveryUnknown || status == domain.DeliveryRejected || status == domain.DeliveryExpired || status == domain.DeliveryAbandonedRestart || status == domain.DeliverySkippedEmpty {
		completed = stamp.Unix()
	}
	res, err := tx.ExecContext(phase5Context(ctx), `UPDATE deliveries SET status=?,result_code=?,remote_message_id=?,sending_at=COALESCE(sending_at,?),completed_at=?,updated_at=? WHERE id=? AND status=?`, status, resultCode, remoteID, sending, completed, stamp.Unix(), trimmedID, current)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrDeliveryTransition
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// validDeliveryTransition keeps the result history monotonic. A planned
// delivery may be held by connector migration or enter the send boundary;
// terminal states cannot be rewritten except by an explicit manual retry,
// which creates a new delivery row.
func validDeliveryTransition(from, to domain.DeliveryStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case domain.DeliveryPlanned:
		return to == domain.DeliverySending || to == domain.DeliveryMigrationHeld || to == domain.DeliveryQueued || to == domain.DeliverySkippedEmpty || to == domain.DeliveryNotSent || to == domain.DeliveryRejected || to == domain.DeliveryUnknown || to == domain.DeliveryExpired
	case domain.DeliveryMigrationHeld:
		return to == domain.DeliveryPlanned || to == domain.DeliverySending || to == domain.DeliveryQueued || to == domain.DeliveryNotSent || to == domain.DeliveryRejected || to == domain.DeliveryUnknown || to == domain.DeliveryAbandonedRestart
	case domain.DeliveryQueued:
		return to == domain.DeliverySending || to == domain.DeliveryNotSent || to == domain.DeliveryRejected || to == domain.DeliveryUnknown || to == domain.DeliveryExpired
	case domain.DeliverySending:
		return to == domain.DeliverySent || to == domain.DeliveryPartial || to == domain.DeliveryNotSent || to == domain.DeliveryUnknown || to == domain.DeliveryRejected || to == domain.DeliveryAbandonedRestart || to == domain.DeliveryQueued
	default:
		return false
	}
}

func (r *Phase5Repository) MarkSendingAbandonedOnRestart(ctx context.Context, now time.Time) (int, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	res, err := r.store.db.ExecContext(phase5Context(ctx), `UPDATE deliveries SET status='abandoned_restart',result_code='abandoned_restart',completed_at=?,updated_at=? WHERE status='sending'`, phase5Time(now).Unix(), phase5Time(now).Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (r *Phase5Repository) ManualRetry(ctx context.Context, id, initiatedBy string, now time.Time) (DeliveryRecord, error) {
	if err := r.require(); err != nil {
		return DeliveryRecord{}, err
	}
	value, err := r.Delivery(ctx, id)
	if err != nil {
		return DeliveryRecord{}, err
	}
	if value.Status != domain.DeliveryNotSent && value.Status != domain.DeliveryRejected {
		return DeliveryRecord{}, ErrDeliveryRetryNotAllowed
	}
	newID := newPhase5ID("delivery_")
	value.ID = newID
	value.Status = domain.DeliveryPlanned
	value.ResultCode = ""
	value.RemoteMessageID = ""
	value.Attempt++
	value.InitiatedBy = strings.TrimSpace(initiatedBy)
	if value.InitiatedBy == "" {
		value.InitiatedBy = "admin"
	}
	value.CreatedAt = phase5Time(now)
	value.UpdatedAt = value.CreatedAt
	value.SendingAt = nil
	value.CompletedAt = nil
	if err := r.CreateDelivery(ctx, value); err != nil {
		return DeliveryRecord{}, err
	}
	return r.Delivery(ctx, newID)
}

func validFeedback(value string) bool {
	switch value {
	case "correct_pass", "correct_drop", "false_drop", "false_pass", "uncertain_review":
		return true
	}
	return false
}

func (r *Phase5Repository) CreateFeedback(ctx context.Context, value FeedbackRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if strings.TrimSpace(value.RouteDecisionID) == "" || strings.TrimSpace(value.ReviewedBy) == "" || !validFeedback(value.FeedbackType) {
		return ErrFeedbackNotFound
	}
	if value.ID == "" {
		value.ID = newPhase5ID("feedback_")
	}
	value.CreatedAt = phase5Time(value.CreatedAt)
	value.UpdatedAt = phase5Time(value.UpdatedAt)
	_, err := r.store.db.ExecContext(phase5Context(ctx), `INSERT INTO feedback (id,route_decision_id,ai_decision_id,feedback_type,reviewed_by,notes,resolved,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, value.ID, value.RouteDecisionID, nullIfEmpty(value.AIDecisionID), value.FeedbackType, value.ReviewedBy, value.Notes, boolInt(value.Resolved), value.CreatedAt.Unix(), value.UpdatedAt.Unix())
	return err
}

func (r *Phase5Repository) FeedbackForRoute(ctx context.Context, id string) ([]FeedbackRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(phase5Context(ctx), `SELECT id,route_decision_id,ai_decision_id,feedback_type,reviewed_by,notes,resolved,created_at,updated_at FROM feedback WHERE route_decision_id=? ORDER BY created_at,id`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]FeedbackRecord, 0)
	for rows.Next() {
		var v FeedbackRecord
		var ai sql.NullString
		var resolved, created, updated int64
		if e := rows.Scan(&v.ID, &v.RouteDecisionID, &ai, &v.FeedbackType, &v.ReviewedBy, &v.Notes, &resolved, &created, &updated); e != nil {
			return nil, e
		}
		v.AIDecisionID = ai.String
		v.Resolved = resolved != 0
		v.CreatedAt, v.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		values = append(values, v)
	}
	return values, rows.Err()
}

func (r *Phase5Repository) Feedback(ctx context.Context, id string) (FeedbackRecord, error) {
	if err := r.require(); err != nil {
		return FeedbackRecord{}, err
	}
	var value FeedbackRecord
	var ai sql.NullString
	var resolved, created, updated int64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT id,route_decision_id,ai_decision_id,feedback_type,reviewed_by,notes,resolved,created_at,updated_at FROM feedback WHERE id=?`, strings.TrimSpace(id)).Scan(&value.ID, &value.RouteDecisionID, &ai, &value.FeedbackType, &value.ReviewedBy, &value.Notes, &resolved, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return FeedbackRecord{}, ErrFeedbackNotFound
	}
	if err != nil {
		return FeedbackRecord{}, err
	}
	value.AIDecisionID = ai.String
	value.Resolved = resolved != 0
	value.CreatedAt, value.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return value, nil
}

// ListFeedback returns a bounded newest-first page. Feedback is retained
// longer than route/delivery rows, so callers must use the cursor rather than
// OFFSET when traversing the review history.
func (r *Phase5Repository) ListFeedback(ctx context.Context, limit int, before *time.Time, beforeID string) ([]FeedbackRecord, bool, error) {
	if err := r.require(); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	clauses := []string{}
	args := []any{}
	if before != nil {
		stamp := before.UTC().Unix()
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, stamp, stamp, strings.TrimSpace(beforeID))
	}
	query := `SELECT id,route_decision_id,ai_decision_id,feedback_type,reviewed_by,notes,resolved,created_at,updated_at FROM feedback`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := r.store.db.QueryContext(phase5Context(ctx), query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]FeedbackRecord, 0, limit)
	for rows.Next() {
		var value FeedbackRecord
		var ai sql.NullString
		var resolved, created, updated int64
		if err := rows.Scan(&value.ID, &value.RouteDecisionID, &ai, &value.FeedbackType, &value.ReviewedBy, &value.Notes, &resolved, &created, &updated); err != nil {
			return nil, false, err
		}
		value.AIDecisionID = ai.String
		value.Resolved = resolved != 0
		value.CreatedAt, value.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasNext := len(values) > limit
	if hasNext {
		values = values[:limit]
	}
	return values, hasNext, nil
}

func (r *Phase5Repository) UpdateFeedback(ctx context.Context, id, feedbackType, notes string, resolved bool, updatedAt time.Time) (FeedbackRecord, error) {
	if err := r.require(); err != nil {
		return FeedbackRecord{}, err
	}
	if !validFeedback(feedbackType) {
		return FeedbackRecord{}, ErrFeedbackNotFound
	}
	result, err := r.store.db.ExecContext(phase5Context(ctx), `UPDATE feedback SET feedback_type=?,notes=?,resolved=?,updated_at=? WHERE id=?`, feedbackType, notes, boolInt(resolved), phase5Time(updatedAt).Unix(), strings.TrimSpace(id))
	if err != nil {
		return FeedbackRecord{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return FeedbackRecord{}, ErrFeedbackNotFound
	}
	return r.Feedback(ctx, id)
}

func (r *Phase5Repository) FeedbackMetrics(ctx context.Context) (knownImportantFalseDrops int64, unresolvedCriticalFalseDrops int64, reviewedSuggestedDrops int64, err error) {
	if err = r.require(); err != nil {
		return
	}
	var releaseID string
	if scanErr := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COALESCE((SELECT id FROM classifier_releases WHERE active=1 LIMIT 1),'')`).Scan(&releaseID); scanErr != nil {
		err = scanErr
		return
	}
	err = r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COUNT(DISTINCT f.route_decision_id) FROM feedback f JOIN route_decisions d ON d.id=f.route_decision_id AND d.classifier_release_id=? JOIN ai_decisions a ON a.id=d.ai_decision_id AND a.classifier_release_id=? WHERE f.feedback_type='false_drop' AND json_extract(a.classification_json,'$.importance') IN ('high','critical')`, releaseID, releaseID).Scan(&knownImportantFalseDrops)
	if err != nil {
		return
	}
	err = r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COUNT(DISTINCT f.route_decision_id) FROM feedback f JOIN route_decisions d ON d.id=f.route_decision_id AND d.classifier_release_id=? JOIN ai_decisions a ON a.id=d.ai_decision_id AND a.classifier_release_id=? WHERE f.feedback_type='false_drop' AND f.resolved=0 AND json_extract(a.classification_json,'$.importance')='critical'`, releaseID, releaseID).Scan(&unresolvedCriticalFalseDrops)
	if err != nil {
		return
	}
	err = r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COUNT(DISTINCT d.id) FROM route_decisions d JOIN ai_decisions a ON a.id=d.ai_decision_id AND a.classifier_release_id=? WHERE d.classifier_release_id=? AND d.suggested_action='drop' AND a.reviewed=1`, releaseID, releaseID).Scan(&reviewedSuggestedDrops)
	return
}

func (r *Phase5Repository) SaveEnforceApproval(ctx context.Context, value EnforceApprovalRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" || value.ClassifierReleaseID == "" || value.PolicyDigest == "" || value.ProfileDigest == "" || value.ApprovedBy == "" {
		return ErrApprovalInvalid
	}
	if len(value.ReadinessEvidenceJSON) == 0 {
		value.ReadinessEvidenceJSON = []byte(`{}`)
	}
	if !json.Valid(value.ReadinessEvidenceJSON) {
		return ErrApprovalInvalid
	}
	value.ApprovedAt = phase5Time(value.ApprovedAt)
	value.CreatedAt = phase5Time(value.CreatedAt)
	tx, err := r.store.db.BeginTx(phase5Context(ctx), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(phase5Context(ctx), `UPDATE enforce_approvals SET revoked_at=COALESCE(revoked_at,?) WHERE revoked_at IS NULL AND (classifier_release_id<>? OR policy_digest<>? OR profile_digest<>?)`, value.ApprovedAt.Unix(), value.ClassifierReleaseID, value.PolicyDigest, value.ProfileDigest); err != nil {
		return err
	}
	if _, err = tx.ExecContext(phase5Context(ctx), `INSERT INTO enforce_approvals (id,classifier_release_id,policy_digest,profile_digest,readiness_evidence_json,approved_at,approved_by,reason,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, value.ID, value.ClassifierReleaseID, value.PolicyDigest, value.ProfileDigest, string(value.ReadinessEvidenceJSON), value.ApprovedAt.Unix(), value.ApprovedBy, value.Reason, value.CreatedAt.Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *Phase5Repository) ValidEnforceApproval(ctx context.Context, releaseID, policyDigest, profileDigest string, now time.Time) (EnforceApprovalRecord, error) {
	if err := r.require(); err != nil {
		return EnforceApprovalRecord{}, err
	}
	var v EnforceApprovalRecord
	var evidence string
	var approved, created int64
	var revoked sql.NullInt64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT a.id,a.classifier_release_id,a.policy_digest,a.profile_digest,a.readiness_evidence_json,a.approved_at,a.approved_by,a.revoked_at,a.reason,a.created_at
FROM enforce_approvals a
JOIN classifier_releases cr ON cr.id=a.classifier_release_id AND cr.active=1
WHERE a.classifier_release_id=? AND a.policy_digest=? AND a.profile_digest=? AND a.revoked_at IS NULL
ORDER BY a.approved_at DESC LIMIT 1`, strings.TrimSpace(releaseID), strings.TrimSpace(policyDigest), strings.TrimSpace(profileDigest)).Scan(&v.ID, &v.ClassifierReleaseID, &v.PolicyDigest, &v.ProfileDigest, &evidence, &approved, &v.ApprovedBy, &revoked, &v.Reason, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return EnforceApprovalRecord{}, ErrApprovalNotFound
	}
	if err != nil {
		return EnforceApprovalRecord{}, err
	}
	v.ReadinessEvidenceJSON = []byte(evidence)
	v.ApprovedAt, v.CreatedAt = time.Unix(approved, 0).UTC(), time.Unix(created, 0).UTC()
	if revoked.Valid {
		x := time.Unix(revoked.Int64, 0).UTC()
		v.RevokedAt = &x
	}
	// An approval is valid from the instant it is committed.  The injected
	// clock used by deterministic callers can legitimately equal ApprovedAt;
	// only an approval dated in the future is invalid.
	if v.ApprovedAt.After(phase5Time(now)) {
		return EnforceApprovalRecord{}, ErrApprovalInvalid
	}
	// A newly recorded unresolved critical false-DROP invalidates every
	// approval. This check is intentionally performed at the DROP boundary,
	// not only when an approval is created, so an operator cannot continue to
	// suppress important content after a review arrives.
	var critical int64
	if err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COUNT(*) FROM feedback f JOIN route_decisions d ON d.id=f.route_decision_id JOIN ai_decisions a ON a.id=d.ai_decision_id AND a.classifier_release_id=? WHERE d.classifier_release_id=? AND f.feedback_type='false_drop' AND f.resolved=0 AND json_extract(a.classification_json,'$.importance')='critical'`, releaseID, releaseID).Scan(&critical); err != nil {
		return EnforceApprovalRecord{}, err
	}
	if critical > 0 {
		return EnforceApprovalRecord{}, ErrApprovalInvalid
	}
	return v, nil
}

func (r *Phase5Repository) RevokeEnforceApprovals(ctx context.Context, reason string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	stamp := phase5Time(now).Unix()
	_, err := r.store.db.ExecContext(phase5Context(ctx), `UPDATE enforce_approvals SET revoked_at=?,reason=? WHERE revoked_at IS NULL`, stamp, strings.TrimSpace(reason))
	return err
}

// EnforceEmergencyDisabled is durable operational state. It is stored as a
// small release_metadata value rather than a new mutable schema so the kill
// switch survives a restart without introducing a second configuration source.
func (r *Phase5Repository) EnforceEmergencyDisabled(ctx context.Context) (bool, error) {
	if err := r.require(); err != nil {
		return false, err
	}
	var value string
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT value FROM release_metadata WHERE key='enforce_emergency_disabled'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), "true"), nil
}

func (r *Phase5Repository) SetEnforceEmergencyDisabled(ctx context.Context, disabled bool, at time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	value := "false"
	if disabled {
		value = "true"
	}
	_, err := r.store.db.ExecContext(phase5Context(ctx), `INSERT INTO release_metadata(key,value,updated_at) VALUES('enforce_emergency_disabled',?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, value, phase5Time(at).Unix())
	return err
}

func (r *Phase5Repository) RetentionMetadata(ctx context.Context) (map[string]int64, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(phase5Context(ctx), `SELECT domain,retention_days FROM retention_metadata ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var domain string
		var days int64
		if err := rows.Scan(&domain, &days); err != nil {
			return nil, err
		}
		result[domain] = days
	}
	return result, rows.Err()
}

func (r *Phase5Repository) AddMediaCacheEntry(ctx context.Context, value MediaCacheEntryRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	if value.ID == "" || value.SHA256 == "" || value.SizeBytes < 0 || value.MIMEType == "" || value.StorageKey == "" {
		return ErrPhase5Unavailable
	}
	value.CreatedAt = phase5Time(value.CreatedAt)
	value.ExpiresAt = phase5Time(value.ExpiresAt)
	value.LastAccessedAt = phase5Time(value.LastAccessedAt)
	_, err := r.store.db.ExecContext(phase5Context(ctx), `INSERT INTO media_cache_entries (id,sha256,size_bytes,mime_type,storage_key,created_at,expires_at,last_accessed_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(sha256) DO UPDATE SET last_accessed_at=excluded.last_accessed_at,expires_at=excluded.expires_at`, value.ID, value.SHA256, value.SizeBytes, value.MIMEType, value.StorageKey, value.CreatedAt.Unix(), value.ExpiresAt.Unix(), value.LastAccessedAt.Unix())
	return err
}

func (r *Phase5Repository) LinkMediaCache(ctx context.Context, value MediaCacheLinkRecord) error {
	if err := r.require(); err != nil {
		return err
	}
	value.CreatedAt = phase5Time(value.CreatedAt)
	_, err := r.store.db.ExecContext(phase5Context(ctx), `INSERT OR IGNORE INTO media_cache_links (entry_id,route_decision_id,event_id,source_url,created_at) VALUES (?,?,?,?,?)`, value.EntryID, value.RouteDecisionID, value.EventID, value.SourceURL, value.CreatedAt.Unix())
	return err
}

func (r *Phase5Repository) MediaCacheEntryBySHA(ctx context.Context, sha string) (MediaCacheEntryRecord, error) {
	if err := r.require(); err != nil {
		return MediaCacheEntryRecord{}, err
	}
	var value MediaCacheEntryRecord
	var created, expires, accessed int64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT id,sha256,size_bytes,mime_type,storage_key,created_at,expires_at,last_accessed_at FROM media_cache_entries WHERE sha256=?`, strings.TrimSpace(sha)).Scan(&value.ID, &value.SHA256, &value.SizeBytes, &value.MIMEType, &value.StorageKey, &created, &expires, &accessed)
	if errors.Is(err, sql.ErrNoRows) {
		return MediaCacheEntryRecord{}, ErrReplayNotFound
	}
	if err != nil {
		return MediaCacheEntryRecord{}, err
	}
	value.CreatedAt, value.ExpiresAt, value.LastAccessedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), time.Unix(accessed, 0).UTC()
	return value, nil
}

// DeleteMediaCacheEntry removes one expired/unowned cache entry and its links.
// It is intentionally narrow: callers use it only while replacing an expired
// content-addressed object, never as a general cache management API.
func (r *Phase5Repository) DeleteMediaCacheEntry(ctx context.Context, id string) error {
	if err := r.require(); err != nil {
		return err
	}
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return ErrReplayNotFound
	}
	tx, err := r.store.db.BeginTx(phase5Context(ctx), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(phase5Context(ctx), `DELETE FROM media_cache_links WHERE entry_id=?`, trimmedID); err != nil {
		return err
	}
	result, err := tx.ExecContext(phase5Context(ctx), `DELETE FROM media_cache_entries WHERE id=?`, trimmedID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrReplayNotFound
	}
	return tx.Commit()
}

func (r *Phase5Repository) TouchMediaCacheEntry(ctx context.Context, id string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	res, err := r.store.db.ExecContext(phase5Context(ctx), `UPDATE media_cache_entries SET last_accessed_at=? WHERE id=?`, phase5Time(now).Unix(), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrReplayNotFound
	}
	return nil
}

// MediaCacheEntryForSourceURL resolves one cached public-media reference for
// a replay snapshot. It returns metadata only; callers still use the cache
// owner to validate the storage path and read bytes.
func (r *Phase5Repository) MediaCacheEntryForSourceURL(ctx context.Context, eventID, sourceURL string) (MediaCacheEntryRecord, error) {
	if err := r.require(); err != nil {
		return MediaCacheEntryRecord{}, err
	}
	var value MediaCacheEntryRecord
	var created, expires, accessed int64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT e.id,e.sha256,e.size_bytes,e.mime_type,e.storage_key,e.created_at,e.expires_at,e.last_accessed_at FROM media_cache_entries e JOIN media_cache_links l ON l.entry_id=e.id WHERE l.event_id=? AND l.source_url=? ORDER BY l.created_at DESC,e.id DESC LIMIT 1`, strings.TrimSpace(eventID), strings.TrimSpace(sourceURL)).Scan(&value.ID, &value.SHA256, &value.SizeBytes, &value.MIMEType, &value.StorageKey, &created, &expires, &accessed)
	if errors.Is(err, sql.ErrNoRows) {
		return MediaCacheEntryRecord{}, ErrReplayNotFound
	}
	if err != nil {
		return MediaCacheEntryRecord{}, err
	}
	value.CreatedAt, value.ExpiresAt, value.LastAccessedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), time.Unix(accessed, 0).UTC()
	return value, nil
}

func (r *Phase5Repository) MediaCacheUsage(ctx context.Context) (int64, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	var total int64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COALESCE(SUM(size_bytes),0) FROM media_cache_entries`).Scan(&total)
	return total, err
}

// MediaCacheSummary is a metadata-only aggregate for the Dashboard. It never
// returns storage paths or media bytes.
type MediaCacheSummary struct {
	Entries      int64 `json:"entries"`
	Bytes        int64 `json:"bytes"`
	Expired      int64 `json:"expired"`
	LinkedEvents int64 `json:"linked_events"`
}

func (r *Phase5Repository) MediaCacheSummary(ctx context.Context, now time.Time) (MediaCacheSummary, error) {
	if err := r.require(); err != nil {
		return MediaCacheSummary{}, err
	}
	var value MediaCacheSummary
	stamp := phase5Time(now).Unix()
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COUNT(*),COALESCE(SUM(size_bytes),0),COALESCE(SUM(CASE WHEN expires_at<=? THEN 1 ELSE 0 END),0) FROM media_cache_entries`, stamp).Scan(&value.Entries, &value.Bytes, &value.Expired)
	if err != nil {
		return MediaCacheSummary{}, err
	}
	if err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COUNT(DISTINCT event_id) FROM media_cache_links`).Scan(&value.LinkedEvents); err != nil {
		return MediaCacheSummary{}, err
	}
	return value, nil
}

// MediaCacheEventUsage returns the bytes already linked to one replay event.
// It is a bounded aggregate used by the cache before an atomic rename; no
// media bytes are loaded into memory.
func (r *Phase5Repository) MediaCacheEventUsage(ctx context.Context, eventID string) (int64, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	var total int64
	err := r.store.db.QueryRowContext(phase5Context(ctx), `SELECT COALESCE(SUM(e.size_bytes),0) FROM media_cache_entries e WHERE EXISTS (SELECT 1 FROM media_cache_links l WHERE l.entry_id=e.id AND l.event_id=?)`, strings.TrimSpace(eventID)).Scan(&total)
	return total, err
}

// EvictMediaCacheEntries removes expired/LRU metadata and returns the removed
// entries so the filesystem owner can unlink the corresponding relative
// files. The SQL and file operations are intentionally separate: a missing
// file is harmless, while a failed metadata delete is surfaced to the caller.
func (r *Phase5Repository) EvictMediaCacheEntries(ctx context.Context, before time.Time, bytesOver int64) ([]MediaCacheEntryRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	if bytesOver <= 0 {
		return nil, nil
	}
	rows, err := r.store.db.QueryContext(phase5Context(ctx), `SELECT id,sha256,size_bytes,mime_type,storage_key,created_at,expires_at,last_accessed_at FROM media_cache_entries ORDER BY CASE WHEN expires_at<=? THEN 0 ELSE 1 END,last_accessed_at,id`, phase5Time(before).Unix())
	if err != nil {
		return nil, err
	}
	var entries []MediaCacheEntryRecord
	var reclaimed int64
	for rows.Next() {
		var entry MediaCacheEntryRecord
		var created, expires, accessed int64
		if e := rows.Scan(&entry.ID, &entry.SHA256, &entry.SizeBytes, &entry.MIMEType, &entry.StorageKey, &created, &expires, &accessed); e != nil {
			return nil, e
		}
		entry.CreatedAt, entry.ExpiresAt, entry.LastAccessedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), time.Unix(accessed, 0).UTC()
		entries = append(entries, entry)
		reclaimed += entry.SizeBytes
		if reclaimed >= bytesOver {
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if _, err := r.store.db.ExecContext(phase5Context(ctx), `DELETE FROM media_cache_entries WHERE id=?`, entry.ID); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (r *Phase5Repository) EvictMediaCache(ctx context.Context, before time.Time, bytesOver int64) error {
	_, err := r.EvictMediaCacheEntries(ctx, before, bytesOver)
	return err
}

func (r *Phase5Repository) PruneFeedback(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 256
	}
	res, err := r.store.db.ExecContext(phase5Context(ctx), `DELETE FROM feedback WHERE id IN (SELECT id FROM feedback WHERE created_at < ? ORDER BY created_at,id LIMIT ?)`, phase5Time(before).Unix(), limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
