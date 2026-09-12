package migration

// Coordinator is the durable, connector-neutral state machine for Phase 3B.
// It owns no goroutine and never stores a Messenger, renderer, credential or
// client pointer. Every transition is written to SQLite before the next
// externally visible step is attempted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/deliverysnapshot"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

const RollbackWindow = 7 * 24 * time.Hour

var (
	ErrMigrationNotFound          = platformdb.ErrMigrationNotFound
	ErrMigrationInProgress        = platformdb.ErrMigrationInProgress
	ErrMigrationMappingRequired   = platformdb.ErrMigrationMappingRequired
	ErrMigrationMappingAmbiguous  = platformdb.ErrMigrationMappingAmbiguous
	ErrMigrationTargetUnavailable = platformdb.ErrMigrationTargetUnavailable
	ErrMigrationPreflight         = platformdb.ErrMigrationPreflight
	ErrMigrationRecovery          = platformdb.ErrMigrationRecovery
	ErrMigrationRollbackExpired   = platformdb.ErrMigrationRollbackExpired
)

type SendResult string

const (
	SendSent     SendResult = "sent"
	SendQueued   SendResult = "queued"
	SendNotSent  SendResult = "not_sent"
	SendUnknown  SendResult = "unknown"
	SendRejected SendResult = "rejected"
)

// Sender is deliberately narrow. Implementations may be backed by OneBot,
// Satori or Telegram, but the snapshot passed here contains no secret or
// process-local object. Unknown results are terminal and are never retried.
type Sender interface {
	Send(context.Context, domain.Target, deliverysnapshot.Payload) (SendResult, error)
}

type ConnectorChecker interface {
	Test(context.Context, domain.Connector) error
}

type Config struct {
	Repository *platformdb.MigrationRepository
	Domain     *platformdb.DomainRepository
	Checker    ConnectorChecker
	Sender     Sender
	Now        func() time.Time
	// ProjectionRebuilder is an optional, synchronous bridge to the
	// authoritative Legacy subscription snapshot. It is deliberately a
	// function boundary rather than a stored Legacy service pointer: the
	// coordinator remains restart-safe and does not own any runtime object.
	// When configured, a successful route switch is not marked completed until
	// the rebuild has succeeded.
	ProjectionRebuilder func(context.Context) error
}

type Coordinator struct {
	repo                *platformdb.MigrationRepository
	domain              *platformdb.DomainRepository
	checker             ConnectorChecker
	sender              Sender
	now                 func() time.Time
	projectionRebuilder func(context.Context) error
}

func NewCoordinator(config Config) *Coordinator {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Coordinator{repo: config.Repository, domain: config.Domain, checker: config.Checker, sender: config.Sender, now: now, projectionRebuilder: config.ProjectionRebuilder}
}

// SetProjectionRebuilder installs the optional Legacy projection bridge after
// all runtime services have been constructed. Installing it only changes a
// function pointer; it starts no goroutine and performs no I/O.
func (c *Coordinator) SetProjectionRebuilder(rebuilder func(context.Context) error) {
	if c == nil {
		return
	}
	c.projectionRebuilder = rebuilder
}

func (c *Coordinator) rebuildProjection(ctx context.Context) error {
	if c == nil || c.projectionRebuilder == nil {
		return nil
	}
	return c.projectionRebuilder(ctx)
}

func (c *Coordinator) require() error {
	if c == nil || c.repo == nil || c.domain == nil {
		return platformdb.ErrDomainUnavailable
	}
	return nil
}
func (c *Coordinator) stamp() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	return c.now().UTC()
}

func topologyJSON(value domain.Connector) string {
	// Durable snapshots carry topology identity and credential references only;
	// never copy an unsanitized connector config (which may contain a manually
	// inserted secret) into the migration journal. Non-sensitive topology
	// options are retained so a seven-day rollback has a useful snapshot.
	b, _ := json.Marshal(map[string]any{"id": value.ID, "kind": value.Kind, "name": value.Name, "role": value.Role, "enabled": value.Enabled, "status": value.Status, "endpoint": safeEndpoint(value.Endpoint), "credential_id": value.CredentialID, "config": safeJSON(value.ConfigJSON), "metadata": safeJSON(value.MetadataJSON)})
	return string(b)
}

func safeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// A malformed endpoint may contain credentials or bearer material in
		// the portion that failed to parse. Never copy it into a durable
		// migration snapshot; an invalid topology is handled by preflight.
		return ""
	}
	if parsed.User != nil {
		parsed.User = url.User(parsed.User.Username())
	}
	query := parsed.Query()
	for key := range query {
		if sensitiveTopologyKey(key) {
			query.Set(key, "[redacted]")
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

func sensitiveTopologyKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	for _, token := range []string{"token", "secret", "password", "api_key", "apikey", "private_key", "access_key"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func safeJSON(raw string) any {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return map[string]any{}
	}
	return redactTopology(value)
}

func redactTopology(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			keyNorm := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
			if keyNorm != "credential_id" && keyNorm != "credential_ref" && sensitiveTopologyKey(keyNorm) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactTopology(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactTopology(item)
		}
		return out
	default:
		return value
	}
}

func (c *Coordinator) Create(ctx context.Context, oldID, newID string) (platformdb.ConnectorMigration, error) {
	if err := c.require(); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if _, err := c.repo.ActiveMigration(ctx); err != nil && !errors.Is(err, platformdb.ErrMigrationNotFound) {
		// A journal read failure is not evidence that there is no active
		// migration.  Refuse to create a second coordinator record rather than
		// turning a degraded platform database into an unsafe concurrent switch.
		return platformdb.ConnectorMigration{}, err
	} else if err == nil {
		return platformdb.ConnectorMigration{}, ErrMigrationInProgress
	}
	oldConnector, err := c.domain.Connector(ctx, oldID)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	newConnector, err := c.domain.Connector(ctx, newID)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if oldConnector.Role != domain.ConnectorMain || newConnector.Role != domain.ConnectorMain {
		return platformdb.ConnectorMigration{}, ErrMigrationPreflight
	}
	id, err := domain.NewID()
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	now := c.stamp()
	value := platformdb.ConnectorMigration{MigrationID: id, OldConnectorID: oldID, NewConnectorID: newID, State: platformdb.MigrationDraft, StartedAt: now.Unix(), UpdatedAt: now.Unix(), OldTopologySnapshotJSON: topologyJSON(oldConnector), NewTopologySnapshotJSON: topologyJSON(newConnector), MappingSnapshotJSON: "[]", AffectedTargetIDsJSON: "[]", ProgressMarker: "draft"}
	if err := c.repo.CreateMigration(ctx, value); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	c.audit(ctx, value.MigrationID, "migration.create", "created", map[string]any{"old_connector_id": oldID, "new_connector_id": newID})
	return c.repo.Migration(ctx, id)
}

func (c *Coordinator) SetMappings(ctx context.Context, id string, mappings []platformdb.ConnectorMigrationMapping) error {
	if err := c.require(); err != nil {
		return err
	}
	m, err := c.repo.Migration(ctx, id)
	if err != nil {
		return err
	}
	if m.State != platformdb.MigrationDraft && m.State != platformdb.MigrationPreparing {
		return platformdb.ErrMigrationInvalidState
	}
	if err := c.repo.ReplaceMappings(ctx, id, mappings, c.stamp()); err != nil {
		return err
	}
	c.audit(ctx, id, "migration.mapping_update", "accepted", map[string]any{"count": len(mappings)})
	return nil
}

// Discover enumerates the rebuildable projection targets affected by a
// migration and persists a required mapping row for each one. It is a
// read/prepare stage: no connector route is switched and no Legacy mutation
// is performed. Existing user-confirmed mappings are preserved so the UI can
// safely call Discover again after a refresh.
func (c *Coordinator) Discover(ctx context.Context, id string) ([]platformdb.ConnectorMigrationMapping, error) {
	if err := c.require(); err != nil {
		return nil, err
	}
	m, err := c.repo.Migration(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.State != platformdb.MigrationDraft && m.State != platformdb.MigrationPreparing {
		return nil, platformdb.ErrMigrationInvalidState
	}
	oldConnector, err := c.domain.Connector(ctx, m.OldConnectorID)
	if err != nil {
		return nil, err
	}
	if oldConnector.Role != domain.ConnectorMain {
		return nil, ErrMigrationPreflight
	}
	projections, err := c.domain.ProjectionsForConnector(ctx, oldConnector.ID)
	if err != nil {
		return nil, err
	}
	targets, err := c.domain.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	byTarget := make(map[string]domain.Target, len(targets))
	for _, target := range targets {
		byTarget[target.ID] = target
	}
	existing, err := c.repo.Mappings(ctx, id)
	if err != nil {
		return nil, err
	}
	byOld := make(map[string]platformdb.ConnectorMigrationMapping, len(existing))
	for _, mapping := range existing {
		byOld[mapping.OldTargetID] = mapping
	}
	result := make([]platformdb.ConnectorMigrationMapping, 0, len(projections))
	affected := make([]string, 0, len(projections))
	for _, projection := range projections {
		// Only active Legacy subscriptions participate in a route migration;
		// stale/degraded projection rows remain observable metadata but must not
		// block a switch or force an invented target mapping.
		if !projection.Enabled || projection.ProjectionStatus != domain.ProjectionActive {
			continue
		}
		target, ok := byTarget[projection.TargetID]
		if !ok || target.ConnectorID != oldConnector.ID {
			return nil, ErrMigrationTargetUnavailable
		}
		mapping := byOld[target.ID]
		if mapping.OldTargetID == "" {
			mapping = platformdb.ConnectorMigrationMapping{MigrationID: id, OldTargetID: target.ID, OldConnectorID: oldConnector.ID, OldTargetType: string(target.TargetType), OldExternalID: target.ExternalID, Status: "required"}
		} else {
			// Identity fields come from the current authoritative projection, not
			// from a stale browser payload. New-target fields remain the explicit
			// user choice and are checked during Preflight.
			mapping.MigrationID = id
			mapping.OldConnectorID = oldConnector.ID
			mapping.OldTargetType = string(target.TargetType)
			mapping.OldExternalID = target.ExternalID
		}
		result = append(result, mapping)
		affected = append(affected, target.ID)
	}
	if err := c.repo.ReplaceMappings(ctx, id, result, c.stamp()); err != nil {
		return nil, err
	}
	if err := c.repo.ConfirmMigration(ctx, id, result, affected, c.stamp()); err != nil {
		return nil, err
	}
	if m.State == platformdb.MigrationDraft {
		if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationPreparing, "targets_discovered", "", c.stamp()); err != nil {
			return nil, err
		}
	}
	c.audit(ctx, id, "migration.discover", "completed", map[string]any{"affected_targets": len(affected)})
	return result, nil
}

func (c *Coordinator) Preflight(ctx context.Context, id string) (map[string]any, error) {
	if err := c.require(); err != nil {
		return nil, err
	}
	m, err := c.repo.Migration(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.State != platformdb.MigrationDraft && m.State != platformdb.MigrationPreparing {
		return nil, platformdb.ErrMigrationInvalidState
	}
	// Discovery is intentionally repeatable. This makes the preflight endpoint
	// useful to API clients that do not have a separate wizard discovery step,
	// while still preserving any previously confirmed mapping choices.
	if _, err := c.Discover(ctx, id); err != nil {
		return nil, err
	}
	m, err = c.repo.Migration(ctx, id)
	if err != nil {
		return nil, err
	}
	oldConnector, err := c.domain.Connector(ctx, m.OldConnectorID)
	if err != nil {
		return nil, err
	}
	newConnector, err := c.domain.Connector(ctx, m.NewConnectorID)
	if err != nil {
		return nil, err
	}
	if oldConnector.Status != domain.ConnectorActive || !oldConnector.Enabled {
		return nil, ErrMigrationPreflight
	}
	if newConnector.Kind == oldConnector.Kind || newConnector.Status != domain.ConnectorActive || strings.TrimSpace(newConnector.Endpoint) == "" {
		return nil, ErrMigrationTargetUnavailable
	}
	if c.checker != nil {
		if err := c.checker.Test(ctx, newConnector); err != nil {
			return nil, fmt.Errorf("%w: connector test failed", ErrMigrationPreflight)
		}
	}
	projections, err := c.domain.ProjectionsForConnector(ctx, oldConnector.ID)
	if err != nil {
		return nil, err
	}
	targets, err := c.domain.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	byTarget := make(map[string]domain.Target)
	for _, t := range targets {
		byTarget[t.ID] = t
	}
	mappings, err := c.repo.Mappings(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := c.validateMigrationMappings(ctx, m, mappings); err != nil {
		return nil, err
	}
	mappingByOld := make(map[string]platformdb.ConnectorMigrationMapping)
	for _, mapping := range mappings {
		oldTarget, exists := byTarget[mapping.OldTargetID]
		if !exists || oldTarget.ConnectorID != m.OldConnectorID || string(oldTarget.TargetType) != mapping.OldTargetType || oldTarget.ExternalID != mapping.OldExternalID {
			return nil, ErrMigrationMappingRequired
		}
		mappingByOld[mapping.OldTargetID] = mapping
		if mapping.Status == "ambiguous" {
			return nil, ErrMigrationMappingAmbiguous
		}
		if mapping.Status != "confirmed" || mapping.NewTargetID == "" {
			return nil, ErrMigrationMappingRequired
		}
		newTarget, ok := byTarget[mapping.NewTargetID]
		if !ok || newTarget.ConnectorID != m.NewConnectorID || newTarget.Status != domain.TargetResolved || (mapping.NewTargetType != "" && string(newTarget.TargetType) != mapping.NewTargetType) || (mapping.NewExternalID != "" && newTarget.ExternalID != mapping.NewExternalID) {
			return nil, ErrMigrationTargetUnavailable
		}
	}
	affected := make([]string, 0, len(projections))
	for _, p := range projections {
		if !p.Enabled || p.ProjectionStatus != domain.ProjectionActive {
			continue
		}
		if _, ok := mappingByOld[p.TargetID]; !ok {
			return nil, ErrMigrationMappingRequired
		}
		affected = append(affected, p.TargetID)
	}
	now := c.stamp()
	if err := c.repo.ConfirmMigration(ctx, id, mappings, affected, now); err != nil {
		return nil, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationPreflightReady, "preflight_validated", "", now); err != nil {
		return nil, err
	}
	c.audit(ctx, id, "migration.preflight", "passed", map[string]any{"affected_targets": len(affected)})
	return map[string]any{"migration_id": id, "state": platformdb.MigrationPreflightReady, "affected_target_ids": affected, "mapping_count": len(mappings)}, nil
}

func (c *Coordinator) Commit(ctx context.Context, id string) (platformdb.ConnectorMigration, error) {
	if err := c.require(); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	m, err := c.repo.Migration(ctx, id)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if m.State != platformdb.MigrationPreflightReady {
		return platformdb.ConnectorMigration{}, ErrMigrationPreflight
	}
	// Preflight is a user-facing validation snapshot, not an authorization
	// cache. Re-read the durable topology immediately before recording the
	// commit boundary so an administrator cannot change either connector (or
	// enable a second main) between the wizard and the route switch.
	if err := c.validateCommitTopology(ctx, m); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	mappings, err := c.repo.Mappings(ctx, id)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.validateMigrationMappings(ctx, m, mappings); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	now := c.stamp()
	c.audit(ctx, id, "migration.commit", "started", map[string]any{"mapping_count": len(mappings)})
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "commit_started", "", now); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "hold_active", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.domain.SwitchMainConnector(ctx, m.OldConnectorID, m.NewConnectorID); err != nil {
		// SwitchMainConnector is transactional, so the old route remains the
		// only authoritative route on an error. A delivery may nevertheless
		// have been held between the durable hold marker and this failed switch;
		// release only this migration's rows back to that old route. If that
		// release cannot be proven safe, keep the journal recoverable instead of
		// claiming a clean failure.
		if releaseErr := c.releaseHolds(ctx, id, m.OldConnectorID); releaseErr != nil {
			_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "old_route_release_incomplete", errCode(releaseErr), c.stamp())
		} else if rebuildErr := c.rebuildProjection(ctx); rebuildErr != nil {
			_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "old_projection_rebuild_failed", errCode(rebuildErr), c.stamp())
		} else {
			_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationFailed, "switch_failed", errCode(err), c.stamp())
		}
		c.audit(ctx, id, "migration.commit", "failed", map[string]any{"error": "switch_failed"})
		return platformdb.ConnectorMigration{}, err
	}
	// Read back the durable route before touching any held delivery. This is
	// deliberately separate from the UPDATE transaction: it proves the
	// database now exposes exactly one enabled main connector of the requested
	// kind and gives restart recovery an unambiguous marker.
	connectors, readErr := c.domain.ListConnectors(ctx)
	if readErr != nil || !hasOnlyEnabledMain(connectors, m.NewConnectorID) {
		if readErr == nil {
			readErr = ErrMigrationPreflight
		}
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "route_readback_failed", errCode(readErr), c.stamp())
		return platformdb.ConnectorMigration{}, readErr
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "route_committed", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	deadline := c.stamp().Add(RollbackWindow).Unix() // keep the durable rollback window even while releasing
	if err := c.setRollbackExpiry(ctx, id, deadline); err != nil {
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "rollback_window_persist_failed", errCode(err), c.stamp())
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.releaseHolds(ctx, id, m.NewConnectorID); err != nil {
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "release_incomplete", errCode(err), c.stamp())
		return platformdb.ConnectorMigration{}, err
	}
	// Rebuild the SQLite projection from a fresh Legacy snapshot before the
	// migration becomes terminal. BuntDB remains the authority; this callback
	// only updates the rebuildable projection. The marker makes a crash between
	// the callback and the terminal journal write recoverable and idempotent.
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "projection_rebuild", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.rebuildProjection(ctx); err != nil {
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "projection_rebuild_failed", errCode(err), c.stamp())
		c.audit(ctx, id, "migration.commit", "projection_rebuild_failed", map[string]any{"error": "projection_rebuild_failed"})
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "projection_rebuilt", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCompleted, "completed", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	c.audit(ctx, id, "migration.commit", "completed", map[string]any{"rollback_expires_at": deadline})
	return c.repo.Migration(ctx, id)
}

