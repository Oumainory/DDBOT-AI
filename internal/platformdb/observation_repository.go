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
	ErrObservationInvalidQuery     = errors.New("platformdb: invalid observation query")
	ErrObservationNotFound         = errors.New("platformdb: observation not found")
)

const (
	DefaultObservationPageSize = 50
	MaxObservationPageSize     = 200
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

// ObservationCursor is the durable position used by the read API. It is a
// value object rather than a database reference: retention may delete the row
// represented by a cursor without making the cursor invalid.
type ObservationCursor struct {
	ObservedAt int64
	ID         string
}

// ObservationEventQuery contains only allowlisted, parameterized filters. A
// zero Limit is normalized to DefaultObservationPageSize by the repository.
type ObservationEventQuery struct {
	Limit            int
	Cursor           *ObservationCursor
	Platform         string
	EventType        string
	SourceKind       string
	SourceExternalID string
	From             *time.Time
	To               *time.Time
}

type ObservationEventListRecord struct {
	ObservedEventRecord
	RouteCount          int
	DeliveryCount       int
	FinalDeliveryStatus string
}

type ObservationEventPage struct {
	Events     []ObservationEventListRecord
	NextCursor *ObservationCursor
}

type ObservationRecentCounts struct {
	Events24h     int
	Routes24h     int
	Deliveries24h int
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

func normalizeObservationQuery(query ObservationEventQuery) (ObservationEventQuery, error) {
	if query.Limit == 0 {
		query.Limit = DefaultObservationPageSize
	}
	if query.Limit < 1 || query.Limit > MaxObservationPageSize {
		return ObservationEventQuery{}, ErrObservationInvalidQuery
	}
	query.Platform = strings.TrimSpace(query.Platform)
	query.EventType = strings.TrimSpace(query.EventType)
	query.SourceKind = strings.TrimSpace(query.SourceKind)
	query.SourceExternalID = strings.TrimSpace(query.SourceExternalID)
	if query.Cursor != nil {
		query.Cursor.ID = strings.TrimSpace(query.Cursor.ID)
		if query.Cursor.ID == "" || query.Cursor.ObservedAt < 0 {
			return ObservationEventQuery{}, ErrObservationInvalidQuery
		}
	}
	if query.From != nil {
		from := query.From.UTC()
		query.From = &from
	}
	if query.To != nil {
		to := query.To.UTC()
		query.To = &to
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return ObservationEventQuery{}, ErrObservationInvalidQuery
	}
	return query, nil
}

// ListObservedEvents returns a stable observed_at DESC, id DESC page. The
// extra row is read only to determine whether a next cursor exists; it is not
// returned to the caller.
func (r *ObservationRepository) ListObservedEvents(ctx context.Context, query ObservationEventQuery) (ObservationEventPage, error) {
	if err := r.requireStore(); err != nil {
		return ObservationEventPage{}, err
	}
	query, err := normalizeObservationQuery(query)
	if err != nil {
		return ObservationEventPage{}, err
	}
	ctx = normalizeContext(ctx)
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 10)
	if query.Platform != "" {
		clauses = append(clauses, "platform = ?")
		args = append(args, query.Platform)
	}
	if query.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, query.EventType)
	}
	if query.SourceKind != "" {
		clauses = append(clauses, "source_kind = ?")
		args = append(args, query.SourceKind)
	}
	if query.SourceExternalID != "" {
		clauses = append(clauses, "source_external_id = ?")
		args = append(args, query.SourceExternalID)
	}
	if query.From != nil {
		clauses = append(clauses, "observed_at >= ?")
		args = append(args, query.From.Unix())
	}
	if query.To != nil {
		clauses = append(clauses, "observed_at <= ?")
		args = append(args, query.To.Unix())
	}
	if query.Cursor != nil {
		clauses = append(clauses, "(observed_at < ? OR (observed_at = ? AND id < ?))")
		args = append(args, query.Cursor.ObservedAt, query.Cursor.ObservedAt, query.Cursor.ID)
	}
	statement := `SELECT e.id, e.schema_version, e.platform, e.source_kind, e.source_external_id,
e.upstream_event_id, e.event_type, e.observed_at, e.source_event_at,
e.content_fingerprint, substr(e.public_snapshot_json, 1, 65536), e.created_at,
(SELECT COUNT(*) FROM route_observations r WHERE r.event_id = e.id),
(SELECT COUNT(*) FROM delivery_observations d WHERE d.event_id = e.id),
COALESCE((SELECT d.status FROM delivery_observations d
          WHERE d.event_id = e.id
          ORDER BY d.observed_at DESC, d.id DESC LIMIT 1), '')
FROM observed_events e`
	if len(clauses) > 0 {
		statement += " WHERE " + strings.Join(clauses, " AND ")
	}
	statement += " ORDER BY observed_at DESC, id DESC LIMIT ?"
	args = append(args, query.Limit+1)
	rows, err := r.store.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return ObservationEventPage{}, fmt.Errorf("platformdb: list observed events: %w", err)
	}
	defer rows.Close()
	events := make([]ObservationEventListRecord, 0, query.Limit)
	for rows.Next() {
		event, scanErr := scanObservedEventList(rows)
		if scanErr != nil {
			return ObservationEventPage{}, fmt.Errorf("platformdb: scan observed event: %w", scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return ObservationEventPage{}, fmt.Errorf("platformdb: list observed events: %w", err)
	}
	page := ObservationEventPage{}
	if len(events) > query.Limit {
		last := events[query.Limit-1].ObservedEventRecord
		page.NextCursor = &ObservationCursor{ObservedAt: last.ObservedAt.Unix(), ID: last.ID}
		events = events[:query.Limit]
	}
	page.Events = events
	return page, nil
}

type observationScanner interface {
	Scan(dest ...any) error
}

func scanObservedEvent(scanner observationScanner) (ObservedEventRecord, error) {
	var (
		event                     ObservedEventRecord
		observedAt, sourceEventAt int64
		createdAt                 int64
		sourceEventAtNullable     sql.NullInt64
	)
	if err := scanner.Scan(
		&event.ID, &event.SchemaVersion, &event.Platform, &event.SourceKind,
		&event.SourceExternalID, &event.UpstreamEventID, &event.EventType,
		&observedAt, &sourceEventAtNullable, &event.ContentFingerprint,
		&event.PublicSnapshotJSON, &createdAt,
	); err != nil {
		return ObservedEventRecord{}, err
	}
	event.ObservedAt = time.Unix(observedAt, 0).UTC()
	if sourceEventAtNullable.Valid {
		sourceEventAt = sourceEventAtNullable.Int64
		value := time.Unix(sourceEventAt, 0).UTC()
		event.SourceEventAt = &value
	}
	event.CreatedAt = time.Unix(createdAt, 0).UTC()
	return event, nil
}

func scanObservedEventList(scanner observationScanner) (ObservationEventListRecord, error) {
	var (
		event                     ObservationEventListRecord
		observedAt, sourceEventAt int64
		createdAt                 int64
		sourceEventAtNullable     sql.NullInt64
	)
	if err := scanner.Scan(
		&event.ID, &event.SchemaVersion, &event.Platform, &event.SourceKind,
		&event.SourceExternalID, &event.UpstreamEventID, &event.EventType,
		&observedAt, &sourceEventAtNullable, &event.ContentFingerprint,
		&event.PublicSnapshotJSON, &createdAt, &event.RouteCount,
		&event.DeliveryCount, &event.FinalDeliveryStatus,
	); err != nil {
		return ObservationEventListRecord{}, err
	}
	event.ObservedAt = time.Unix(observedAt, 0).UTC()
	if sourceEventAtNullable.Valid {
		sourceEventAt = sourceEventAtNullable.Int64
		value := time.Unix(sourceEventAt, 0).UTC()
		event.SourceEventAt = &value
	}
	event.CreatedAt = time.Unix(createdAt, 0).UTC()
	return event, nil
}

func (r *ObservationRepository) GetObservedEvent(ctx context.Context, id string) (ObservedEventRecord, error) {
	if err := r.requireStore(); err != nil {
		return ObservedEventRecord{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ObservedEventRecord{}, ErrObservationInvalidQuery
	}
	row := r.store.db.QueryRowContext(normalizeContext(ctx), `
SELECT id, schema_version, platform, source_kind, source_external_id,
       upstream_event_id, event_type, observed_at, source_event_at,
       content_fingerprint, public_snapshot_json, created_at
FROM observed_events WHERE id = ?`, id)
	event, err := scanObservedEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ObservedEventRecord{}, ErrObservationNotFound
		}
		return ObservedEventRecord{}, fmt.Errorf("platformdb: get observed event: %w", err)
	}
	return event, nil
}

