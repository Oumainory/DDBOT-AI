package platformdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

func TestDomainProjectionRebuildsFromLegacySnapshotAndPreservesTargetTypeIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "domain.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewDomainRepository(store)
	connector, err := repo.EnsureMainConnector(ctx, domain.ConnectorOneBot)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.IsUUIDv7(connector.ID) {
		t.Fatalf("connector id %q is not UUIDv7", connector.ID)
	}
	targetGroup, err := repo.UpsertTarget(ctx, domain.Target{ConnectorID: connector.ID, TargetType: domain.TargetGroup, ExternalID: "42", DisplayName: "group"})
	if err != nil {
		t.Fatal(err)
	}
	targetChannel, err := repo.UpsertTarget(ctx, domain.Target{ConnectorID: connector.ID, TargetType: domain.TargetChannel, ExternalID: "42", DisplayName: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	if targetGroup.ID == targetChannel.ID {
		t.Fatal("target type must be part of target identity")
	}
	records := []domain.LegacySubscription{{
		Platform: "bilibili", ExternalID: "401742377", DisplayName: "原神", SubscriptionType: "dynamic",
		TargetType: "group", TargetExternalID: "42", TargetDisplayName: "group", Enabled: true,
		LegacyKey: "bilibili:401742377:dynamic|group:42", OptionsJSON: `{"text":["版本"]}`,
	}}
	if err := repo.RebuildProjection(ctx, records); err != nil {
		t.Fatal(err)
	}
	projections, err := repo.ListProjections(ctx)
	if err != nil || len(projections) != 1 {
		t.Fatalf("projections = %#v, %v", projections, err)
	}
	source, err := repo.Source(ctx, projections[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.IsUUIDv7(source.ID) || source.Platform != domain.PlatformBilibili {
		t.Fatalf("projected source = %#v", source)
	}
	if err := repo.DeleteTarget(ctx, targetGroup.ID); !errors.Is(err, domain.ErrTargetInUse) {
		t.Fatalf("DeleteTarget(in use) = %v, want target_in_use", err)
	}
	if err := repo.RebuildProjection(ctx, nil); err != nil {
		t.Fatal(err)
	}
	projections, err = repo.ListProjections(ctx)
	if err != nil || len(projections) != 0 {
		t.Fatalf("empty rebuild projections = %#v, %v", projections, err)
	}
}

func TestDomainProjectionRollbackDoesNotLeavePartialRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "rollback.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewDomainRepository(store)
	records := []domain.LegacySubscription{{Platform: "bilibili", ExternalID: "1", SubscriptionType: "dynamic", TargetType: "private", TargetExternalID: "9"}}
	if err := repo.RebuildProjection(ctx, records); err == nil {
		t.Fatal("invalid target type rebuild unexpectedly succeeded")
	}
	projections, err := repo.ListProjections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 0 {
		t.Fatalf("rollback left projections: %#v", projections)
	}
}

func TestDomainProjectionUsesConfirmedMigrationTargetForActiveRoute(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "migration-projection.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	domainRepo := NewDomainRepository(store)
	oldConnector, err := domainRepo.CreateConnector(ctx, domain.Connector{Kind: domain.ConnectorOneBot, Name: "OneBot", Role: domain.ConnectorMain, Enabled: true, Status: domain.ConnectorActive, Endpoint: "onebot://test"})
	if err != nil {
		t.Fatal(err)
	}
	newConnector, err := domainRepo.CreateConnector(ctx, domain.Connector{Kind: domain.ConnectorSatori, Name: "Satori", Role: domain.ConnectorMain, Enabled: false, Status: domain.ConnectorActive, Endpoint: "satori://test"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := []domain.LegacySubscription{{Platform: "bilibili", ExternalID: "source-1", DisplayName: "source", SubscriptionType: "dynamic", TargetType: string(domain.TargetGroup), TargetExternalID: "123", TargetDisplayName: "old", Enabled: true}}
	if err := domainRepo.RebuildProjection(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	oldTargets, err := domainRepo.ListTargets(ctx)
	if err != nil || len(oldTargets) != 1 {
		t.Fatalf("old targets=%#v err=%v", oldTargets, err)
	}
	newTarget, err := domainRepo.UpsertTarget(ctx, domain.Target{ConnectorID: newConnector.ID, TargetType: domain.TargetChannel, ExternalID: "guild/channel", DisplayName: "new channel", Status: domain.TargetResolved})
	if err != nil {
		t.Fatal(err)
	}
	migrationRepo := NewMigrationRepository(store)
	createdAt := time.Unix(1700000000, 0).UTC()
	if err := migrationRepo.CreateMigration(ctx, ConnectorMigration{MigrationID: "migration-projection", OldConnectorID: oldConnector.ID, NewConnectorID: newConnector.ID, State: MigrationDraft, StartedAt: createdAt.Unix(), UpdatedAt: createdAt.Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := migrationRepo.ReplaceMappings(ctx, "migration-projection", []ConnectorMigrationMapping{{MigrationID: "migration-projection", OldTargetID: oldTargets[0].ID, OldConnectorID: oldConnector.ID, OldTargetType: string(domain.TargetGroup), OldExternalID: "123", NewTargetID: newTarget.ID, NewConnectorID: newConnector.ID, NewTargetType: string(domain.TargetChannel), NewExternalID: "guild/channel", Status: "confirmed"}}, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := migrationRepo.UpdateMigration(ctx, "migration-projection", MigrationPreparing, "targets_discovered", "", createdAt); err != nil {
		t.Fatal(err)
	}
	if err := migrationRepo.UpdateMigration(ctx, "migration-projection", MigrationPreflightReady, "preflight_validated", "", createdAt); err != nil {
		t.Fatal(err)
	}
	if err := migrationRepo.UpdateMigration(ctx, "migration-projection", MigrationCommitting, "route_committed", "", createdAt); err != nil {
		t.Fatal(err)
	}
	if err := domainRepo.SwitchMainConnector(ctx, oldConnector.ID, newConnector.ID); err != nil {
		t.Fatal(err)
	}
	if err := domainRepo.RebuildProjection(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	projections, err := domainRepo.ListProjections(ctx)
	if err != nil || len(projections) != 1 {
		t.Fatalf("projections=%#v err=%v", projections, err)
	}
	if projections[0].TargetID != newTarget.ID {
		t.Fatalf("projected target=%s want mapped target %s", projections[0].TargetID, newTarget.ID)
	}
}
