package platformdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

func phase5Store(t *testing.T) (*Store, *Phase5Repository) {
	t.Helper()
	store, err := Open(context.Background(), Config{Path: filepath.Join(t.TempDir(), "phase5.sqlite"), Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewPhase5Repository(store)
}

func phase5Decision(id, event string, action string) RouteDecisionRecord {
	at := time.Unix(1700000000, 0).UTC()
	return RouteDecisionRecord{ID: id, EventID: event, ObservedEventID: "obs-" + event, RouteObservationID: "route-" + id, SourceID: "source-1", TargetID: "target-1", SubscriptionID: "sub-1", ConfiguredMode: "enforce", EffectiveMode: "enforce", ProfileID: "profile-1", PolicyDigest: "policy-1", SuggestedAction: action, EffectiveAction: action, ReasonCode: "test", CreatedAt: at, DecidedAt: at}
}

func TestPhase5PersistDropIsAtomicAndReplayable(t *testing.T) {
	_, repo := phase5Store(t)
	ctx := context.Background()
	decision := phase5Decision("route-drop-1", "event-drop-1", "drop")
	snapshot := ReplayableEventRecord{ID: "replay-drop-1", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, EventType: "dynamic", SnapshotJSON: json.RawMessage(`{"schema_version":1,"public":true}`), CreatedAt: decision.CreatedAt, ExpiresAt: decision.CreatedAt.Add(90 * 24 * time.Hour)}
	if err := repo.PersistDrop(ctx, decision, snapshot); err != nil {
		t.Fatal(err)
	}
	gotDecision, err := repo.RouteDecision(ctx, decision.ID)
	if err != nil || gotDecision.EffectiveAction != "drop" {
		t.Fatalf("route decision = %#v, err=%v", gotDecision, err)
	}
	gotSnapshot, err := repo.ReplayableEvent(ctx, decision.ID)
	if err != nil || gotSnapshot.RouteDecisionID != decision.ID || !json.Valid(gotSnapshot.SnapshotJSON) {
		t.Fatalf("snapshot = %#v, err=%v", gotSnapshot, err)
	}
}

func TestPhase5DeliveryUnknownCannotBeRetried(t *testing.T) {
	_, repo := phase5Store(t)
	at := time.Unix(1700000000, 0).UTC()
	value := DeliveryRecord{ID: "delivery-unknown-1", EventID: "event-1", TargetID: "target-1", TargetType: "group", ExternalID: "123", Status: domain.DeliveryPlanned, InitiatedBy: "system", CreatedAt: at, UpdatedAt: at}
	if err := repo.CreateDelivery(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionDelivery(context.Background(), value.ID, domain.DeliverySending, "", "", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionDelivery(context.Background(), value.ID, domain.DeliveryUnknown, "transport_unknown", "", at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ManualRetry(context.Background(), value.ID, "admin", at.Add(3*time.Second)); err != ErrDeliveryRetryNotAllowed {
		t.Fatalf("unknown retry err = %v, want %v", err, ErrDeliveryRetryNotAllowed)
	}
}

func TestPhase5EmergencyFlagPersists(t *testing.T) {
	_, repo := phase5Store(t)
	if disabled, err := repo.EnforceEmergencyDisabled(context.Background()); err != nil || disabled {
		t.Fatalf("initial emergency = %v, err=%v", disabled, err)
	}
	if err := repo.SetEnforceEmergencyDisabled(context.Background(), true, time.Unix(1700000001, 0)); err != nil {
		t.Fatal(err)
	}
	if disabled, err := repo.EnforceEmergencyDisabled(context.Background()); err != nil || !disabled {
		t.Fatalf("stored emergency = %v, err=%v", disabled, err)
	}
}

func TestPhase5PersistDropRejectsMismatchedSnapshotIdentity(t *testing.T) {
	_, repo := phase5Store(t)
	decision := phase5Decision("route-identity-1", "event-identity-1", "drop")
	// The JSON envelope is public and intentionally contains the identity that
	// a replay process would restore after a crash. A mismatch must prevent the
	// DROP transaction from committing rather than being silently repaired.
	snapshotJSON := json.RawMessage(`{"schema_version":1,"route_decision_id":"route-other","event_id":"event-identity-1","target":{"target_id":"target-1"}}`)
	err := repo.PersistDrop(context.Background(), decision, ReplayableEventRecord{ID: "replay-identity-1", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, TargetID: decision.TargetID, SnapshotJSON: snapshotJSON, EventType: "dynamic", CreatedAt: decision.CreatedAt, ExpiresAt: decision.CreatedAt.Add(time.Hour)})
	if err != ErrApprovalInvalid {
		t.Fatalf("mismatched snapshot error = %v, want %v", err, ErrApprovalInvalid)
	}
	if _, err := repo.RouteDecision(context.Background(), decision.ID); err != ErrRouteDecisionNotFound {
		t.Fatalf("route decision after rejected drop = %v, want absent", err)
	}
}

func TestPhase5PersistDropRequiresStableDecisionIdentity(t *testing.T) {
	_, repo := phase5Store(t)
	decision := phase5Decision("", "event-identity-2", "drop")
	err := repo.PersistDrop(context.Background(), decision, ReplayableEventRecord{SchemaVersion: 1, EventID: decision.EventID, SnapshotJSON: json.RawMessage(`{"schema_version":1}`), EventType: "dynamic"})
	if err != ErrApprovalInvalid {
		t.Fatalf("empty decision identity error = %v, want %v", err, ErrApprovalInvalid)
	}
}

func TestPhase5SendingRowsAreAbandonedOnRestart(t *testing.T) {
	_, repo := phase5Store(t)
	at := time.Unix(1700000000, 0).UTC()
	value, err := repo.CreateDeliveryRecord(context.Background(), DeliveryRecord{ID: "delivery-restart-1", EventID: "event-restart-1", TargetID: "target-1", TargetType: "group", ExternalID: "123", Status: domain.DeliveryPlanned, InitiatedBy: "system", CreatedAt: at, UpdatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionDelivery(context.Background(), value.ID, domain.DeliverySending, "", "", at); err != nil {
		t.Fatal(err)
	}
	count, err := repo.MarkSendingAbandonedOnRestart(context.Background(), at.Add(time.Minute))
	if err != nil || count != 1 {
		t.Fatalf("abandoned count = %d, err=%v", count, err)
	}
	loaded, err := repo.Delivery(context.Background(), value.ID)
	if err != nil || loaded.Status != domain.DeliveryAbandonedRestart || loaded.CompletedAt == nil {
		t.Fatalf("abandoned delivery = %#v, err=%v", loaded, err)
	}
}