func (r *ObservationRepository) ListRouteObservationsForEvent(ctx context.Context, eventID string) ([]RouteObservationRecord, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return nil, ErrObservationInvalidQuery
	}
	rows, err := r.store.db.QueryContext(normalizeContext(ctx), `
SELECT id, event_id, route_ordinal, destination_kind, destination_external_id,
       outcome, reason_code, observed_at, created_at
FROM route_observations
WHERE event_id = ?
ORDER BY route_ordinal ASC, id ASC`, eventID)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list route observations: %w", err)
	}
	defer rows.Close()
	routes := make([]RouteObservationRecord, 0)
	for rows.Next() {
		var route RouteObservationRecord
		var observedAt, createdAt int64
		if err := rows.Scan(&route.ID, &route.EventID, &route.RouteOrdinal,
			&route.DestinationKind, &route.DestinationExternalID, &route.Outcome,
			&route.ReasonCode, &observedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan route observation: %w", err)
		}
		route.ObservedAt = time.Unix(observedAt, 0).UTC()
		route.CreatedAt = time.Unix(createdAt, 0).UTC()
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformdb: list route observations: %w", err)
	}
	return routes, nil
}

func (r *ObservationRepository) ListDeliveryObservationsForEvent(ctx context.Context, eventID string) ([]DeliveryObservationRecord, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return nil, ErrObservationInvalidQuery
	}
	rows, err := r.store.db.QueryContext(normalizeContext(ctx), `
SELECT id, event_id, route_observation_id, connector_kind,
       destination_external_id, status, result_code, observed_at, created_at
FROM delivery_observations
WHERE event_id = ?
ORDER BY observed_at ASC, id ASC`, eventID)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list delivery observations: %w", err)
	}
	defer rows.Close()
	deliveries := make([]DeliveryObservationRecord, 0)
	for rows.Next() {
		var delivery DeliveryObservationRecord
		var observedAt, createdAt int64
		if err := rows.Scan(&delivery.ID, &delivery.EventID, &delivery.RouteObservationID,
			&delivery.ConnectorKind, &delivery.DestinationExternalID, &delivery.Status,
			&delivery.ResultCode, &observedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan delivery observation: %w", err)
		}
		delivery.ObservedAt = time.Unix(observedAt, 0).UTC()
		delivery.CreatedAt = time.Unix(createdAt, 0).UTC()
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformdb: list delivery observations: %w", err)
	}
	return deliveries, nil
}

func (r *ObservationRepository) ObservationRecentCounts(ctx context.Context, now time.Time) (ObservationRecentCounts, error) {
	if err := r.requireStore(); err != nil {
		return ObservationRecentCounts{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.UTC().Add(-24 * time.Hour).Unix()
	var counts ObservationRecentCounts
	if err := r.store.db.QueryRowContext(normalizeContext(ctx), `
SELECT
 (SELECT COUNT(*) FROM observed_events WHERE observed_at >= ?),
 (SELECT COUNT(*) FROM route_observations WHERE observed_at >= ?),
 (SELECT COUNT(*) FROM delivery_observations WHERE observed_at >= ?)`, cutoff, cutoff, cutoff).
		Scan(&counts.Events24h, &counts.Routes24h, &counts.Deliveries24h); err != nil {
		return ObservationRecentCounts{}, fmt.Errorf("platformdb: observation recent counts: %w", err)
	}
	return counts, nil
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
