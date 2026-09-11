package platformdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

var (
	ErrSourceExists       = errors.New("platformdb: source already exists")
	ErrSourceNotFound     = errors.New("platformdb: source not found")
	ErrTargetNotFound     = errors.New("platformdb: target not found")
	ErrConnectorNotFound  = errors.New("platformdb: connector not found")
	ErrProjectionNotFound = errors.New("platformdb: subscription projection not found")
	ErrDomainUnavailable  = errors.New("platformdb: domain unavailable")
	ErrProjectionDegraded = errors.New("platformdb: legacy applied; projection degraded")
)

// DomainRepository owns only the Phase 3 metadata/projection tables. It never
// writes Legacy BuntDB; callers must use the shared LegacySubscriptionService
// for subscription mutations first.
type DomainRepository struct{ store *Store }

func NewDomainRepository(store *Store) *DomainRepository {
	if store == nil {
		return nil
	}
	return &DomainRepository{store: store}
}

func (r *DomainRepository) requireStore() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrDomainUnavailable
	}
	return nil
}

func domainContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func domainJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "{}"
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(canonical)
}

func domainNow(now time.Time) int64 {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC().Unix()
}

func newDomainID() (string, error) {
	id, err := domain.NewID()
	if err != nil {
		return "", fmt.Errorf("platformdb: create domain id: %w", err)
	}
	return id, nil
}

func (r *DomainRepository) ListSources(ctx context.Context) ([]domain.Source, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `
SELECT id, platform, external_id, handle, display_name, canonical_url, status,
       metadata_json, created_at, updated_at
FROM sources ORDER BY platform, display_name, external_id, id`)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list sources: %w", err)
	}
	defer rows.Close()
	var result []domain.Source
	for rows.Next() {
		var source domain.Source
		if err := rows.Scan(&source.ID, &source.Platform, &source.ExternalID, &source.Handle,
			&source.DisplayName, &source.CanonicalURL, &source.Status, &source.MetadataJSON,
			&source.CreatedAt, &source.UpdatedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan source: %w", err)
		}
		result = append(result, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformdb: list sources: %w", err)
	}
	return result, nil
}

func (r *DomainRepository) Source(ctx context.Context, id string) (domain.Source, error) {
	if err := r.requireStore(); err != nil {
		return domain.Source{}, err
	}
	var source domain.Source
	err := r.store.db.QueryRowContext(domainContext(ctx), `
SELECT id, platform, external_id, handle, display_name, canonical_url, status,
       metadata_json, created_at, updated_at
FROM sources WHERE id = ?`, strings.TrimSpace(id)).Scan(
		&source.ID, &source.Platform, &source.ExternalID, &source.Handle,
		&source.DisplayName, &source.CanonicalURL, &source.Status, &source.MetadataJSON,
		&source.CreatedAt, &source.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Source{}, ErrSourceNotFound
	}
	if err != nil {
		return domain.Source{}, fmt.Errorf("platformdb: read source: %w", err)
	}
	return source, nil
}

