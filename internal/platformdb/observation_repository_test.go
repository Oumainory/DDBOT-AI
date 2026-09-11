package platformdb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObservationRepositoryPersistsFactsAndRelationships(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "observations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewObservationRepository(store)

	observedAt := time.Unix(1700000000, 0).UTC()
	event := ObservedEventRecord{
		ID:                 "evt_test",
		SchemaVersion:      1,
		Platform:           "bilibili",
		SourceKind:         "account",
		SourceExternalID:   "401742377",
		UpstreamEventID:    "dynamic-1",
		EventType:          "dynamic",
		ObservedAt:         observedAt,
		ContentFingerprint: strings.Repeat("a", 64),
		PublicSnapshotJSON: `{"platform":"bilibili","event_type":"dynamic","text":"public"}`,
		CreatedAt:          observedAt,
	}
	if err := repository.InsertObservedEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	route := RouteObservationRecord{
		ID:                    "route_test",
		EventID:               event.ID,
		RouteOrdinal:          0,
		DestinationKind:       "qq_group",
		DestinationExternalID: "123456",
		Outcome:               "pass",
		ReasonCode:            "legacy_pass",
		ObservedAt:            observedAt,
		CreatedAt:             observedAt,
	}
	if err := repository.InsertRouteObservation(ctx, route); err != nil {
		t.Fatal(err)
	}
	delivery := DeliveryObservationRecord{
		ID:                    "delivery_test",
		EventID:               event.ID,
		RouteObservationID:    route.ID,
		ConnectorKind:         "onebot",
		DestinationExternalID: "123456",
		Status:                "sent",
		ResultCode:            "sent",
		ObservedAt:            observedAt,
		CreatedAt:             observedAt,
	}
	if err := repository.InsertDeliveryObservation(ctx, delivery); err != nil {
		t.Fatal(err)
	}

	events, routes, deliveries, err := repository.ObservationCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 || routes != 1 || deliveries != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/1", events, routes, deliveries)
	}
	snapshot, err := repository.ObservationSnapshot(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != event.PublicSnapshotJSON {
		t.Fatalf("snapshot = %q, want %q", snapshot, event.PublicSnapshotJSON)
	}

	if err := repository.InsertRouteObservation(ctx, RouteObservationRecord{
		ID:                    "orphan-route",
		EventID:               "missing-event",
		RouteOrdinal:          0,
		DestinationKind:       "qq_group",
		DestinationExternalID: "123456",
		Outcome:               "unknown",
		ReasonCode:            "unknown",
		ObservedAt:            observedAt,
		CreatedAt:             observedAt,
	}); err == nil {
		t.Fatal("orphan route was accepted")
	}
	if err := repository.InsertDeliveryObservation(ctx, DeliveryObservationRecord{
		ID:                    "orphan-delivery",
		EventID:               event.ID,
		RouteObservationID:    "missing-route",
		ConnectorKind:         "onebot",
		DestinationExternalID: "123456",
		Status:                "sent",
		ResultCode:            "sent",
		ObservedAt:            observedAt,
		CreatedAt:             observedAt,
	}); err == nil {
		t.Fatal("orphan delivery was accepted")
	}
	if err := repository.InsertRouteObservation(ctx, RouteObservationRecord{
		ID:                    "invalid-outcome",
		EventID:               event.ID,
		DestinationKind:       "qq_group",
		DestinationExternalID: "123456",
		Outcome:               "drop",
		ReasonCode:            "unknown",
		ObservedAt:            observedAt,
		CreatedAt:             observedAt,
	}); err == nil {
		t.Fatal("invalid route outcome was accepted")
	}
}

