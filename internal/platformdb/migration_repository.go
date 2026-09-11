package platformdb

// This file owns the small durable surface used by the Phase 3 connector
// migration coordinator.  It deliberately does not expose *sql.DB: callers
// can only perform the bounded journal, mapping, hold, pairing and audit
// operations that are part of the contract.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/deliverysnapshot"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

type MigrationState string

const (
	MigrationDraft          MigrationState = "draft"
	MigrationPreparing      MigrationState = "preparing"
	MigrationPreflightReady MigrationState = "preflight_ready"
	MigrationCommitting     MigrationState = "committing"
	MigrationCompleted      MigrationState = "completed"
	MigrationFailed         MigrationState = "failed"
	MigrationRolledBack     MigrationState = "rolled_back"
	MigrationRecovery       MigrationState = "recovery_required"
)

var (
	ErrMigrationNotFound          = errors.New("platformdb: migration_not_found")
	ErrMigrationInProgress        = errors.New("platformdb: migration_in_progress")
	ErrMigrationMappingRequired   = errors.New("platformdb: migration_mapping_required")
	ErrMigrationMappingAmbiguous  = errors.New("platformdb: migration_mapping_ambiguous")
	ErrMigrationTargetUnavailable = errors.New("platformdb: migration_target_unavailable")
	ErrMigrationPreflight         = errors.New("platformdb: migration_preflight_failed")
	ErrMigrationRecovery          = errors.New("platformdb: migration_recovery_required")
	ErrMigrationRollbackExpired   = errors.New("platformdb: migration_rollback_expired")
	ErrMigrationInvalidState      = errors.New("platformdb: migration_invalid_state")
	ErrMigrationHoldConflict      = errors.New("platformdb: migration_hold_conflict")
	ErrPairingInvalid             = errors.New("platformdb: pairing_invalid")
	ErrPairingExpired             = errors.New("platformdb: pairing_expired")
	ErrPairingLocked              = errors.New("platformdb: pairing_locked")
	ErrPairingConsumed            = errors.New("platformdb: pairing_consumed")
)

type ConnectorMigration struct {
	MigrationID             string         `json:"migration_id"`
	OldConnectorID          string         `json:"old_connector_id"`
	NewConnectorID          string         `json:"new_connector_id"`
	State                   MigrationState `json:"state"`
	StartedAt               int64          `json:"started_at"`
	UpdatedAt               int64          `json:"updated_at"`
	CompletedAt             *int64         `json:"completed_at,omitempty"`
	OldTopologySnapshotJSON string         `json:"old_topology_snapshot_json"`
	NewTopologySnapshotJSON string         `json:"new_topology_snapshot_json"`
	MappingSnapshotJSON     string         `json:"mapping_snapshot_json"`
	AffectedTargetIDsJSON   string         `json:"affected_target_ids_json"`
	RollbackExpiresAt       *int64         `json:"rollback_expires_at,omitempty"`
	ErrorCode               string         `json:"error_code,omitempty"`
	ProgressMarker          string         `json:"progress_marker"`
	ConfirmedAt             *int64         `json:"confirmed_at,omitempty"`
}