func (c *Coordinator) setRollbackExpiry(ctx context.Context, id string, deadline int64) error {
	m, err := c.repo.Migration(ctx, id)
	if err != nil {
		return err
	}
	m.RollbackExpiresAt = &deadline // use direct bounded update through confirmation, preserving mapping snapshots
	return c.repo.StoreMigrationWithRollback(ctx, m)
}

func (c *Coordinator) releaseHolds(ctx context.Context, id, newConnectorID string) error {
	migrationRecord, err := c.repo.Migration(ctx, id)
	if err != nil {
		return err
	}
	mappings, err := c.repo.Mappings(ctx, id)
	if err != nil {
		return err
	}
	byOld := make(map[string]platformdb.ConnectorMigrationMapping, len(mappings))
	byNew := make(map[string]platformdb.ConnectorMigrationMapping, len(mappings))
	for _, mapping := range mappings {
		byOld[mapping.OldTargetID] = mapping
		if mapping.NewTargetID != "" {
			byNew[mapping.NewTargetID] = mapping
		}
	}
	holds, err := c.repo.Holds(ctx, id)
	if err != nil {
		return err
	}
	// Any releasing row predates this coordinator instance and therefore has
	// an unknown external-send outcome. Close it before checking the sender so
	// recovery never attempts an unconditional resend.
	if err := c.repo.MarkReleasingUnknown(ctx, id, c.stamp()); err != nil {
		return err
	}
	if len(holds) > 0 && c.sender == nil {
		return errors.New("migration release sender unavailable")
	}
	for _, hold := range holds {
		if hold.ReleaseState == "released" || hold.ReleaseState == "unknown" {
			continue
		}
		if hold.ReleaseState == "releasing" {
			continue
		}
		if c.sender == nil {
			continue
		}
		claimed, claimErr := c.repo.ClaimHold(ctx, hold.DeliveryID, c.stamp())
		if claimErr != nil {
			continue
		}
		// Legacy v1 rows may not have the durable payload_json column populated.
		// Never turn an incomplete historical row into a guessed external send:
		// a held payload must be self-contained before it can leave the durable
		// migration boundary.  Mark it terminally unknown so restart recovery
		// cannot retry an outcome that was never safely reconstructable.
		if payloadErr := claimed.Payload.Validate(); payloadErr != nil {
			_ = c.repo.FinishHold(ctx, hold.DeliveryID, "unknown", "invalid_snapshot", c.stamp())
			return fmt.Errorf("%w: invalid held payload: %v", platformdb.ErrMigrationRecovery, payloadErr)
		}
		mapping, target, targetErr := c.releaseTarget(ctx, claimed.Payload, migrationRecord, byOld, byNew, newConnectorID)
		if targetErr != nil {
			_ = c.repo.FinishHold(ctx, hold.DeliveryID, "failed", errCode(targetErr), c.stamp())
			return targetErr
		}
		claimed.Payload.LogicalTarget = deliverysnapshot.LogicalTarget{TargetID: target.ID, TargetType: string(target.TargetType), ExternalID: target.ExternalID, LegacyRouteKey: claimed.Payload.LogicalTarget.LegacyRouteKey}
		claimed.Payload.LogicalTargetSnapshot = claimed.Payload.LogicalTarget
		claimed.Payload.RouteSnapshot = routeForTarget(claimed.Payload.RouteSnapshot, target, mapping)
		result, sendErr := c.sender.Send(ctx, target, claimed.Payload)
		if sendErr != nil {
			_ = c.repo.FinishHold(ctx, hold.DeliveryID, "failed", errCode(sendErr), c.stamp())
			return sendErr
		}
		if result == SendUnknown {
			_ = c.repo.FinishHold(ctx, hold.DeliveryID, "unknown", "unknown", c.stamp())
			continue
		}
		if result != SendSent && result != SendQueued {
			_ = c.repo.FinishHold(ctx, hold.DeliveryID, "failed", string(result), c.stamp())
			return fmt.Errorf("migration release failed: %s", result)
		}
		if err := c.repo.FinishHold(ctx, hold.DeliveryID, "released", string(result), c.stamp()); err != nil {
			return err
		}
	}
	return nil
}

