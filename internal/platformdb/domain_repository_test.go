package platformdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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