type ConnectorMigrationMapping struct {
	MigrationID    string `json:"migration_id"`
	OldTargetID    string `json:"old_target_id"`
	OldConnectorID string `json:"old_connector_id"`
	OldTargetType  string `json:"old_target_type"`
	OldExternalID  string `json:"old_external_id"`
	NewTargetID    string `json:"new_target_id,omitempty"`
	NewConnectorID string `json:"new_connector_id,omitempty"`
	NewTargetType  string `json:"new_target_type,omitempty"`
	NewExternalID  string `json:"new_external_id,omitempty"`
	Status         string `json:"mapping_status"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type HoldRecord struct {
	DeliveryID      string                   `json:"delivery_id"`
	MigrationID     string                   `json:"migration_id"`
	EventID         string                   `json:"event_id"`
	RouteDecisionID string                   `json:"route_decision_id,omitempty"`
	Payload         deliverysnapshot.Payload `json:"payload"`
	ReleaseState    string                   `json:"release_state"`
	ReleaseAttempts int                      `json:"release_attempts"`
	ClaimedAt       *int64                   `json:"claimed_at,omitempty"`
	ReleasedAt      *int64                   `json:"released_at,omitempty"`
	LastResultCode  string                   `json:"last_result_code,omitempty"`
	CreatedAt       int64                    `json:"created_at"`
}

type PairingChallenge struct {
	ChallengeID  string `json:"challenge_id"`
	ConnectorID  string `json:"connector_id"`
	CodeSHA256   string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
	ExpiresAt    int64  `json:"expires_at"`
	FailureCount int    `json:"failure_count"`
	ConsumedAt   *int64 `json:"consumed_at,omitempty"`
	LockedAt     *int64 `json:"locked_at,omitempty"`
	AdminID      string `json:"admin_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

type AuditEntry struct {
	ID             string `json:"id"`
	OccurredAt     int64  `json:"occurred_at"`
	PrincipalID    string `json:"principal_id,omitempty"`
	Action         string `json:"action"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id,omitempty"`
	Outcome        string `json:"outcome"`
	RequestID      string `json:"request_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	MetadataJSON   string `json:"metadata_json"`
	PrevHash       string `json:"prev_hash,omitempty"`
	EntryHash      string `json:"entry_hash"`
}

type MigrationRepository struct{ store *Store }

func NewMigrationRepository(store *Store) *MigrationRepository {
	if store == nil {
		return nil
	}
	return &MigrationRepository{store: store}
}

func (r *MigrationRepository) require() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrDomainUnavailable
	}
	return nil
}

func migrationNow(now time.Time) int64 {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC().Unix()
}

func validMigrationState(state MigrationState) bool {
	switch state {
	case MigrationDraft, MigrationPreparing, MigrationPreflightReady, MigrationCommitting, MigrationCompleted, MigrationFailed, MigrationRolledBack, MigrationRecovery:
		return true
	}
	return false
}

func (r *MigrationRepository) CreateMigration(ctx context.Context, value ConnectorMigration) error {
	if err := r.require(); err != nil {
		return err
	}
	if strings.TrimSpace(value.MigrationID) == "" {
		id, err := domain.NewID()
		if err != nil {
			return err
		}
		value.MigrationID = id
	}
	if strings.TrimSpace(value.OldConnectorID) == "" || strings.TrimSpace(value.NewConnectorID) == "" || value.OldConnectorID == value.NewConnectorID {
		return ErrMigrationPreflight
	}
	if !validMigrationState(value.State) {
		value.State = MigrationDraft
	}
	if value.StartedAt == 0 {
		value.StartedAt = migrationNow(time.Time{})
	}
	if value.UpdatedAt == 0 {
		value.UpdatedAt = value.StartedAt
	}
	if value.OldTopologySnapshotJSON == "" {
		value.OldTopologySnapshotJSON = "{}"
	}
	if value.NewTopologySnapshotJSON == "" {
		value.NewTopologySnapshotJSON = "{}"
	}
	if value.MappingSnapshotJSON == "" {
		value.MappingSnapshotJSON = "[]"
	}
	if value.AffectedTargetIDsJSON == "" {
		value.AffectedTargetIDsJSON = "[]"
	}
	if value.ProgressMarker == "" {
		value.ProgressMarker = string(value.State)
	}
	_, err := r.store.db.ExecContext(domainContext(ctx), `INSERT INTO connector_migrations
(migration_id, old_connector_id, new_connector_id, state, started_at, updated_at, old_topology_snapshot_json, new_topology_snapshot_json, mapping_snapshot_json, affected_target_ids_json, error_code, progress_marker)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.MigrationID, value.OldConnectorID, value.NewConnectorID, value.State,
		value.StartedAt, value.UpdatedAt, value.OldTopologySnapshotJSON, value.NewTopologySnapshotJSON, value.MappingSnapshotJSON, value.AffectedTargetIDsJSON, value.ErrorCode, value.ProgressMarker)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrMigrationInProgress
		}
		return fmt.Errorf("platformdb: create migration: %w", err)
	}
	return nil
}

func scanMigration(row interface{ Scan(...any) error }) (ConnectorMigration, error) {
	var m ConnectorMigration
	var completed, rollback, confirmed sqlNullInt64
	err := row.Scan(&m.MigrationID, &m.OldConnectorID, &m.NewConnectorID, &m.State, &m.StartedAt, &m.UpdatedAt, &completed, &m.OldTopologySnapshotJSON, &m.NewTopologySnapshotJSON, &m.MappingSnapshotJSON, &m.AffectedTargetIDsJSON, &rollback, &m.ErrorCode, &m.ProgressMarker, &confirmed)
	if err != nil {
		return ConnectorMigration{}, err
	}
	if completed.Valid {
		v := completed.Int64
		m.CompletedAt = &v
	}
	if rollback.Valid {
		v := rollback.Int64
		m.RollbackExpiresAt = &v
	}
	if confirmed.Valid {
		v := confirmed.Int64
		m.ConfirmedAt = &v
	}
	return m, nil
}

// sqlNullInt64 is local to avoid leaking database/sql values in public models.
type sqlNullInt64 struct {
	Int64 int64
	Valid bool
}

func (n *sqlNullInt64) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		n.Valid = false
		return nil
	case int64:
		n.Int64 = v
		n.Valid = true
		return nil
	case int:
		n.Int64 = int64(v)
		n.Valid = true
		return nil
	}
	return fmt.Errorf("platformdb: invalid nullable timestamp")
}

func migrationSelect() string {
	return `SELECT migration_id, old_connector_id, new_connector_id, state, started_at, updated_at, completed_at, old_topology_snapshot_json, new_topology_snapshot_json, mapping_snapshot_json, affected_target_ids_json, rollback_expires_at, error_code, progress_marker, confirmed_at FROM connector_migrations`
}