func hasOnlyEnabledMain(connectors []domain.Connector, expected string) bool {
	var enabled string
	for _, connector := range connectors {
		if connector.Role == domain.ConnectorMain && connector.Enabled {
			if enabled != "" {
				return false
			}
			enabled = connector.ID
		}
	}
	return enabled == expected
}

func (c *Coordinator) releaseTarget(ctx context.Context, payload deliverysnapshot.Payload, migrationRecord platformdb.ConnectorMigration, byOld, byNew map[string]platformdb.ConnectorMigrationMapping, destinationConnector string) (platformdb.ConnectorMigrationMapping, domain.Target, error) {
	oldID := payload.LogicalTarget.TargetID
	mapping, ok := byOld[oldID]
	if destinationConnector == migrationRecord.NewConnectorID {
		if !ok || mapping.Status != "confirmed" || mapping.NewTargetID == "" {
			return platformdb.ConnectorMigrationMapping{}, domain.Target{}, ErrMigrationMappingRequired
		}
		target, err := c.domain.Target(ctx, mapping.NewTargetID)
		if err != nil {
			return mapping, domain.Target{}, err
		}
		if target.ConnectorID != destinationConnector || target.Status != domain.TargetResolved {
			return mapping, domain.Target{}, ErrMigrationTargetUnavailable
		}
		return mapping, target, nil
	}
	if !ok {
		// A rollback payload may already contain the new target identity if a
		// delivery was produced after the successful switch. Resolve it through
		// the inverse map rather than guessing an external ID.
		mapping, ok = byNew[oldID]
	}
	if !ok {
		return platformdb.ConnectorMigrationMapping{}, domain.Target{}, ErrMigrationMappingRequired
	}
	target, err := c.domain.Target(ctx, mapping.OldTargetID)
	if err != nil {
		return mapping, domain.Target{}, err
	}
	if target.ConnectorID != destinationConnector || target.Status != domain.TargetResolved {
		return mapping, domain.Target{}, ErrMigrationTargetUnavailable
	}
	return mapping, target, nil
}