func (r *DomainRepository) CreateSource(ctx context.Context, source domain.Source) (domain.Source, error) {
	if err := r.requireStore(); err != nil {
		return domain.Source{}, err
	}
	if source.ID == "" {
		var err error
		source.ID, err = newDomainID()
		if err != nil {
			return domain.Source{}, err
		}
	}
	source.Platform = domain.Platform(domain.NormalizePlatform(string(source.Platform)))
	if source.Status == "" {
		source.Status = domain.SourceActive
	}
	if source.MetadataJSON == "" {
		source.MetadataJSON = "{}"
	}
	if err := domain.ValidateSource(source); err != nil {
		return domain.Source{}, err
	}
	stamp := domainNow(time.Now())
	if source.CreatedAt == 0 {
		source.CreatedAt = stamp
	}
	source.UpdatedAt = stamp
	_, err := r.store.db.ExecContext(domainContext(ctx), `
INSERT INTO sources
(id, platform, external_id, handle, display_name, canonical_url, status, metadata_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, source.ID, source.Platform, strings.TrimSpace(source.ExternalID),
		strings.TrimSpace(source.Handle), strings.TrimSpace(source.DisplayName), strings.TrimSpace(source.CanonicalURL),
		source.Status, domainJSON(source.MetadataJSON), source.CreatedAt, source.UpdatedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.Source{}, ErrSourceExists
		}
		return domain.Source{}, fmt.Errorf("platformdb: create source: %w", err)
	}
	source.MetadataJSON = domainJSON(source.MetadataJSON)
	return source, nil
}

func (r *DomainRepository) UpsertSource(ctx context.Context, source domain.Source) (domain.Source, error) {
	if err := r.requireStore(); err != nil {
		return domain.Source{}, err
	}
	source.Platform = domain.Platform(domain.NormalizePlatform(string(source.Platform)))
	if source.Status == "" {
		source.Status = domain.SourceActive
	}
	if source.MetadataJSON == "" {
		source.MetadataJSON = "{}"
	}
	if source.ID == "" {
		var err error
		source.ID, err = newDomainID()
		if err != nil {
			return domain.Source{}, err
		}
	}
	stamp := domainNow(time.Now())
	var existingID string
	var created int64
	err := r.store.db.QueryRowContext(domainContext(ctx),
		"SELECT id, created_at FROM sources WHERE platform = ? AND external_id = ?",
		source.Platform, strings.TrimSpace(source.ExternalID)).Scan(&existingID, &created)
	if err == nil {
		source.ID = existingID
		source.CreatedAt = created
		source.UpdatedAt = stamp
		_, err = r.store.db.ExecContext(domainContext(ctx), `
UPDATE sources SET handle = ?, display_name = ?, canonical_url = ?, status = ?, metadata_json = ?, updated_at = ?
WHERE id = ?`, strings.TrimSpace(source.Handle), strings.TrimSpace(source.DisplayName), strings.TrimSpace(source.CanonicalURL),
			source.Status, domainJSON(source.MetadataJSON), stamp, source.ID)
		if err != nil {
			return domain.Source{}, fmt.Errorf("platformdb: update source projection: %w", err)
		}
		source.MetadataJSON = domainJSON(source.MetadataJSON)
		return source, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Source{}, fmt.Errorf("platformdb: inspect source projection: %w", err)
	}
	source.CreatedAt = stamp
	source.UpdatedAt = stamp
	return r.CreateSource(ctx, source)
}

func (r *DomainRepository) UpdateSource(ctx context.Context, source domain.Source) (domain.Source, error) {
	if err := r.requireStore(); err != nil {
		return domain.Source{}, err
	}
	if err := domain.ValidateSource(source); err != nil {
		return domain.Source{}, err
	}
	source.UpdatedAt = domainNow(time.Now())
	result, err := r.store.db.ExecContext(domainContext(ctx), `
UPDATE sources SET display_name = ?, handle = ?, canonical_url = ?, status = ?, metadata_json = ?, updated_at = ?
WHERE id = ?`, strings.TrimSpace(source.DisplayName), strings.TrimSpace(source.Handle), strings.TrimSpace(source.CanonicalURL),
		source.Status, domainJSON(source.MetadataJSON), source.UpdatedAt, source.ID)
	if err != nil {
		return domain.Source{}, fmt.Errorf("platformdb: update source: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.Source{}, ErrSourceNotFound
	}
	source.MetadataJSON = domainJSON(source.MetadataJSON)
	return source, nil
}

func (r *DomainRepository) DeleteSource(ctx context.Context, id string) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	var count int
	if err := r.store.db.QueryRowContext(domainContext(ctx), "SELECT COUNT(*) FROM subscription_projections WHERE source_id = ?", id).Scan(&count); err != nil {
		return fmt.Errorf("platformdb: check source use: %w", err)
	}
	if count > 0 {
		return domain.ErrSourceInUse
	}
	result, err := r.store.db.ExecContext(domainContext(ctx), "DELETE FROM sources WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("platformdb: delete source: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrSourceNotFound
	}
	return nil
}

func (r *DomainRepository) ListConnectors(ctx context.Context) ([]domain.Connector, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `
SELECT id, kind, name, role, enabled, status, endpoint, COALESCE(credential_id, ''), config_json, metadata_json, created_at, updated_at
FROM connectors ORDER BY role, name, id`)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list connectors: %w", err)
	}
	defer rows.Close()
	var result []domain.Connector
	for rows.Next() {
		var value domain.Connector
		var enabled int
		if err := rows.Scan(&value.ID, &value.Kind, &value.Name, &value.Role, &enabled, &value.Status,
			&value.Endpoint, &value.CredentialID, &value.ConfigJSON, &value.MetadataJSON, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan connector: %w", err)
		}
		value.Enabled = enabled != 0
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *DomainRepository) Connector(ctx context.Context, id string) (domain.Connector, error) {
	if err := r.requireStore(); err != nil {
		return domain.Connector{}, err
	}
	var value domain.Connector
	var enabled int
	err := r.store.db.QueryRowContext(domainContext(ctx), `
SELECT id, kind, name, role, enabled, status, endpoint, COALESCE(credential_id, ''), config_json, metadata_json, created_at, updated_at
FROM connectors WHERE id = ?`, id).Scan(&value.ID, &value.Kind, &value.Name, &value.Role, &enabled, &value.Status,
		&value.Endpoint, &value.CredentialID, &value.ConfigJSON, &value.MetadataJSON, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Connector{}, ErrConnectorNotFound
	}
	if err != nil {
		return domain.Connector{}, fmt.Errorf("platformdb: read connector: %w", err)
	}
	value.Enabled = enabled != 0
	return value, nil
}

func (r *DomainRepository) EnsureMainConnector(ctx context.Context, kind string) (domain.Connector, error) {
	if err := r.requireStore(); err != nil {
		return domain.Connector{}, err
	}
	if kind == "" {
		kind = domain.ConnectorOneBot
	}
	if kind != domain.ConnectorOneBot && kind != domain.ConnectorSatori {
		return domain.Connector{}, domain.ErrTopologyInvalid
	}
	connectors, err := r.ListConnectors(ctx)
	if err != nil {
		return domain.Connector{}, err
	}
	if err := domain.ValidateConnectorTopology(connectors); err != nil {
		return domain.Connector{}, err
	}
	for _, connector := range connectors {
		if connector.Enabled && connector.Role == domain.ConnectorMain {
			return connector, nil
		}
	}
	id, err := newDomainID()
	if err != nil {
		return domain.Connector{}, err
	}
	stamp := domainNow(time.Now())
	_, err = r.store.db.ExecContext(domainContext(ctx), `
INSERT INTO connectors (id, kind, name, role, enabled, status, endpoint, config_json, metadata_json, created_at, updated_at)
VALUES (?, ?, ?, 'main', 1, 'active', '', '{}', '{}', ?, ?)`, id, kind, strings.ToUpper(kind), stamp, stamp)
	if err != nil {
		return domain.Connector{}, fmt.Errorf("platformdb: create main connector: %w", err)
	}
	return r.Connector(ctx, id)
}

func (r *DomainRepository) UpdateConnector(ctx context.Context, connector domain.Connector) (domain.Connector, error) {
	if err := r.requireStore(); err != nil {
		return domain.Connector{}, err
	}
	if err := domain.ValidateConnector(connector); err != nil {
		return domain.Connector{}, err
	}
	connectors, err := r.ListConnectors(ctx)
	if err != nil {
		return domain.Connector{}, err
	}
	for index := range connectors {
		if connectors[index].ID == connector.ID {
			connectors[index] = connector
		}
	}
	if err := domain.ValidateConnectorTopology(connectors); err != nil {
		return domain.Connector{}, err
	}
	connector.UpdatedAt = domainNow(time.Now())
	result, err := r.store.db.ExecContext(domainContext(ctx), `
UPDATE connectors SET name = ?, enabled = ?, status = ?, endpoint = ?, credential_id = NULLIF(?, ''), config_json = ?, metadata_json = ?, updated_at = ?
WHERE id = ?`, strings.TrimSpace(connector.Name), boolInt(connector.Enabled), connector.Status,
		strings.TrimSpace(connector.Endpoint), strings.TrimSpace(connector.CredentialID), domainJSON(connector.ConfigJSON), domainJSON(connector.MetadataJSON), connector.UpdatedAt, connector.ID)
	if err != nil {
		return domain.Connector{}, fmt.Errorf("platformdb: update connector: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.Connector{}, ErrConnectorNotFound
	}
	connector.ConfigJSON = domainJSON(connector.ConfigJSON)
	connector.MetadataJSON = domainJSON(connector.MetadataJSON)
	return connector, nil
}

func (r *DomainRepository) ListTargets(ctx context.Context) ([]domain.Target, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `
SELECT id, connector_id, target_type, external_id, display_name, metadata_json, status, created_at, updated_at
FROM targets ORDER BY display_name, target_type, external_id, id`)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list targets: %w", err)
	}
	defer rows.Close()
	var result []domain.Target
	for rows.Next() {
		var value domain.Target
		if err := rows.Scan(&value.ID, &value.ConnectorID, &value.TargetType, &value.ExternalID, &value.DisplayName,
			&value.MetadataJSON, &value.Status, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan target: %w", err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *DomainRepository) Target(ctx context.Context, id string) (domain.Target, error) {
	if err := r.requireStore(); err != nil {
		return domain.Target{}, err
	}
	var value domain.Target
	err := r.store.db.QueryRowContext(domainContext(ctx), `
SELECT id, connector_id, target_type, external_id, display_name, metadata_json, status, created_at, updated_at
FROM targets WHERE id = ?`, id).Scan(&value.ID, &value.ConnectorID, &value.TargetType, &value.ExternalID, &value.DisplayName,
		&value.MetadataJSON, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Target{}, ErrTargetNotFound
	}
	if err != nil {
		return domain.Target{}, fmt.Errorf("platformdb: read target: %w", err)
	}
	return value, nil
}

func (r *DomainRepository) UpsertTarget(ctx context.Context, target domain.Target) (domain.Target, error) {
	if err := r.requireStore(); err != nil {
		return domain.Target{}, err
	}
	if target.ID == "" {
		var err error
		target.ID, err = newDomainID()
		if err != nil {
			return domain.Target{}, err
		}
	}
	if target.Status == "" {
		target.Status = domain.TargetResolved
	}
	if target.MetadataJSON == "" {
		target.MetadataJSON = "{}"
	}
	if err := domain.ValidateTarget(target); err != nil {
		return domain.Target{}, err
	}
	stamp := domainNow(time.Now())
	var id string
	var created int64
	err := r.store.db.QueryRowContext(domainContext(ctx), `
SELECT id, created_at FROM targets WHERE connector_id = ? AND target_type = ? AND external_id = ?`,
		target.ConnectorID, target.TargetType, target.ExternalID).Scan(&id, &created)
	if err == nil {
		target.ID, target.CreatedAt, target.UpdatedAt = id, created, stamp
		_, err = r.store.db.ExecContext(domainContext(ctx), `
UPDATE targets SET display_name = ?, metadata_json = ?, status = ?, updated_at = ? WHERE id = ?`,
			target.DisplayName, domainJSON(target.MetadataJSON), target.Status, stamp, target.ID)
		if err != nil {
			return domain.Target{}, fmt.Errorf("platformdb: update target projection: %w", err)
		}
		target.MetadataJSON = domainJSON(target.MetadataJSON)
		return target, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Target{}, fmt.Errorf("platformdb: inspect target projection: %w", err)
	}
	target.CreatedAt, target.UpdatedAt = stamp, stamp
	_, err = r.store.db.ExecContext(domainContext(ctx), `
INSERT INTO targets (id, connector_id, target_type, external_id, display_name, metadata_json, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, target.ID, target.ConnectorID, target.TargetType, target.ExternalID,
		target.DisplayName, domainJSON(target.MetadataJSON), target.Status, stamp, stamp)
	if err != nil {
		return domain.Target{}, fmt.Errorf("platformdb: create target projection: %w", err)
	}
	target.MetadataJSON = domainJSON(target.MetadataJSON)
	return target, nil
}

func (r *DomainRepository) UpdateTarget(ctx context.Context, target domain.Target) (domain.Target, error) {
	if err := r.requireStore(); err != nil {
		return domain.Target{}, err
	}
	if err := domain.ValidateTarget(target); err != nil {
		return domain.Target{}, err
	}
	target.UpdatedAt = domainNow(time.Now())
	result, err := r.store.db.ExecContext(domainContext(ctx), `
UPDATE targets SET display_name = ?, metadata_json = ?, status = ?, updated_at = ?
WHERE id = ?`, strings.TrimSpace(target.DisplayName), domainJSON(target.MetadataJSON), target.Status, target.UpdatedAt, target.ID)
	if err != nil {
		return domain.Target{}, fmt.Errorf("platformdb: update target: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return domain.Target{}, ErrTargetNotFound
	}
	target.MetadataJSON = domainJSON(target.MetadataJSON)
	return target, nil
}

func (r *DomainRepository) DeleteTarget(ctx context.Context, id string) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	var count int
	if err := r.store.db.QueryRowContext(domainContext(ctx), "SELECT COUNT(*) FROM subscription_projections WHERE target_id = ?", id).Scan(&count); err != nil {
		return fmt.Errorf("platformdb: check target use: %w", err)
	}
	if count > 0 {
		return domain.ErrTargetInUse
	}
	result, err := r.store.db.ExecContext(domainContext(ctx), "DELETE FROM targets WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("platformdb: delete target: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrTargetNotFound
	}
	return nil
}

func (r *DomainRepository) ListProjections(ctx context.Context) ([]domain.SubscriptionProjection, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `
SELECT id, source_id, target_id, legacy_key, enabled, legacy_options_snapshot_json, projection_status, projected_at
FROM subscription_projections ORDER BY projected_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list projections: %w", err)
	}
	defer rows.Close()
	var result []domain.SubscriptionProjection
	for rows.Next() {
		var value domain.SubscriptionProjection
		var enabled int
		if err := rows.Scan(&value.ID, &value.SourceID, &value.TargetID, &value.LegacyKey, &enabled,
			&value.LegacyOptionsSnapshotJSON, &value.ProjectionStatus, &value.ProjectedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan projection: %w", err)
		}
		value.Enabled = enabled != 0
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *DomainRepository) ProjectionsForSource(ctx context.Context, sourceID string) ([]domain.SubscriptionProjection, error) {
	return r.listProjectionsWhere(ctx, "source_id = ?", sourceID)
}

func (r *DomainRepository) ProjectionsForTarget(ctx context.Context, targetID string) ([]domain.SubscriptionProjection, error) {
	return r.listProjectionsWhere(ctx, "target_id = ?", targetID)
}

// ProjectionsForConnector returns projections whose target belongs to the
// connector. It is used by topology mutations so a connector kind switch is
// blocked only by that connector's active Legacy subscriptions, not by an
// unrelated connector in the same installation.
func (r *DomainRepository) ProjectionsForConnector(ctx context.Context, connectorID string) ([]domain.SubscriptionProjection, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `
SELECT p.id, p.source_id, p.target_id, p.legacy_key, p.enabled,
       p.legacy_options_snapshot_json, p.projection_status, p.projected_at
FROM subscription_projections p
JOIN targets t ON t.id = p.target_id
WHERE t.connector_id = ?
ORDER BY p.projected_at DESC, p.id`, strings.TrimSpace(connectorID))
	if err != nil {
		return nil, fmt.Errorf("platformdb: list connector projections: %w", err)
	}
	defer rows.Close()
	var result []domain.SubscriptionProjection
	for rows.Next() {
		var value domain.SubscriptionProjection
		var enabled int
		if err := rows.Scan(&value.ID, &value.SourceID, &value.TargetID, &value.LegacyKey, &enabled,
			&value.LegacyOptionsSnapshotJSON, &value.ProjectionStatus, &value.ProjectedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan connector projection: %w", err)
		}
		value.Enabled = enabled != 0
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformdb: list connector projections: %w", err)
	}
	return result, nil
}

func (r *DomainRepository) listProjectionsWhere(ctx context.Context, predicate string, value string) ([]domain.SubscriptionProjection, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(domainContext(ctx), `
SELECT id, source_id, target_id, legacy_key, enabled, legacy_options_snapshot_json, projection_status, projected_at
FROM subscription_projections WHERE `+predicate+` ORDER BY projected_at DESC, id`, value)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list projections: %w", err)
	}
	defer rows.Close()
	var result []domain.SubscriptionProjection
	for rows.Next() {
		var value domain.SubscriptionProjection
		var enabled int
		if err := rows.Scan(&value.ID, &value.SourceID, &value.TargetID, &value.LegacyKey, &enabled,
			&value.LegacyOptionsSnapshotJSON, &value.ProjectionStatus, &value.ProjectedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan projection: %w", err)
		}
		value.Enabled = enabled != 0
		result = append(result, value)
	}
	return result, rows.Err()
}

// RebuildProjection replaces the SQLite projection from an authoritative
// Legacy snapshot. BuntDB is never written and an empty snapshot is valid.
func (r *DomainRepository) RebuildProjection(ctx context.Context, records []domain.LegacySubscription) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	ctx = domainContext(ctx)
	connector, err := r.EnsureMainConnector(ctx, domain.ConnectorOneBot)
	if err != nil {
		return err
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformdb: begin projection rebuild: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DELETE FROM subscription_projections"); err != nil {
		return fmt.Errorf("platformdb: clear projection: %w", err)
	}
	stamp := domainNow(time.Now())
	for _, record := range records {
		source, err := upsertSourceTx(ctx, tx, record, stamp)
		if err != nil {
			return err
		}
		target, err := upsertTargetTx(ctx, tx, connector, record, stamp)
		if err != nil {
			return err
		}
		projectionID, err := newDomainID()
		if err != nil {
			return err
		}
		legacyKey := record.LegacyKey
		if legacyKey == "" {
			legacyKey = fmt.Sprintf("%s:%s:%s|%s:%s", record.Platform, record.ExternalID, record.SubscriptionType, record.TargetType, record.TargetExternalID)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO subscription_projections
(id, source_id, target_id, legacy_key, enabled, legacy_options_snapshot_json, projection_status, projected_at)
VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`, projectionID, source.ID, target.ID, legacyKey, boolInt(record.Enabled), domainJSON(record.OptionsJSON), stamp); err != nil {
			return fmt.Errorf("platformdb: insert projection: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformdb: commit projection rebuild: %w", err)
	}
	committed = true
	return nil
}