func TestObservationRepositoryPruneCascadesInBoundedBatches(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "observations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewObservationRepository(store)
	old := time.Unix(1700000000, 0).UTC()
	fresh := old.Add(24 * time.Hour)
	for _, item := range []struct {
		id string
		at time.Time
	}{
		{"evt_old_a", old},
		{"evt_old_b", old.Add(time.Second)},
		{"evt_fresh", fresh},
	} {
		if err := repository.InsertObservedEvent(ctx, ObservedEventRecord{
			ID:                 item.id,
			SchemaVersion:      1,
			Platform:           "twitter",
			SourceKind:         "account",
			EventType:          "tweet",
			ObservedAt:         item.at,
			ContentFingerprint: strings.Repeat("b", 64),
			PublicSnapshotJSON: `{"platform":"twitter","event_type":"tweet"}`,
			CreatedAt:          item.at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.InsertRouteObservation(ctx, RouteObservationRecord{
		ID:                    "route_old",
		EventID:               "evt_old_a",
		DestinationKind:       "qq_group",
		DestinationExternalID: "1",
		Outcome:               "filtered",
		ReasonCode:            "filter_hook",
		ObservedAt:            old,
		CreatedAt:             old,
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.PruneObservationsBefore(ctx, fresh, 1); err != nil {
		t.Fatal(err)
	} else if count != 1 {
		t.Fatalf("first prune count = %d, want 1", count)
	}
	events, routes, _, err := repository.ObservationCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if events != 2 || routes != 0 {
		t.Fatalf("counts after bounded prune = %d/%d, want 2/0", events, routes)
	}
	if count, err := repository.PruneObservationsBefore(ctx, fresh, 16); err != nil {
		t.Fatal(err)
	} else if count != 1 {
		t.Fatalf("second prune count = %d, want 1", count)
	}
	events, routes, deliveries, err := repository.ObservationCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 || routes != 0 || deliveries != 0 {
		t.Fatalf("final counts = %d/%d/%d, want 1/0/0", events, routes, deliveries)
	}
	if _, err := repository.PruneObservationsBefore(ctx, fresh, 0); !errors.Is(err, ErrObservationInvalidRetention) {
		t.Fatalf("invalid prune batch error = %v", err)
	}
}

func TestObservationRepositoryValidationDoesNotPersistPartialFacts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "observations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewObservationRepository(store)
	if err := repository.InsertObservedEvent(ctx, ObservedEventRecord{ID: "bad", SchemaVersion: 2}); !errors.Is(err, ErrObservationInvalidEvent) {
		t.Fatalf("invalid event error = %v", err)
	}
	if err := repository.InsertRouteObservation(ctx, RouteObservationRecord{ID: "bad", EventID: "missing"}); !errors.Is(err, ErrObservationInvalidRoute) {
		t.Fatalf("invalid route error = %v", err)
	}
	if err := repository.InsertDeliveryObservation(ctx, DeliveryObservationRecord{ID: "bad", EventID: "missing"}); !errors.Is(err, ErrObservationInvalidDelivery) {
		t.Fatalf("invalid delivery error = %v", err)
	}
	events, routes, deliveries, err := repository.ObservationCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if events != 0 || routes != 0 || deliveries != 0 {
		t.Fatalf("invalid writes changed counts = %d/%d/%d", events, routes, deliveries)
	}
}

func TestObservationRepositoryCursorPaginationFiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "query.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewObservationRepository(store)
	at := time.Unix(1_700_000_000, 0).UTC()
	for _, item := range []struct {
		id, platform, eventType, sourceKind, sourceID string
	}{
		{"evt-c", "bilibili", "dynamic", "account", "source-a"},
		{"evt-b", "bilibili", "dynamic", "account", "source-a"},
		{"evt-a", "twitter", "tweet", "account", "source-b"},
	} {
		if err := repository.InsertObservedEvent(ctx, ObservedEventRecord{
			ID: item.id, SchemaVersion: 1, Platform: item.platform, EventType: item.eventType,
			SourceKind: item.sourceKind, SourceExternalID: item.sourceID, ObservedAt: at,
			ContentFingerprint: strings.Repeat("a", 64), PublicSnapshotJSON: `{"text":"public"}`, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.InsertRouteObservation(ctx, RouteObservationRecord{ID: "route-c", EventID: "evt-c", RouteOrdinal: 0, DestinationKind: "qq_group", DestinationExternalID: "1", Outcome: "pass", ReasonCode: "legacy_pass", ObservedAt: at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repository.InsertDeliveryObservation(ctx, DeliveryObservationRecord{ID: "delivery-c", EventID: "evt-c", RouteObservationID: "route-c", ConnectorKind: "onebot", DestinationExternalID: "1", Status: "sent", ResultCode: "sent", ObservedAt: at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.NextCursor == nil || page.Events[0].ID != "evt-c" || page.Events[1].ID != "evt-b" {
		t.Fatalf("first page = %#v, cursor=%#v", page.Events, page.NextCursor)
	}
	if page.Events[0].RouteCount != 1 || page.Events[0].DeliveryCount != 1 || page.Events[0].FinalDeliveryStatus != "sent" {
		t.Fatalf("event aggregates = %#v", page.Events[0])
	}
	second, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].ID != "evt-a" || second.NextCursor != nil {
		t.Fatalf("second page = %#v, cursor=%#v", second.Events, second.NextCursor)
	}
	filtered, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: 50, Platform: "bilibili", EventType: "dynamic", SourceExternalID: "source-a"})
	if err != nil || len(filtered.Events) != 2 {
		t.Fatalf("filtered page = %#v, err=%v", filtered, err)
	}
	from := at.Add(-time.Second)
	to := at.Add(time.Second)
	timeFiltered, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: 50, From: &from, To: &to})
	if err != nil || len(timeFiltered.Events) != 3 {
		t.Fatalf("time filtered page = %#v, err=%v", timeFiltered, err)
	}
	if _, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: MaxObservationPageSize + 1}); !errors.Is(err, ErrObservationInvalidQuery) {
		t.Fatalf("invalid limit error = %v", err)
	}
	recent, err := repository.ObservationRecentCounts(ctx, at.Add(time.Hour))
	if err != nil || recent.Events24h != 3 || recent.Routes24h != 1 || recent.Deliveries24h != 1 {
		t.Fatalf("recent counts = %#v, err=%v", recent, err)
	}
}

func TestObservationRepositoryCursorSurvivesConcurrentInsert(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "cursor.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewObservationRepository(store)
	at := time.Unix(1_700_000_000, 0).UTC()
	insert := func(id string, observed time.Time) {
		t.Helper()
		if err := repository.InsertObservedEvent(ctx, ObservedEventRecord{ID: id, SchemaVersion: 1, Platform: "bilibili", SourceKind: "account", EventType: "dynamic", ObservedAt: observed, ContentFingerprint: strings.Repeat("b", 64), PublicSnapshotJSON: `{"text":"public"}`, CreatedAt: observed}); err != nil {
			t.Fatal(err)
		}
	}
	insert("evt-b", at)
	insert("evt-a", at)
	first, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: 1})
	if err != nil || len(first.Events) != 1 || first.Events[0].ID != "evt-b" || first.NextCursor == nil {
		t.Fatalf("first page = %#v, err=%v", first, err)
	}
	insert("evt-new", at.Add(time.Second))
	second, err := repository.ListObservedEvents(ctx, ObservationEventQuery{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Events) != 1 || second.Events[0].ID != "evt-a" {
		t.Fatalf("second page after insert = %#v, err=%v", second, err)
	}
}