func routeForTarget(raw json.RawMessage, target domain.Target, mapping platformdb.ConnectorMigrationMapping) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		value = map[string]any{}
	}
	value["connector_id"] = target.ConnectorID
	value["target_id"] = target.ID
	value["target_type"] = string(target.TargetType)
	value["external_id"] = target.ExternalID
	if mapping.NewExternalID != "" && target.ID == mapping.NewTargetID {
		value["mapped_from_target_id"] = mapping.OldTargetID
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// validateMigrationMappings re-reads both sides of every confirmed mapping.
// Preflight is not an authorization cache: targets can be deleted or become
// unresolved while an administrator is reviewing the wizard. Every durable
// route-changing operation therefore repeats this identity check immediately
// before switching connectors.
func (c *Coordinator) validateMigrationMappings(ctx context.Context, m platformdb.ConnectorMigration, mappings []platformdb.ConnectorMigrationMapping) error {
	targets, err := c.domain.ListTargets(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]domain.Target, len(targets))
	for _, target := range targets {
		byID[target.ID] = target
	}
	seenNew := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		if mapping.Status == "ambiguous" {
			return ErrMigrationMappingAmbiguous
		}
		if mapping.Status != "confirmed" || strings.TrimSpace(mapping.OldTargetID) == "" || strings.TrimSpace(mapping.NewTargetID) == "" {
			return ErrMigrationMappingRequired
		}
		oldTarget, ok := byID[mapping.OldTargetID]
		if !ok || oldTarget.ConnectorID != m.OldConnectorID || string(oldTarget.TargetType) != mapping.OldTargetType || oldTarget.ExternalID != mapping.OldExternalID || oldTarget.Status != domain.TargetResolved {
			return ErrMigrationTargetUnavailable
		}
		newTarget, ok := byID[mapping.NewTargetID]
		if !ok || newTarget.ConnectorID != m.NewConnectorID || newTarget.Status != domain.TargetResolved || (mapping.NewTargetType != "" && string(newTarget.TargetType) != mapping.NewTargetType) || (mapping.NewExternalID != "" && newTarget.ExternalID != mapping.NewExternalID) {
			return ErrMigrationTargetUnavailable
		}
		if _, exists := seenNew[newTarget.ID]; exists {
			return ErrMigrationMappingAmbiguous
		}
		seenNew[newTarget.ID] = struct{}{}
	}
	return nil
}