func (r *MigrationRepository) Migration(ctx context.Context, id string) (ConnectorMigration, error) {
	if err := r.require(); err != nil {
		return ConnectorMigration{}, err
	}
	m, err := scanMigration(r.store.db.QueryRowContext(domainContext(ctx), migrationSelect()+" WHERE migration_id = ?", strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectorMigration{}, ErrMigrationNotFound
	}
	if err != nil {
		return ConnectorMigration{}, fmt.Errorf("platformdb: read migration: %w", err)
	}
	return m, nil
}

func (r *MigrationRepository) ActiveMigration(ctx context.Context) (ConnectorMigration, error) {
	if err := r.require(); err != nil {
		return ConnectorMigration{}, err
	}
	m, err := scanMigration(r.store.db.QueryRowContext(domainContext(ctx), migrationSelect()+" WHERE state IN ('draft','preparing','preflight_ready','committing','recovery_required') ORDER BY started_at LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectorMigration{}, ErrMigrationNotFound
	}
	if err != nil {
		return ConnectorMigration{}, err
	}
	return m, nil
}

func (r *MigrationRepository) UnfinishedMigrations(ctx context.Context) ([]ConnectorMigration, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), migrationSelect()+" WHERE state IN ('draft','preparing','preflight_ready','committing','recovery_required') ORDER BY started_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ConnectorMigration
	for rows.Next() {
		m, scanErr := scanMigration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, m)
	}
	return values, rows.Err()
}

// ListMigrations returns the durable journal in stable order for the
// Dashboard. Recovery uses UnfinishedMigrations so terminal history never
// participates in startup decisions.
func (r *MigrationRepository) ListMigrations(ctx context.Context) ([]ConnectorMigration, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), migrationSelect()+" ORDER BY started_at, migration_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ConnectorMigration
	for rows.Next() {
		m, scanErr := scanMigration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, m)
	}
	return values, rows.Err()
}

func (r *MigrationRepository) UpdateMigration(ctx context.Context, id string, state MigrationState, marker, errorCode string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if !validMigrationState(state) {
		return ErrMigrationInvalidState
	}
	// State transitions are deliberately explicit.  Repeating the same state
	// is allowed for durable progress markers (for example commit_started →
	// hold_active while remaining committing); arbitrary jumps would make
	// restart recovery ambiguous.
	var current MigrationState
	if err := r.store.db.QueryRowContext(domainContext(ctx), "SELECT state FROM connector_migrations WHERE migration_id=?", id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMigrationNotFound
		}
		return err
	}
	if !migrationTransitionAllowed(current, state) {
		return ErrMigrationInvalidState
	}
	stamp := migrationNow(now)
	var completed any
	if state == MigrationCompleted || state == MigrationFailed || state == MigrationRolledBack {
		completed = stamp
	}
	result, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE connector_migrations SET state=?, updated_at=?, completed_at=COALESCE(?, completed_at), error_code=?, progress_marker=? WHERE migration_id=?`, state, stamp, completed, strings.TrimSpace(errorCode), strings.TrimSpace(marker), id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrMigrationInProgress
		}
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrMigrationNotFound
	}
	return nil
}

func migrationTransitionAllowed(from, to MigrationState) bool {
	if from == to {
		return true
	}
	switch from {
	case MigrationDraft:
		return to == MigrationPreparing || to == MigrationFailed
	case MigrationPreparing:
		return to == MigrationPreflightReady || to == MigrationFailed || to == MigrationRecovery
	case MigrationPreflightReady:
		return to == MigrationCommitting || to == MigrationFailed || to == MigrationRecovery
	case MigrationCommitting:
		return to == MigrationCompleted || to == MigrationFailed || to == MigrationRecovery || to == MigrationRolledBack
	case MigrationCompleted:
		return to == MigrationCommitting || to == MigrationRolledBack
	case MigrationRecovery:
		return to == MigrationCommitting || to == MigrationCompleted || to == MigrationFailed || to == MigrationRolledBack
	case MigrationFailed, MigrationRolledBack:
		return false
	default:
		return false
	}
}

// SetRollbackExpiry updates only the rollback deadline. It is kept separate
// from state transitions so a release failure cannot accidentally overwrite
// the immutable topology or mapping snapshots.
func (r *MigrationRepository) SetRollbackExpiry(ctx context.Context, id string, deadline int64, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	result, err := r.store.db.ExecContext(domainContext(ctx), "UPDATE connector_migrations SET rollback_expires_at = ?, updated_at = ? WHERE migration_id = ?", deadline, migrationNow(now), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrMigrationNotFound
	}
	return nil
}

// StoreMigrationWithRollback is retained as a small compatibility helper for
// coordinator code and does not permit arbitrary state mutation.
func (r *MigrationRepository) StoreMigrationWithRollback(ctx context.Context, m ConnectorMigration) error {
	if m.RollbackExpiresAt == nil {
		return nil
	}
	return r.SetRollbackExpiry(ctx, m.MigrationID, *m.RollbackExpiresAt, time.Unix(m.UpdatedAt, 0))
}

func (r *MigrationRepository) ConfirmMigration(ctx context.Context, id string, mappings []ConnectorMigrationMapping, affected []string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	stamp := migrationNow(now)
	data, _ := json.Marshal(mappings)
	targets, _ := json.Marshal(affected)
	result, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE connector_migrations SET mapping_snapshot_json=?, affected_target_ids_json=?, confirmed_at=?, updated_at=? WHERE migration_id=?`, string(data), string(targets), stamp, stamp, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrMigrationNotFound
	}
	return nil
}