func upsertSourceTx(ctx context.Context, tx *sql.Tx, record domain.LegacySubscription, stamp int64) (domain.Source, error) {
	platform := domain.NormalizePlatform(record.Platform)
	var source domain.Source
	err := tx.QueryRowContext(ctx, `SELECT id, created_at FROM sources WHERE platform = ? AND external_id = ?`, platform, record.ExternalID).Scan(&source.ID, &source.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		id, idErr := newDomainID()
		if idErr != nil {
			return domain.Source{}, idErr
		}
		source = domain.Source{ID: id, Platform: domain.Platform(platform), ExternalID: record.ExternalID, Handle: record.ExternalID,
			DisplayName: record.DisplayName, CanonicalURL: record.CanonicalURL, Status: domain.SourceActive,
			MetadataJSON: "{}", CreatedAt: stamp, UpdatedAt: stamp}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sources
(id, platform, external_id, handle, display_name, canonical_url, status, metadata_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, source.ID, source.Platform, source.ExternalID, source.Handle, source.DisplayName,
			source.CanonicalURL, source.Status, source.MetadataJSON, stamp, stamp); err != nil {
			return domain.Source{}, fmt.Errorf("platformdb: insert projected source: %w", err)
		}
		return source, nil
	}
	if err != nil {
		return domain.Source{}, fmt.Errorf("platformdb: inspect projected source: %w", err)
	}
	source.Platform, source.ExternalID, source.Handle, source.DisplayName, source.CanonicalURL, source.Status, source.MetadataJSON, source.UpdatedAt = domain.Platform(platform), record.ExternalID, record.ExternalID, record.DisplayName, record.CanonicalURL, domain.SourceActive, "{}", stamp
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET handle = ?, display_name = ?, canonical_url = ?, status = ?, updated_at = ? WHERE id = ?`, source.Handle, source.DisplayName, source.CanonicalURL, source.Status, stamp, source.ID); err != nil {
		return domain.Source{}, fmt.Errorf("platformdb: update projected source: %w", err)
	}
	return source, nil
}

func upsertTargetTx(ctx context.Context, tx *sql.Tx, connector domain.Connector, record domain.LegacySubscription, stamp int64) (domain.Target, error) {
	targetType := domain.TargetType(record.TargetType)
	if targetType == "" {
		targetType = domain.TargetGroup
	}
	var target domain.Target
	err := tx.QueryRowContext(ctx, `SELECT id, created_at FROM targets WHERE connector_id = ? AND target_type = ? AND external_id = ?`, connector.ID, targetType, record.TargetExternalID).Scan(&target.ID, &target.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		id, idErr := newDomainID()
		if idErr != nil {
			return domain.Target{}, idErr
		}
		target = domain.Target{ID: id, ConnectorID: connector.ID, TargetType: targetType, ExternalID: record.TargetExternalID,
			DisplayName: record.TargetDisplayName, MetadataJSON: "{}", Status: domain.TargetResolved, CreatedAt: stamp, UpdatedAt: stamp}
		if _, err := tx.ExecContext(ctx, `INSERT INTO targets
(id, connector_id, target_type, external_id, display_name, metadata_json, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, '{}', ?, ?, ?)`, target.ID, target.ConnectorID, target.TargetType, target.ExternalID, target.DisplayName, target.Status, stamp, stamp); err != nil {
			return domain.Target{}, fmt.Errorf("platformdb: insert projected target: %w", err)
		}
		return target, nil
	}
	if err != nil {
		return domain.Target{}, fmt.Errorf("platformdb: inspect projected target: %w", err)
	}
	target.ConnectorID, target.TargetType, target.ExternalID, target.DisplayName, target.Status, target.UpdatedAt = connector.ID, targetType, record.TargetExternalID, record.TargetDisplayName, domain.TargetResolved, stamp
	if _, err := tx.ExecContext(ctx, `UPDATE targets SET display_name = ?, status = ?, updated_at = ? WHERE id = ?`, target.DisplayName, target.Status, stamp, target.ID); err != nil {
		return domain.Target{}, fmt.Errorf("platformdb: update projected target: %w", err)
	}
	return target, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