func (c *Coordinator) validateCommitTopology(ctx context.Context, m platformdb.ConnectorMigration) error {
	oldConnector, err := c.domain.Connector(ctx, m.OldConnectorID)
	if err != nil {
		return err
	}
	newConnector, err := c.domain.Connector(ctx, m.NewConnectorID)
	if err != nil {
		return err
	}
	connectors, err := c.domain.ListConnectors(ctx)
	if err != nil {
		return err
	}
	if err := domain.ValidateConnectorTopology(connectors); err != nil {
		return ErrMigrationPreflight
	}
	if oldConnector.Role != domain.ConnectorMain || oldConnector.Status != domain.ConnectorActive || !oldConnector.Enabled || !hasOnlyEnabledMain(connectors, oldConnector.ID) {
		return ErrMigrationPreflight
	}
	if newConnector.Role != domain.ConnectorMain || newConnector.Enabled || newConnector.Kind == oldConnector.Kind || newConnector.Status != domain.ConnectorActive || strings.TrimSpace(newConnector.Endpoint) == "" {
		return ErrMigrationTargetUnavailable
	}
	return nil
}

func rollbackMarkerAllowed(m platformdb.ConnectorMigration) bool {
	if m.State == platformdb.MigrationCompleted {
		return m.ProgressMarker == "completed" || m.ProgressMarker == "projection_rebuilt"
	}
	if m.State != platformdb.MigrationRecovery {
		return false
	}
	switch m.ProgressMarker {
	case "route_committed", "release_incomplete", "projection_rebuild", "projection_rebuild_failed", "projection_rebuilt":
		return true
	default:
		return false
	}
}

func (c *Coordinator) Rollback(ctx context.Context, id string) (platformdb.ConnectorMigration, error) {
	if err := c.require(); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	m, err := c.repo.Migration(ctx, id)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if m.State != platformdb.MigrationCompleted && m.State != platformdb.MigrationRecovery {
		return platformdb.ConnectorMigration{}, platformdb.ErrMigrationInvalidState
	}
	if !rollbackMarkerAllowed(m) {
		// A recovery journal without a known committed-new-route marker is
		// intentionally not guessable. The administrator must inspect and
		// resolve it rather than allowing rollback to act on an unknown route.
		return platformdb.ConnectorMigration{}, ErrMigrationRecovery
	}
	if m.RollbackExpiresAt != nil && c.stamp().Unix() >= *m.RollbackExpiresAt {
		return platformdb.ConnectorMigration{}, ErrMigrationRollbackExpired
	}
	connectors, err := c.domain.ListConnectors(ctx)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if !hasOnlyEnabledMain(connectors, m.NewConnectorID) {
		// Rollback is only safe when the committed route is unambiguous. If the
		// route is already old, split, or absent, leave the journal recoverable.
		return platformdb.ConnectorMigration{}, ErrMigrationRecovery
	}
	mappings, err := c.repo.Mappings(ctx, id)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.validateMigrationMappings(ctx, m, mappings); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	oldConnector, err := c.domain.Connector(ctx, m.OldConnectorID)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if oldConnector.Status == domain.ConnectorUnavailable || strings.TrimSpace(oldConnector.Endpoint) == "" {
		return platformdb.ConnectorMigration{}, ErrMigrationTargetUnavailable
	}
	if c.checker != nil {
		if err := c.checker.Test(ctx, oldConnector); err != nil {
			return platformdb.ConnectorMigration{}, fmt.Errorf("%w: old connector test failed", ErrMigrationPreflight)
		}
	}
	// Subscriptions created on the new connector after the migration may not
	// be represented by the original mapping snapshot. Refuse rollback until
	// every such target has an explicit inverse mapping.
	newProjections, err := c.domain.ProjectionsForConnector(ctx, m.NewConnectorID)
	if err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	knownNew := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		if mapping.NewTargetID != "" {
			knownNew[mapping.NewTargetID] = struct{}{}
		}
	}
	for _, projection := range newProjections {
		if !projection.Enabled || projection.ProjectionStatus != domain.ProjectionActive {
			continue
		}
		if _, ok := knownNew[projection.TargetID]; !ok {
			return platformdb.ConnectorMigration{}, ErrMigrationMappingRequired
		}
	}
	c.audit(ctx, id, "migration.rollback", "started", nil)
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "rollback_started", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.domain.SwitchMainConnector(ctx, m.NewConnectorID, m.OldConnectorID); err != nil {
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "rollback_switch_failed", errCode(err), c.stamp())
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.releaseHolds(ctx, id, m.OldConnectorID); err != nil {
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "rollback_release_incomplete", errCode(err), c.stamp())
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "rollback_projection_rebuild", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.rebuildProjection(ctx); err != nil {
		_ = c.repo.UpdateMigration(ctx, id, platformdb.MigrationRecovery, "rollback_projection_rebuild_failed", errCode(err), c.stamp())
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationCommitting, "rollback_projection_rebuilt", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	if err := c.repo.UpdateMigration(ctx, id, platformdb.MigrationRolledBack, "rolled_back", "", c.stamp()); err != nil {
		return platformdb.ConnectorMigration{}, err
	}
	c.audit(ctx, id, "migration.rollback", "completed", nil)
	return c.repo.Migration(ctx, id)
}

