package migration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/deliverysnapshot"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

type recordingSender struct {
	result SendResult
	calls  int
}

func (s *recordingSender) Send(_ context.Context, _ domain.Target, _ deliverysnapshot.Payload) (SendResult, error) {
	s.calls++
	return s.result, nil
}

func coordinatorFixture(t *testing.T) (*Coordinator, *platformdb.DomainRepository, *platformdb.MigrationRepository, domain.Connector, domain.Connector) {
	t.Helper()
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d := platformdb.NewDomainRepository(store)
	old, err := d.CreateConnector(ctx, domain.Connector{Kind: domain.ConnectorOneBot, Name: "OneBot", Role: domain.ConnectorMain, Enabled: true, Status: domain.ConnectorActive, Endpoint: "onebot://test"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := d.CreateConnector(ctx, domain.Connector{Kind: domain.ConnectorSatori, Name: "Satori", Role: domain.ConnectorMain, Enabled: false, Status: domain.ConnectorActive, Endpoint: "satori://test"})
	if err != nil {
		t.Fatal(err)
	}
	r := platformdb.NewMigrationRepository(store)
	c := NewCoordinator(Config{Repository: r, Domain: d, Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	return c, d, r, old, next
}

func TestCoordinatorSwitchesOnlyAfterPreflight(t *testing.T) {
	ctx := context.Background()
	c, d, r, old, next := coordinatorFixture(t)
	m, err := c.Create(ctx, old.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != platformdb.MigrationDraft {
		t.Fatalf("state=%s", m.State)
	}
	if _, err := c.Commit(ctx, m.MigrationID); !errors.Is(err, ErrMigrationPreflight) {
		t.Fatalf("commit before preflight=%v", err)
	}
	if _, err := c.Preflight(ctx, m.MigrationID); err != nil {
		t.Fatal(err)
	}
	done, err := c.Commit(ctx, m.MigrationID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != platformdb.MigrationCompleted {
		t.Fatalf("state=%s", done.State)
	}
	active, err := d.ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var enabled string
	for _, v := range active {
		if v.Enabled {
			enabled = v.ID
		}
	}
	if enabled != next.ID {
		t.Fatalf("enabled connector=%s want %s", enabled, next.ID)
	}
	if _, err := r.ActiveMigration(ctx); !errors.Is(err, platformdb.ErrMigrationNotFound) {
		t.Fatalf("active migration=%v", err)
	}
}

func TestCoordinatorRebuildsProjectionBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	c, _, _, old, next := coordinatorFixture(t)
	called := 0
	c.SetProjectionRebuilder(func(context.Context) error {
		called++
		return nil
	})
	m, err := c.Create(ctx, old.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Preflight(ctx, m.MigrationID); err != nil {
		t.Fatal(err)
	}
	completed, err := c.Commit(ctx, m.MigrationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != platformdb.MigrationCompleted || called != 1 {
		t.Fatalf("state=%s projection rebuild calls=%d", completed.State, called)
	}
}

func TestProjectionRebuildFailureLeavesRecovery(t *testing.T) {
	ctx := context.Background()
	c, _, repo, old, next := coordinatorFixture(t)
	c.SetProjectionRebuilder(func(context.Context) error { return errors.New("projection unavailable") })
	m, err := c.Create(ctx, old.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Preflight(ctx, m.MigrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(ctx, m.MigrationID); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	stored, err := repo.Migration(ctx, m.MigrationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != platformdb.MigrationRecovery || stored.ProgressMarker != "projection_rebuild_failed" {
		t.Fatalf("recovery state=%s marker=%s", stored.State, stored.ProgressMarker)
	}
}

func TestCoordinatorDoesNotGuessMapping(t *testing.T) {
	ctx := context.Background()
	c, d, _, old, next := coordinatorFixture(t)
	m, err := c.Create(ctx, old.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RebuildProjection(ctx, []domain.LegacySubscription{{
		Platform: "bilibili", ExternalID: "source-1", DisplayName: "source", SubscriptionType: "dynamic",
		TargetType: string(domain.TargetGroup), TargetExternalID: "123", TargetDisplayName: "old", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := d.ListTargets(ctx)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	target := targets[0]
	if err := c.SetMappings(ctx, m.MigrationID, []platformdb.ConnectorMigrationMapping{{MigrationID: m.MigrationID, OldTargetID: target.ID, OldConnectorID: old.ID, OldTargetType: "group", OldExternalID: "123", Status: "ambiguous"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Preflight(ctx, m.MigrationID); !errors.Is(err, ErrMigrationMappingAmbiguous) {
		t.Fatalf("ambiguous mapping preflight=%v", err)
	}
}

func TestHeldDeliveryPersistsAndUnknownIsTerminal(t *testing.T) {
	ctx := context.Background()
	c, d, repo, old, next := coordinatorFixture(t)
	m, err := c.Create(ctx, old.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RebuildProjection(ctx, []domain.LegacySubscription{{
		Platform: "bilibili", ExternalID: "source-1", DisplayName: "source", SubscriptionType: "dynamic",
		TargetType: string(domain.TargetGroup), TargetExternalID: "123", TargetDisplayName: "old", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := d.ListTargets(ctx)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	oldTarget := targets[0]
	newTarget, err := d.UpsertTarget(ctx, domain.Target{ConnectorID: next.ID, TargetType: domain.TargetGroup, ExternalID: "456", DisplayName: "new", Status: domain.TargetResolved})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetMappings(ctx, m.MigrationID, []platformdb.ConnectorMigrationMapping{{MigrationID: m.MigrationID, OldTargetID: oldTarget.ID, OldConnectorID: old.ID, OldTargetType: string(oldTarget.TargetType), OldExternalID: oldTarget.ExternalID, NewTargetID: newTarget.ID, NewConnectorID: next.ID, NewTargetType: string(newTarget.TargetType), NewExternalID: newTarget.ExternalID, Status: "confirmed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Preflight(ctx, m.MigrationID); err != nil {
		t.Fatal(err)
	}
	// A no-subscription migration is preflight-ready; move it to committing to
	// exercise the hold boundary with an explicit target mapping.
	if err := repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationCommitting, "hold_active", "", time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	payload := deliverysnapshot.Payload{SchemaVersion: 1, DeliveryID: "delivery-1", MigrationID: m.MigrationID, EventID: "event-1", RouteID: "route-1", RouteSnapshot: json.RawMessage(`{"connector_id":"old"}`), LogicalTarget: deliverysnapshot.LogicalTarget{TargetID: oldTarget.ID, TargetType: string(oldTarget.TargetType), ExternalID: oldTarget.ExternalID}, Message: deliverysnapshot.MessageSnapshot{SchemaVersion: 1, Segments: []deliverysnapshot.Segment{{Type: "text", Data: map[string]any{"text": "hello"}}}}}
	if err := c.Hold(ctx, payload); err != nil {
		t.Fatal(err)
	}
	holds, err := repo.Holds(ctx, m.MigrationID)
	if err != nil || len(holds) != 1 {
		t.Fatalf("holds=%#v err=%v", holds, err)
	}
	holds = nil
	holds, err = repo.Holds(ctx, m.MigrationID)
	if err != nil || holds[0].Payload.DeliveryID != "delivery-1" {
		t.Fatalf("restored hold=%#v err=%v", holds, err)
	}
	sender := &recordingSender{result: SendUnknown}
	c.sender = sender
	if err := c.repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationCommitting, "route_committed", "", time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := c.releaseHolds(ctx, m.MigrationID, next.ID); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatalf("send calls=%d", sender.calls)
	}
	if err := c.releaseHolds(ctx, m.MigrationID, next.ID); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatalf("unknown hold resent calls=%d", sender.calls)
	}
}

func TestMigrationHoldFinalStateCheckPreventsStaleHold(t *testing.T) {
	ctx := context.Background()
	c, d, repo, old, next := coordinatorFixture(t)
	m, err := c.Create(ctx, old.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RebuildProjection(ctx, []domain.LegacySubscription{{
		Platform: "bilibili", ExternalID: "source-stale", DisplayName: "source", SubscriptionType: "dynamic",
		TargetType: string(domain.TargetGroup), TargetExternalID: "321", TargetDisplayName: "old", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := d.ListTargets(ctx)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	target := targets[0]
	if err := repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationPreparing, "targets_discovered", "", time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationPreflightReady, "preflight_validated", "", time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationCommitting, "hold_active", "", time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	payload := deliverysnapshot.Payload{
		SchemaVersion: 1, DeliveryID: "stale-delivery", MigrationID: m.MigrationID, EventID: "stale-event", RouteID: "stale-route",
		RouteSnapshot: json.RawMessage(`{"connector_id":"old"}`),
		LogicalTarget: deliverysnapshot.LogicalTarget{TargetID: target.ID, TargetType: string(target.TargetType), ExternalID: target.ExternalID},
		Message:       deliverysnapshot.MessageSnapshot{SchemaVersion: 1, Segments: []deliverysnapshot.Segment{{Type: "text", Data: map[string]any{"text": "hello"}}}},
	}
	// This models an eligibility read that succeeded, followed by a durable
	// migration transition before the hold repository boundary was reached.
	if err := repo.UpdateMigration(ctx, m.MigrationID, platformdb.MigrationFailed, "hold_persistence_failed", "", time.Unix(1700000001, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutHold(ctx, m.MigrationID, payload, time.Unix(1700000001, 0)); !errors.Is(err, platformdb.ErrMigrationInvalidState) {
		t.Fatalf("stale hold error=%v", err)
	}
	holds, err := repo.Holds(ctx, m.MigrationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 0 {
		t.Fatalf("stale hold persisted: %#v", holds)
	}
}
