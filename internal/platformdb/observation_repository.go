package platformdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrObservationInvalidEvent     = errors.New("platformdb: invalid observed event")
	ErrObservationInvalidRoute     = errors.New("platformdb: invalid route observation")
	ErrObservationInvalidDelivery  = errors.New("platformdb: invalid delivery observation")
	ErrObservationInvalidRetention = errors.New("platformdb: invalid observation retention")
)

// ObservedEventRecord is the durable, allowlisted public snapshot written by
// the passive observation runtime. It deliberately has no raw source payload
// or connector credentials.
type ObservedEventRecord struct {
	ID                 string
	SchemaVersion      int
	Platform           string
	SourceKind         string
	SourceExternalID   string
	UpstreamEventID    string
	EventType          string
	ObservedAt         time.Time
	SourceEventAt      *time.Time
	ContentFingerprint string
	PublicSnapshotJSON string
	CreatedAt          time.Time
}

type RouteObservationRecord struct {
	ID                    string
	EventID               string
	RouteOrdinal          int
	DestinationKind       string
	DestinationExternalID string
	Outcome               string
	ReasonCode            string
	ObservedAt            time.Time
	CreatedAt             time.Time
}

type DeliveryObservationRecord struct {
	ID                    string
	EventID               string
	RouteObservationID    string
	ConnectorKind         string
	DestinationExternalID string
	Status                string
	ResultCode            string
	ObservedAt            time.Time
	CreatedAt             time.Time
}

// ObservationRepository is the only durable boundary used by the P2A
// recorder. It shares the existing platformdb Store and never opens another
// SQLite handle.
type ObservationRepository struct {
	store *Store
}

func NewObservationRepository(store *Store) *ObservationRepository {
	if store == nil {
		return nil
	}
	return &ObservationRepository{store: store}
}

func (r *ObservationRepository) requireStore() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrDatabaseClosed
	}
	return nil
}

func normalizeObservationTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func (r *ObservationRepository) InsertObservedEvent(ctx context.Context, record ObservedEventRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if strings.TrimSpace(record.ID) == "" || record.SchemaVersion != 1 ||
		strings.TrimSpace(record.Platform) == "" || strings.TrimSpace(record.SourceKind) == "" ||
		strings.TrimSpace(record.EventType) == "" || strings.TrimSpace(record.ContentFingerprint) == "" ||
		record.PublicSnapshotJSON == "" {
		return ErrObservationInvalidEvent
	}
	ctx = normalizeContext(ctx)
	observedAt := normalizeObservationTime(record.ObservedAt).Unix()
	createdAt := normalizeObservationTime(record.CreatedAt).Unix()
	var sourceEventAt any
	if record.SourceEventAt != nil && !record.SourceEventAt.IsZero() {
		sourceEventAt = record.SourceEventAt.UTC().Unix()
	}
	_, err := r.store.db.ExecContext(ctx, `
INSERT INTO observed_events
(id, schema_version, platform, source_kind, source_external_id,
 upstream_event_id, event_type, observed_at, source_event_at,
 content_fingerprint, public_snapshot_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.SchemaVersion, record.Platform, record.SourceKind,
		record.SourceExternalID, record.UpstreamEventID, record.EventType,
		observedAt, sourceEventAt, record.ContentFingerprint,
		record.PublicSnapshotJSON, createdAt)
	if err != nil {
		return fmt.Errorf("platformdb: insert observed event: %w", err)
	}
	return nil
}

func (r *ObservationRepository) InsertRouteObservation(ctx context.Context, record RouteObservationRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.EventID) == "" ||
		record.RouteOrdinal < 0 || strings.TrimSpace(record.DestinationKind) == "" ||
		record.DestinationExternalID == "" || strings.TrimSpace(record.Outcome) == "" ||
		strings.TrimSpace(record.ReasonCode) == "" {
		return ErrObservationInvalidRoute
	}
	ctx = normalizeContext(ctx)
	_, err := r.store.db.ExecContext(ctx, `
INSERT INTO route_observations
(id, event_id, route_ordinal, destination_kind, destination_external_id,
 outcome, reason_code, observed_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.EventID, record.RouteOrdinal, record.DestinationKind,
		record.DestinationExternalID, record.Outcome, record.ReasonCode,
		normalizeObservationTime(record.ObservedAt).Unix(), normalizeObservationTime(record.CreatedAt).Unix())
	if err != nil {
		return fmt.Errorf("platformdb: insert route observation: %w", err)
	}
	return nil
}

func (r *ObservationRepository) InsertDeliveryObservation(ctx context.Context, record DeliveryObservationRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.EventID) == "" ||
		strings.TrimSpace(record.RouteObservationID) == "" || strings.TrimSpace(record.ConnectorKind) == "" ||
		record.DestinationExternalID == "" || strings.TrimSpace(record.Status) == "" ||
		strings.TrimSpace(record.ResultCode) == "" {
		return ErrObservationInvalidDelivery
	}
	ctx = normalizeContext(ctx)
	_, err := r.store.db.ExecContext(ctx, `
INSERT INTO delivery_observations
(id, event_id, route_observation_id, connector_kind, destination_external_id,
 status, result_code, observed_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.EventID, record.RouteObservationID, record.ConnectorKind,
		record.DestinationExternalID, record.Status, record.ResultCode,
		normalizeObservationTime(record.ObservedAt).Unix(), normalizeObservationTime(record.CreatedAt).Unix())
	if err != nil {
		return fmt.Errorf("platformdb: insert delivery observation: %w", err)
	}
	return nil
}

// PruneObservationsBefore deletes at most batchSize root events in one short
// transaction. Foreign-key cascades remove their route and delivery facts.
// It returns the number of root events removed; callers may repeat it as a
// best-effort janitor without turning retention into a readiness dependency.
func (r *ObservationRepository) PruneObservationsBefore(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	if err := r.requireStore(); err != nil {
		return 0, err
	}
	if cutoff.IsZero() || batchSize <= 0 {
		return 0, ErrObservationInvalidRetention
	}
	ctx = normalizeContext(ctx)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("platformdb: begin observation prune: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, `
DELETE FROM observed_events
WHERE id IN (
    SELECT id FROM observed_events
    WHERE observed_at < ?
    ORDER BY observed_at, id
    LIMIT ?
)`, cutoff.UTC().Unix(), batchSize)
	if err != nil {
		return 0, fmt.Errorf("platformdb: prune observations: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("platformdb: count pruned observations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("platformdb: commit observation prune: %w", err)
	}
	committed = true
	return int(count), nil
}

// ObservationCounts is intentionally a narrow test/diagnostic helper, not a
// public Dashboard query API.
func (r *ObservationRepository) ObservationCounts(ctx context.Context) (events, routes, deliveries int, err error) {
	if err = r.requireStore(); err != nil {
		return
	}
	ctx = normalizeContext(ctx)
	queries := []struct {
		query string
		out   *int
	}{
		{"SELECT COUNT(*) FROM observed_events", &events},
		{"SELECT COUNT(*) FROM route_observations", &routes},
		{"SELECT COUNT(*) FROM delivery_observations", &deliveries},
	}
	for _, item := range queries {
		if scanErr := r.store.db.QueryRowContext(ctx, item.query).Scan(item.out); scanErr != nil {
			err = fmt.Errorf("platformdb: count observations: %w", scanErr)
			return
		}
	}
	return
}

// ObservationSnapshot returns persisted JSON for tests and local diagnostics;
// it does not expose any source response beyond the allowlisted snapshot that
// was explicitly written by the recorder.
func (r *ObservationRepository) ObservationSnapshot(ctx context.Context, id string) (string, error) {
	if err := r.requireStore(); err != nil {
		return "", err
	}
	if strings.TrimSpace(id) == "" {
		return "", ErrObservationInvalidEvent
	}
	var snapshot string
	if err := r.store.db.QueryRowContext(normalizeContext(ctx),
		"SELECT public_snapshot_json FROM observed_events WHERE id = ?", id).Scan(&snapshot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrObservationInvalidEvent
		}
		return "", fmt.Errorf("platformdb: read observation snapshot: %w", err)
	}
	return strings.Clone(snapshot), nil
}