// completeNewRouteRecovery resumes the durable stages after the new route was
// committed. It is safe to call repeatedly: held rows claim themselves
// atomically, unknown rows are terminal, and projection rebuilds are derived
// from the Legacy snapshot rather than from a process-local cache.
func (c *Coordinator) completeNewRouteRecovery(ctx context.Context, m platformdb.ConnectorMigration) error {
	connectors, err := c.domain.ListConnectors(ctx)
	if err != nil {
		return err
	}
	if !hasOnlyEnabledMain(connectors, m.NewConnectorID) {
		// The marker says the new route was committed, but the current durable
		// topology must still prove that claim before any held delivery is sent.
		// A split/old/absent route is ambiguous and remains recoverable for an
		// administrator instead of being guessed into a destination.
		return ErrMigrationRecovery
	}
	if m.RollbackExpiresAt == nil {
		deadline := c.stamp().Add(RollbackWindow).Unix()
		if err := c.repo.SetRollbackExpiry(ctx, m.MigrationID, deadline, c.stamp()); err != nil {
			return err
		}
	}
	if err := c.releaseHolds(ctx, m.MigrationID, m.NewConnectorID); err != nil {
		return err
	}
	if err := c.rebuildProjection(ctx); err != nil {
		return err
	}
	if err := c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationCompleted, "completed", "", c.stamp()); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) completeOldRouteFailure(ctx context.Context, m platformdb.ConnectorMigration, terminalState platformdb.MigrationState, marker string) error {
	connectors, err := c.domain.ListConnectors(ctx)
	if err != nil {
		return err
	}
	if !hasOnlyEnabledMain(connectors, m.OldConnectorID) {
		return ErrMigrationRecovery
	}
	if err := c.releaseHolds(ctx, m.MigrationID, m.OldConnectorID); err != nil {
		return err
	}
	if err := c.rebuildProjection(ctx); err != nil {
		return err
	}
	return c.repo.UpdateMigration(ctx, m.MigrationID, terminalState, marker, "", c.stamp())
}

// recoverBeforeRouteCommit classifies the durable connector topology before
// touching it.  A pre-route crash normally finds the old connector active; if
// the new connector is active, the switch crossed its external boundary and
// must be reversed.  Anything else is ambiguous and is left in recovery
// rather than guessed into a route.
func (c *Coordinator) recoverBeforeRouteCommit(ctx context.Context, m platformdb.ConnectorMigration) error {
	connectors, err := c.domain.ListConnectors(ctx)
	if err != nil {
		return err
	}
	oldActive := hasOnlyEnabledMain(connectors, m.OldConnectorID)
	newActive := hasOnlyEnabledMain(connectors, m.NewConnectorID)
	if !oldActive && !newActive {
		return ErrMigrationRecovery
	}
	if newActive {
		if err := c.domain.SwitchMainConnector(ctx, m.NewConnectorID, m.OldConnectorID); err != nil {
			return err
		}
	}
	return c.completeOldRouteFailure(ctx, m, platformdb.MigrationFailed, "rolled_back_after_restart")
}