func (r *MigrationRepository) ReplaceMappings(ctx context.Context, id string, mappings []ConnectorMigrationMapping, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	stamp := migrationNow(now)
	tx, err := r.store.db.BeginTx(domainContext(ctx), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(domainContext(ctx), "DELETE FROM connector_migration_mappings WHERE migration_id = ?", id); err != nil {
		return err
	}
	for _, m := range mappings {
		if strings.TrimSpace(m.OldTargetID) == "" || strings.TrimSpace(m.OldConnectorID) == "" || strings.TrimSpace(m.OldTargetType) == "" || strings.TrimSpace(m.OldExternalID) == "" {
			return ErrMigrationMappingRequired
		}
		if m.MigrationID == "" {
			m.MigrationID = id
		}
		if m.Status == "" {
			m.Status = "required"
		}
		if m.Status != "required" && m.Status != "confirmed" && m.Status != "unavailable" && m.Status != "ambiguous" {
			return ErrMigrationInvalidState
		}
		if m.CreatedAt == 0 {
			m.CreatedAt = stamp
		}
		m.UpdatedAt = stamp
		if _, err = tx.ExecContext(domainContext(ctx), `INSERT INTO connector_migration_mappings (migration_id,old_target_id,old_connector_id,old_target_type,old_external_id,new_target_id,new_connector_id,new_target_type,new_external_id,mapping_status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, id, m.OldTargetID, m.OldConnectorID, m.OldTargetType, m.OldExternalID, nullIfEmpty(m.NewTargetID), nullIfEmpty(m.NewConnectorID), nullIfEmpty(m.NewTargetType), nullIfEmpty(m.NewExternalID), m.Status, m.CreatedAt, m.UpdatedAt); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(mappings)
	if _, err = tx.ExecContext(domainContext(ctx), "UPDATE connector_migrations SET mapping_snapshot_json=?, updated_at=? WHERE migration_id=?", string(data), stamp, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *MigrationRepository) Mappings(ctx context.Context, id string) ([]ConnectorMigrationMapping, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `SELECT migration_id,old_target_id,old_connector_id,old_target_type,old_external_id,COALESCE(new_target_id,''),COALESCE(new_connector_id,''),COALESCE(new_target_type,''),COALESCE(new_external_id,''),mapping_status,created_at,updated_at FROM connector_migration_mappings WHERE migration_id=? ORDER BY old_target_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectorMigrationMapping
	for rows.Next() {
		var m ConnectorMigrationMapping
		if err := rows.Scan(&m.MigrationID, &m.OldTargetID, &m.OldConnectorID, &m.OldTargetType, &m.OldExternalID, &m.NewTargetID, &m.NewConnectorID, &m.NewTargetType, &m.NewExternalID, &m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *MigrationRepository) PutHold(ctx context.Context, migrationID string, payload deliverysnapshot.Payload, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.MigrationID != migrationID {
		return ErrMigrationInvalidState
	}
	stamp := migrationNow(now)
	if payload.CreatedAt == 0 {
		payload.CreatedAt = stamp
	}
	if payload.HeldAt == 0 {
		payload.HeldAt = stamp
	}
	payloadJSON, err := payload.Marshal()
	if err != nil {
		return err
	}
	var existingMigration, existingPayload string
	lookupErr := r.store.db.QueryRowContext(domainContext(ctx), "SELECT migration_id,COALESCE(payload_json,'') FROM delivery_migration_holds WHERE delivery_id=?", payload.DeliveryID).Scan(&existingMigration, &existingPayload)
	if lookupErr == nil {
		if existingMigration == migrationID {
			// Repeating the same hold is idempotent only when it carries the
			// same durable payload. A reused delivery identity with different
			// content must never silently overwrite the first snapshot.
			if strings.TrimSpace(existingPayload) != "" {
				existing, decodeErr := deliverysnapshot.Unmarshal([]byte(existingPayload))
				if decodeErr != nil {
					return ErrMigrationHoldConflict
				}
				canonicalExisting, marshalErr := existing.Marshal()
				if marshalErr != nil || string(canonicalExisting) != string(payloadJSON) {
					return ErrMigrationHoldConflict
				}
			}
			return nil
		}
		return ErrMigrationHoldConflict
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	_, err = r.store.db.ExecContext(domainContext(ctx), `INSERT INTO delivery_migration_holds (delivery_id,migration_id,event_id,route_decision_id,route_snapshot_json,logical_target_json,message_snapshot_json,payload_schema_version,status,created_at,payload_json) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, payload.DeliveryID, migrationID, payload.EventID, payload.RouteID, string(payload.RouteSnapshot), mustJSON(payload.LogicalTarget), mustJSON(payload.Message), payload.SchemaVersion, "migration_held", stamp, string(payloadJSON))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrMigrationHoldConflict
	}
	return err
}

func (r *MigrationRepository) Holds(ctx context.Context, migrationID string) ([]HoldRecord, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `SELECT delivery_id,migration_id,event_id,COALESCE(route_decision_id,''),route_snapshot_json,logical_target_json,message_snapshot_json,release_state,release_attempts,claimed_at,released_at,last_result_code,created_at,COALESCE(payload_json,'') FROM delivery_migration_holds WHERE migration_id=? ORDER BY created_at,delivery_id`, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HoldRecord
	for rows.Next() {
		var h HoldRecord
		var route, logical, message, payloadJSON string
		var claimed, released sqlNullInt64
		if err := rows.Scan(&h.DeliveryID, &h.MigrationID, &h.EventID, &h.RouteDecisionID, &route, &logical, &message, &h.ReleaseState, &h.ReleaseAttempts, &claimed, &released, &h.LastResultCode, &h.CreatedAt, &payloadJSON); err != nil {
			return nil, err
		}
		raw := deliverysnapshot.Payload{SchemaVersion: 1, DeliveryID: h.DeliveryID, MigrationID: h.MigrationID, EventID: h.EventID, RouteID: h.RouteDecisionID, RouteSnapshot: json.RawMessage(route)}
		if strings.TrimSpace(payloadJSON) != "" {
			decoded, decodeErr := deliverysnapshot.Unmarshal([]byte(payloadJSON))
			if decodeErr != nil {
				return nil, decodeErr
			}
			raw = decoded
		} else {
			if err := json.Unmarshal([]byte(logical), &raw.LogicalTarget); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(message), &raw.Message); err != nil {
				return nil, err
			}
		}
		raw.DeliveryID, raw.MigrationID, raw.EventID, raw.RouteID = h.DeliveryID, h.MigrationID, h.EventID, h.RouteDecisionID
		h.Payload = raw
		if claimed.Valid {
			v := claimed.Int64
			h.ClaimedAt = &v
		}
		if released.Valid {
			v := released.Int64
			h.ReleasedAt = &v
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *MigrationRepository) ClaimHold(ctx context.Context, deliveryID string, now time.Time) (HoldRecord, error) {
	if err := r.require(); err != nil {
		return HoldRecord{}, err
	}
	stamp := migrationNow(now)
	result, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE delivery_migration_holds SET release_state='releasing',release_attempts=release_attempts+1,claimed_at=?,last_result_code='' WHERE delivery_id=? AND release_state='held'`, stamp, deliveryID)
	if err != nil {
		return HoldRecord{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return HoldRecord{}, ErrMigrationInvalidState
	}
	var h HoldRecord
	var route, logical, message, payloadJSON string
	var claimed, released sqlNullInt64
	err = r.store.db.QueryRowContext(domainContext(ctx), `SELECT delivery_id,migration_id,event_id,COALESCE(route_decision_id,''),route_snapshot_json,logical_target_json,message_snapshot_json,release_state,release_attempts,claimed_at,released_at,last_result_code,created_at,COALESCE(payload_json,'') FROM delivery_migration_holds WHERE delivery_id=?`, deliveryID).Scan(&h.DeliveryID, &h.MigrationID, &h.EventID, &h.RouteDecisionID, &route, &logical, &message, &h.ReleaseState, &h.ReleaseAttempts, &claimed, &released, &h.LastResultCode, &h.CreatedAt, &payloadJSON)
	if err != nil {
		return HoldRecord{}, err
	}
	if strings.TrimSpace(payloadJSON) != "" {
		decoded, decodeErr := deliverysnapshot.Unmarshal([]byte(payloadJSON))
		if decodeErr != nil {
			return HoldRecord{}, decodeErr
		}
		h.Payload = decoded
	} else {
		h.Payload.SchemaVersion = 1
		h.Payload.RouteSnapshot = json.RawMessage(route)
		_ = json.Unmarshal([]byte(logical), &h.Payload.LogicalTarget)
		_ = json.Unmarshal([]byte(message), &h.Payload.Message)
	}
	h.Payload.DeliveryID, h.Payload.MigrationID, h.Payload.EventID, h.Payload.RouteID = h.DeliveryID, h.MigrationID, h.EventID, h.RouteDecisionID
	return h, nil
}

func (r *MigrationRepository) FinishHold(ctx context.Context, deliveryID, state, resultCode string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	if state != "released" && state != "unknown" && state != "failed" {
		return ErrMigrationInvalidState
	}
	stamp := migrationNow(now)
	var released any
	if state == "released" {
		released = stamp
	}
	result, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE delivery_migration_holds SET release_state=?,released_at=COALESCE(?,released_at),last_result_code=? WHERE delivery_id=? AND release_state='releasing'`, state, released, strings.TrimSpace(resultCode), deliveryID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 1 {
		return nil
	}
	// Terminal writes are idempotent. A recovery pass may see an unknown row
	// already marked by an earlier process and must never send it again.
	var current string
	if err := r.store.db.QueryRowContext(domainContext(ctx), "SELECT release_state FROM delivery_migration_holds WHERE delivery_id=?", deliveryID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMigrationNotFound
		}
		return err
	}
	if current == state {
		return nil
	}
	return ErrMigrationInvalidState
}

// MarkReleasingUnknown closes the only ambiguous crash window: a process can
// disappear after an external send returns but before FinishHold commits.
// Such a row is terminal and is never automatically retried.
func (r *MigrationRepository) MarkReleasingUnknown(ctx context.Context, migrationID string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	_, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE delivery_migration_holds SET release_state='unknown', last_result_code='abandoned_restart' WHERE migration_id=? AND release_state='releasing'`, migrationID)
	return err
}

type HoldSummary struct {
	Total     int `json:"total"`
	Held      int `json:"held"`
	Releasing int `json:"releasing"`
	Released  int `json:"released"`
	Unknown   int `json:"unknown"`
	Failed    int `json:"failed"`
}

func (r *MigrationRepository) HoldSummary(ctx context.Context, migrationID string) (HoldSummary, error) {
	if err := r.require(); err != nil {
		return HoldSummary{}, err
	}
	var out HoldSummary
	rows, err := r.store.db.QueryContext(domainContext(ctx), "SELECT release_state, COUNT(*) FROM delivery_migration_holds WHERE migration_id=? GROUP BY release_state", migrationID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return out, err
		}
		out.Total += count
		switch state {
		case "held":
			out.Held = count
		case "releasing":
			out.Releasing = count
		case "released":
			out.Released = count
		case "unknown":
			out.Unknown = count
		case "failed":
			out.Failed = count
		}
	}
	return out, rows.Err()
}

func (r *MigrationRepository) FrozenTarget(ctx context.Context, targetID string) (bool, error) {
	if err := r.require(); err != nil {
		return false, err
	}
	var count int
	err := r.store.db.QueryRowContext(domainContext(ctx), `SELECT COUNT(*) FROM connector_migration_mappings m JOIN connector_migrations x ON x.migration_id=m.migration_id WHERE (m.old_target_id=? OR m.new_target_id=?) AND x.state IN ('preparing','preflight_ready','committing','recovery_required')`, targetID, targetID).Scan(&count)
	return count > 0, err
}

// FrozenLegacyGroup is the adapter-friendly gate used by the Legacy
// subscription service. Legacy OneBot identities are naked group numbers, so
// the lookup is performed by the durable target external identity rather than
// by guessing a UUID in the command layer.
func (r *MigrationRepository) FrozenLegacyGroup(ctx context.Context, groupCode int64) (bool, error) {
	if err := r.require(); err != nil {
		return false, err
	}
	var count int
	err := r.store.db.QueryRowContext(domainContext(ctx), `SELECT COUNT(*) FROM connector_migration_mappings m JOIN connector_migrations x ON x.migration_id=m.migration_id JOIN targets t ON t.id=m.old_target_id WHERE t.target_type='group' AND t.external_id=? AND x.state IN ('preparing','preflight_ready','committing','recovery_required')`, fmt.Sprintf("%d", groupCode)).Scan(&count)
	return count > 0, err
}

func (r *MigrationRepository) CreatePairing(ctx context.Context, challenge PairingChallenge) error {
	if err := r.require(); err != nil {
		return err
	}
	if challenge.ChallengeID == "" {
		id, err := domain.NewID()
		if err != nil {
			return err
		}
		challenge.ChallengeID = id
	}
	_, err := r.store.db.ExecContext(domainContext(ctx), `INSERT INTO telegram_pairing_challenges (challenge_id,connector_id,code_sha256,created_at,expires_at,admin_id,session_id) VALUES (?,?,?,?,?,?,?)`, challenge.ChallengeID, challenge.ConnectorID, challenge.CodeSHA256, challenge.CreatedAt, challenge.ExpiresAt, challenge.AdminID, challenge.SessionID)
	return err
}
func (r *MigrationRepository) Pairing(ctx context.Context, id string) (PairingChallenge, error) {
	if err := r.require(); err != nil {
		return PairingChallenge{}, err
	}
	var p PairingChallenge
	var consumed, locked sqlNullInt64
	err := r.store.db.QueryRowContext(domainContext(ctx), `SELECT challenge_id,connector_id,code_sha256,created_at,expires_at,failure_count,consumed_at,locked_at,admin_id,session_id FROM telegram_pairing_challenges WHERE challenge_id=?`, id).Scan(&p.ChallengeID, &p.ConnectorID, &p.CodeSHA256, &p.CreatedAt, &p.ExpiresAt, &p.FailureCount, &consumed, &locked, &p.AdminID, &p.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return PairingChallenge{}, ErrPairingInvalid
	}
	if err != nil {
		return p, err
	}
	if consumed.Valid {
		v := consumed.Int64
		p.ConsumedAt = &v
	}
	if locked.Valid {
		v := locked.Int64
		p.LockedAt = &v
	}
	return p, nil
}

// PairingByCodeHash locates the durable challenge for a one-time Telegram
// code. The code digest is the only lookup material; plaintext never crosses
// this repository boundary. Consumed/locked/expired rows are intentionally
// still returned so the service can apply the same stable state semantics as
// challenge-id verification instead of treating every replay as "missing".
func (r *MigrationRepository) PairingByCodeHash(ctx context.Context, codeHash string) (PairingChallenge, error) {
	if err := r.require(); err != nil {
		return PairingChallenge{}, err
	}
	var p PairingChallenge
	var consumed, locked sqlNullInt64
	err := r.store.db.QueryRowContext(domainContext(ctx), `SELECT challenge_id,connector_id,code_sha256,created_at,expires_at,failure_count,consumed_at,locked_at,admin_id,session_id FROM telegram_pairing_challenges WHERE code_sha256=? ORDER BY created_at DESC LIMIT 1`, strings.TrimSpace(codeHash)).Scan(&p.ChallengeID, &p.ConnectorID, &p.CodeSHA256, &p.CreatedAt, &p.ExpiresAt, &p.FailureCount, &consumed, &locked, &p.AdminID, &p.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return PairingChallenge{}, ErrPairingInvalid
	}
	if err != nil {
		return PairingChallenge{}, err
	}
	if consumed.Valid {
		v := consumed.Int64
		p.ConsumedAt = &v
	}
	if locked.Valid {
		v := locked.Int64
		p.LockedAt = &v
	}
	return p, nil
}
func (r *MigrationRepository) ConsumePairing(ctx context.Context, id string, now time.Time) error {
	if err := r.require(); err != nil {
		return err
	}
	stamp := migrationNow(now)
	res, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE telegram_pairing_challenges SET consumed_at=? WHERE challenge_id=? AND consumed_at IS NULL AND locked_at IS NULL`, stamp, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrPairingConsumed
	}
	return nil
}
func (r *MigrationRepository) FailPairing(ctx context.Context, id string, now time.Time) (int, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	stamp := migrationNow(now)
	if _, err := r.store.db.ExecContext(domainContext(ctx), `UPDATE telegram_pairing_challenges SET failure_count=failure_count+1,locked_at=CASE WHEN failure_count+1>=5 THEN ? ELSE locked_at END WHERE challenge_id=? AND consumed_at IS NULL AND locked_at IS NULL`, stamp, id); err != nil {
		return 0, err
	}
	p, err := r.Pairing(ctx, id)
	if err != nil {
		return 0, err
	}
	return p.FailureCount, nil
}

func (r *MigrationRepository) AppendAudit(ctx context.Context, entry AuditEntry) (AuditEntry, error) {
	if err := r.require(); err != nil {
		return AuditEntry{}, err
	}
	if entry.ID == "" {
		id, err := domain.NewID()
		if err != nil {
			return entry, err
		}
		entry.ID = id
	}
	entry.MetadataJSON = sanitizeAuditMetadata(entry.MetadataJSON)
	ctx = domainContext(ctx)
	// The previous hash and append must share one SQLite transaction. Without
	// this boundary two concurrent callers could read the same tail and create
	// a permanently forked chain.
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return AuditEntry{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var previous string
	if err := tx.QueryRowContext(ctx, `SELECT entry_hash FROM audit_entries ORDER BY rowid DESC LIMIT 1`).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AuditEntry{}, err
	}
	entry.PrevHash = previous
	payload := auditPayload(entry)
	sum := sha256.Sum256([]byte(entry.PrevHash + "|" + payload))
	entry.EntryHash = hex.EncodeToString(sum[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries (id,occurred_at,principal_id,action,resource_type,resource_id,outcome,request_id,idempotency_key,metadata_json,prev_hash,entry_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, entry.ID, entry.OccurredAt, entry.PrincipalID, entry.Action, entry.ResourceType, entry.ResourceID, entry.Outcome, entry.RequestID, entry.IdempotencyKey, entry.MetadataJSON, entry.PrevHash, entry.EntryHash); err != nil {
		return AuditEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuditEntry{}, err
	}
	committed = true
	return entry, nil
}

func auditPayload(entry AuditEntry) string {
	return fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s|%s|%s|%s", entry.ID, entry.OccurredAt, entry.PrincipalID, entry.Action, entry.ResourceType, entry.ResourceID, entry.Outcome, entry.RequestID, entry.IdempotencyKey, entry.MetadataJSON)
}

func sanitizeAuditMetadata(raw string) string {
	var value any
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &value) != nil {
		return "{}"
	}
	// Audit metadata is intentionally object-shaped. Dropping a scalar avoids
	// turning this safety boundary into a generic secret/string log sink.
	if _, ok := value.(map[string]any); !ok {
		return "{}"
	}
	redacted := redactAuditValue(value)
	data, err := json.Marshal(redacted)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func redactAuditValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if auditSensitiveKey(key) {
				out[key] = "[redacted]"
			} else {
				out[key] = redactAuditValue(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactAuditValue(item)
		}
		return out
	default:
		return value
	}
}

func auditSensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	for _, token := range []string{"password", "secret", "token", "credential", "private_key", "access_key", "pairing_code", "ciphertext", "nonce"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}
func (r *MigrationRepository) ListAudit(ctx context.Context, resourceType, resourceID string) ([]AuditEntry, error) {
	if err := r.require(); err != nil {
		return nil, err
	}
	q := `SELECT id,occurred_at,principal_id,action,resource_type,resource_id,outcome,request_id,idempotency_key,metadata_json,prev_hash,entry_hash FROM audit_entries`
	args := []any{}
	if strings.TrimSpace(resourceType) != "" {
		q += " WHERE resource_type=?"
		args = append(args, resourceType)
		if resourceID != "" {
			q += " AND resource_id=?"
			args = append(args, resourceID)
		}
	}
	// SQLite's implicit rowid is the append order. Using occurred_at here would
	// allow a clock adjustment to reorder entries and make a valid hash chain
	// appear forked; occurred_at remains an audit field, not the chain cursor.
	q += " ORDER BY rowid"
	rows, err := r.store.db.QueryContext(domainContext(ctx), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.PrincipalID, &e.Action, &e.ResourceType, &e.ResourceID, &e.Outcome, &e.RequestID, &e.IdempotencyKey, &e.MetadataJSON, &e.PrevHash, &e.EntryHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (r *MigrationRepository) VerifyAuditChain(ctx context.Context) error {
	entries, err := r.ListAudit(ctx, "", "")
	if err != nil {
		return err
	}
	prev := ""
	for _, e := range entries {
		payload := auditPayload(e)
		sum := sha256.Sum256([]byte(prev + "|" + payload))
		if e.PrevHash != prev || e.EntryHash != hex.EncodeToString(sum[:]) {
			return errors.New("platformdb: audit chain invalid")
		}
		prev = e.EntryHash
	}
	return nil
}

// PruneAudit removes entries older than cutoff as one explicit retention
// operation and inserts a durable audit.prune anchor. Because the hash chain
// is ordered by append rowid, retained entries are re-chained behind the
// anchor in the same transaction; there is no window in which a successful
// prune leaves VerifyAuditChain unable to validate the retained history.
// Pruning is intentionally the only mutation boundary for retention. Ordinary
// repository/API callers still have no update or single-entry delete method.
func (r *MigrationRepository) PruneAudit(ctx context.Context, cutoff time.Time) (int, error) {
	return r.PruneAuditAt(ctx, cutoff, time.Now())
}

// PruneAuditAt is the deterministic-clock form used by tests and by a future
// retention scheduler. It has the same all-or-nothing semantics as
// PruneAudit.
func (r *MigrationRepository) PruneAuditAt(ctx context.Context, cutoff, now time.Time) (int, error) {
	if err := r.require(); err != nil {
		return 0, err
	}
	if cutoff.IsZero() {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	ctx = domainContext(ctx)
	type storedAudit struct {
		ID             string
		OccurredAt     int64
		PrincipalID    string
		Action         string
		ResourceType   string
		ResourceID     string
		Outcome        string
		RequestID      string
		IdempotencyKey string
		MetadataJSON   string
	}
	rows, err := r.store.db.QueryContext(ctx, `SELECT id,occurred_at,principal_id,action,resource_type,resource_id,outcome,request_id,idempotency_key,metadata_json FROM audit_entries ORDER BY rowid`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var retained []storedAudit
	pruned := 0
	for rows.Next() {
		var entry storedAudit
		if err := rows.Scan(&entry.ID, &entry.OccurredAt, &entry.PrincipalID, &entry.Action, &entry.ResourceType, &entry.ResourceID, &entry.Outcome, &entry.RequestID, &entry.IdempotencyKey, &entry.MetadataJSON); err != nil {
			return 0, err
		}
		if entry.OccurredAt < cutoff.UTC().Unix() {
			pruned++
		} else {
			retained = append(retained, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	_ = rows.Close()
	if pruned == 0 {
		return 0, nil
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_entries"); err != nil {
		return 0, err
	}
	anchorID, err := domain.NewID()
	if err != nil {
		return 0, err
	}
	anchor := AuditEntry{
		ID: anchorID, OccurredAt: now.UTC().Unix(), Action: "audit.prune",
		ResourceType: "audit", Outcome: "retention", MetadataJSON: mustJSON(map[string]any{
			"cutoff": cutoff.UTC().Format(time.RFC3339Nano), "pruned_entries": pruned,
		}),
	}
	anchor.MetadataJSON = sanitizeAuditMetadata(anchor.MetadataJSON)
	anchor.PrevHash = ""
	sum := sha256.Sum256([]byte(anchor.PrevHash + "|" + auditPayload(anchor)))
	anchor.EntryHash = hex.EncodeToString(sum[:])
	insertAudit := func(entry AuditEntry) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_entries (id,occurred_at,principal_id,action,resource_type,resource_id,outcome,request_id,idempotency_key,metadata_json,prev_hash,entry_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, entry.ID, entry.OccurredAt, entry.PrincipalID, entry.Action, entry.ResourceType, entry.ResourceID, entry.Outcome, entry.RequestID, entry.IdempotencyKey, entry.MetadataJSON, entry.PrevHash, entry.EntryHash)
		return err
	}
	if err := insertAudit(anchor); err != nil {
		return 0, err
	}
	previous := anchor.EntryHash
	for _, stored := range retained {
		entry := AuditEntry{ID: stored.ID, OccurredAt: stored.OccurredAt, PrincipalID: stored.PrincipalID, Action: stored.Action, ResourceType: stored.ResourceType, ResourceID: stored.ResourceID, Outcome: stored.Outcome, RequestID: stored.RequestID, IdempotencyKey: stored.IdempotencyKey, MetadataJSON: sanitizeAuditMetadata(stored.MetadataJSON), PrevHash: previous}
		sum := sha256.Sum256([]byte(entry.PrevHash + "|" + auditPayload(entry)))
		entry.EntryHash = hex.EncodeToString(sum[:])
		if err := insertAudit(entry); err != nil {
			return 0, err
		}
		previous = entry.EntryHash
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return pruned, nil
}

func nullIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