func (c *Coordinator) Recover(ctx context.Context) error {
	if err := c.require(); err != nil {
		return err
	}
	values, err := c.repo.UnfinishedMigrations(ctx)
	if err != nil {
		return err
	}
	for _, m := range values {
		switch m.ProgressMarker {
		case "route_committed", "release_incomplete", "projection_rebuild", "projection_rebuild_failed", "projection_rebuilt":
			if err := c.completeNewRouteRecovery(ctx, m); err != nil {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "release_incomplete", errCode(err), c.stamp())
				continue
			}
		case "draft":
			// A draft has not crossed the route-switch boundary. It is safe to
			// retire it without touching connector topology.
			_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationFailed, "abandoned_after_restart", "", c.stamp())
		case "commit_started", "hold_active", "preflight_validated":
			if err := c.recoverBeforeRouteCommit(ctx, m); err != nil {
				marker := "old_route_release_incomplete"
				if errors.Is(err, ErrMigrationRecovery) {
					marker = "route_ambiguous"
				}
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, marker, errCode(err), c.stamp())
				continue
			}
		case "old_route_release_incomplete", "old_projection_rebuild_failed":
			// The failed switch path is already on the old route. Do not switch
			// again; finish only the durable release/rebuild stages.
			if err := c.completeOldRouteFailure(ctx, m, platformdb.MigrationFailed, "switch_failed"); err != nil {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "old_route_release_incomplete", errCode(err), c.stamp())
				continue
			}
		case "rollback_projection_rebuild", "rollback_projection_rebuild_failed", "rollback_projection_rebuilt":
			if err := c.rebuildProjection(ctx); err != nil {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "rollback_projection_rebuild_failed", errCode(err), c.stamp())
				continue
			}
			_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRolledBack, "rolled_back", "", c.stamp())
		case "rollback_started", "rollback_release_incomplete":
			// Rollback is itself a durable two-stage operation. If the process
			// disappears before its final marker, inspect the current durable
			// topology and finish the rollback only when the route is unambiguous.
			connectors, listErr := c.domain.ListConnectors(ctx)
			if listErr != nil {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "rollback_route_unknown", errCode(listErr), c.stamp())
				continue
			}
			oldActive := hasOnlyEnabledMain(connectors, m.OldConnectorID)
			newActive := hasOnlyEnabledMain(connectors, m.NewConnectorID)
			if !oldActive && !newActive {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "rollback_route_unknown", "", c.stamp())
				continue
			}
			if newActive {
				if switchErr := c.domain.SwitchMainConnector(ctx, m.NewConnectorID, m.OldConnectorID); switchErr != nil {
					_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "rollback_switch_failed", errCode(switchErr), c.stamp())
					continue
				}
			}
			if releaseErr := c.releaseHolds(ctx, m.MigrationID, m.OldConnectorID); releaseErr != nil {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "rollback_release_incomplete", errCode(releaseErr), c.stamp())
				continue
			}
			if rebuildErr := c.rebuildProjection(ctx); rebuildErr != nil {
				_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "rollback_projection_rebuild_failed", errCode(rebuildErr), c.stamp())
				continue
			}
			_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRolledBack, "rolled_back", "", c.stamp())
		default:
			_ = c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationRecovery, "recovery_required", "", c.stamp())
		}
		c.audit(ctx, m.MigrationID, "migration.recovery", "processed", map[string]any{"marker": m.ProgressMarker})
	}
	return nil
}

func (c *Coordinator) Hold(ctx context.Context, payload deliverysnapshot.Payload) error {
	if err := c.require(); err != nil {
		return err
	}
	if strings.TrimSpace(payload.LogicalTarget.TargetID) == "" {
		payload.LogicalTarget = payload.LogicalTargetSnapshot
	}
	m, err := c.repo.Migration(ctx, payload.MigrationID)
	if err != nil {
		return err
	}
	if m.State != platformdb.MigrationCommitting {
		return platformdb.ErrMigrationInvalidState
	}
	target, err := c.domain.Target(ctx, payload.LogicalTarget.TargetID)
	if err != nil {
		return err
	}
	if target.ConnectorID != m.OldConnectorID || target.Status != domain.TargetResolved {
		return ErrMigrationTargetUnavailable
	}
	if !containsString(m.AffectedTargetIDsJSON, payload.LogicalTarget.TargetID) {
		mappings, mappingErr := c.repo.Mappings(ctx, payload.MigrationID)
		if mappingErr != nil {
			return mappingErr
		}
		found := false
		for _, mapping := range mappings {
			if mapping.OldTargetID == payload.LogicalTarget.TargetID {
				found = true
				break
			}
		}
		if !found {
			return ErrMigrationTargetUnavailable
		}
	}
	return c.repo.PutHold(ctx, payload.MigrationID, payload, c.stamp())
}

// TryHoldForTarget is the delivery-hook boundary used by a future connector
// runtime. It returns held=true only for an active committing migration whose
// old route owns the logical target. Outside that narrow maintenance window
// callers continue the normal Legacy send path unchanged.
func (c *Coordinator) TryHoldForTarget(ctx context.Context, payload deliverysnapshot.Payload) (held bool, err error) {
	if err := c.require(); err != nil {
		return false, err
	}
	active, err := c.repo.ActiveMigration(ctx)
	if errors.Is(err, platformdb.ErrMigrationNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if active.State != platformdb.MigrationCommitting || strings.TrimSpace(payload.MigrationID) != active.MigrationID {
		return false, nil
	}
	if err := c.Hold(ctx, payload); err != nil {
		// Affected delivery must never fall through to an uncertain Messenger
		// route when its durable snapshot cannot be persisted. Record the
		// failure in the migration journal before returning to the Legacy hook;
		// startup recovery will then keep the migration in an explicit recovery
		// state instead of silently continuing with a partial hold set.
		_ = c.repo.UpdateMigration(ctx, active.MigrationID, platformdb.MigrationRecovery, "hold_persistence_failed", errCode(err), c.stamp())
		c.audit(ctx, active.MigrationID, "migration.hold", "failed", map[string]any{"error": "hold_persistence_failed"})
		return false, err
	}
	return true, nil
}

func containsString(encoded, value string) bool {
	var values []string
	if json.Unmarshal([]byte(encoded), &values) != nil {
		return false
	}
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func (c *Coordinator) audit(ctx context.Context, id, action, outcome string, metadata map[string]any) {
	if c.repo == nil {
		return
	}
	raw := "{}"
	if metadata != nil {
		if data, err := json.Marshal(metadata); err == nil {
			raw = string(data)
		}
	}
	_, _ = c.repo.AppendAudit(ctx, platformdb.AuditEntry{OccurredAt: c.stamp().Unix(), Action: action, ResourceType: "connector_migration", ResourceID: id, Outcome: outcome, MetadataJSON: raw})
}
func errCode(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(strings.ToLower(err.Error()), " ", "_")
}
