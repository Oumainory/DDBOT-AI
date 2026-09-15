package platformdb

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
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

func TestPhase5PersistDropIfAllowedLinearizesEmergencyAndApproval(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	release := aiTestRelease()
	release.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	approval := EnforceApprovalRecord{ID: "approval-linearization-1", ClassifierReleaseID: release.ID, PolicyDigest: "policy-linear-1", ProfileDigest: "profile-linear-1", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin-1", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	makeDecision := func(id string) (RouteDecisionRecord, ReplayableEventRecord) {
		decision := phase5Decision(id, "event-"+id, "drop")
		decision.ClassifierReleaseID = release.ID
		decision.EnforceApprovalID = approval.ID
		decision.PolicyDigest = approval.PolicyDigest
		snapshot := ReplayableEventRecord{ID: "replay-" + id, SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, SnapshotJSON: json.RawMessage(`{"schema_version":1,"route_decision_id":"` + decision.ID + `","event_id":"` + decision.EventID + `"}`), EventType: "dynamic", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
		return decision, snapshot
	}
	firstDecision, firstSnapshot := makeDecision("linear-first")
	if err := repo.PersistDropIfAllowed(ctx, firstDecision, firstSnapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at); err != nil {
		t.Fatalf("drop before disable = %v", err)
	}
	if err := repo.SetEnforceEmergencyDisabled(ctx, true, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	secondDecision, secondSnapshot := makeDecision("linear-after-disable")
	if err := repo.PersistDropIfAllowed(ctx, secondDecision, secondSnapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(2*time.Second)); !errors.Is(err, ErrEmergencyDisabled) {
		t.Fatalf("drop after durable disable = %v, want %v", err, ErrEmergencyDisabled)
	}
	if _, err := repo.RouteDecision(ctx, secondDecision.ID); !errors.Is(err, ErrRouteDecisionNotFound) {
		t.Fatalf("rejected drop decision = %v, want absent", err)
	}
	if err := repo.SetEnforceEmergencyDisabled(ctx, false, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeEnforceApprovals(ctx, "operator review", at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	revokedDecision, revokedSnapshot := makeDecision("linear-after-revoke")
	if err := repo.PersistDropIfAllowed(ctx, revokedDecision, revokedSnapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(5*time.Second)); !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("drop after approval revoke = %v, want %v", err, ErrApprovalInvalid)
	}
}

func TestPhase5PersistDropRejectsReleaseActivationRace(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	oldRelease := aiTestRelease()
	oldRelease.ID = "release-linear-old"
	oldRelease.Fingerprint = "fingerprint-linear-old"
	oldRelease.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, oldRelease); err != nil {
		t.Fatal(err)
	}
	approval := EnforceApprovalRecord{ID: "approval-release-race", ClassifierReleaseID: oldRelease.ID, PolicyDigest: "policy-release-race", ProfileDigest: "profile-release-race", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin-1", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	newRelease := aiTestRelease()
	newRelease.ID = "release-linear-new"
	newRelease.Fingerprint = "fingerprint-linear-new"
	newRelease.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, newRelease); err != nil {
		t.Fatal(err)
	}
	decision := phase5Decision("route-release-race", "event-release-race", "drop")
	decision.ClassifierReleaseID = oldRelease.ID
	decision.EnforceApprovalID = approval.ID
	decision.PolicyDigest = approval.PolicyDigest
	snapshot := ReplayableEventRecord{ID: "replay-release-race", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, SnapshotJSON: json.RawMessage(`{"schema_version":1,"route_decision_id":"route-release-race","event_id":"event-release-race"}`), EventType: "dynamic", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := repo.PersistDropIfAllowed(ctx, decision, snapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(time.Second)); !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("drop after release activation = %v, want %v", err, ErrApprovalInvalid)
	}
	if _, err := repo.RouteDecision(ctx, decision.ID); !errors.Is(err, ErrRouteDecisionNotFound) {
		t.Fatalf("route decision after release race = %v, want absent", err)
	}
}

func TestPhase5ApprovalRejectsReleaseChangedAfterReadiness(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	oldRelease := aiTestRelease()
	oldRelease.ID = "release-approval-old"
	oldRelease.Fingerprint = "fingerprint-approval-old"
	oldRelease.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, oldRelease); err != nil {
		t.Fatal(err)
	}
	// Simulate readiness having been computed for the old release. The active
	// release changes before approval persistence; the repository transaction
	// must reject the stale evidence rather than creating an approval for a
	// release that is no longer active.
	newRelease := aiTestRelease()
	newRelease.ID = "release-approval-new"
	newRelease.Fingerprint = "fingerprint-approval-new"
	newRelease.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, newRelease); err != nil {
		t.Fatal(err)
	}
	err := repo.SaveEnforceApproval(ctx, EnforceApprovalRecord{ID: "approval-stale-release", ClassifierReleaseID: oldRelease.ID, PolicyDigest: "policy", ProfileDigest: "profile", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin", CreatedAt: at})
	if !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("stale release approval error = %v, want %v", err, ErrApprovalInvalid)
	}
	if _, err := repo.ValidEnforceApproval(ctx, oldRelease.ID, "policy", "profile", at.Add(time.Second)); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("stale approval lookup = %v, want %v", err, ErrApprovalNotFound)
	}
}

func TestPhase5PolicyMutationRevokesInFlightApproval(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	release := aiTestRelease()
	release.ID = "release-policy-mutation"
	release.Fingerprint = "fingerprint-policy-mutation"
	release.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	approval := EnforceApprovalRecord{ID: "approval-policy-mutation", ClassifierReleaseID: release.ID, PolicyDigest: "policy-before", ProfileDigest: "profile-before", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin-1", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	aiRepo := NewAIRepository(store)
	if err := aiRepo.SavePolicy(ctx, AIPolicyOverrideRecord{ID: "policy-row", ScopeType: "global", ScopeID: "", Mode: "enforce", DefaultAction: "pass", CreatedAt: at}, at); err != nil {
		t.Fatal(err)
	}
	if err := aiRepo.SavePolicy(ctx, AIPolicyOverrideRecord{ID: "policy-row", ScopeType: "global", ScopeID: "", Mode: "enforce", DefaultAction: "drop", CreatedAt: at}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	decision := phase5Decision("route-policy-mutation", "event-policy-mutation", "drop")
	decision.ClassifierReleaseID = release.ID
	decision.EnforceApprovalID = approval.ID
	decision.PolicyDigest = approval.PolicyDigest
	snapshot := ReplayableEventRecord{ID: "replay-policy-mutation", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, SnapshotJSON: json.RawMessage(`{"schema_version":1,"route_decision_id":"route-policy-mutation","event_id":"event-policy-mutation"}`), EventType: "dynamic", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := repo.PersistDropIfAllowed(ctx, decision, snapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(2*time.Second)); !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("drop after policy mutation = %v, want %v", err, ErrApprovalInvalid)
	}
}

func TestPhase5PolicyCreationRevokesInFlightApproval(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	release := aiTestRelease()
	release.ID = "release-policy-creation"
	release.Fingerprint = "fingerprint-policy-creation"
	release.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	approval := EnforceApprovalRecord{ID: "approval-policy-creation", ClassifierReleaseID: release.ID, PolicyDigest: "policy-before-create", ProfileDigest: "profile-before-create", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin-1", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	// Adding the first sparse policy row changes the effective overlay from
	// inheritance/defaults. It must revoke the approval in the same write
	// transaction, even before a subsequent edit is made.
	if err := NewAIRepository(store).SavePolicy(ctx, AIPolicyOverrideRecord{ID: "policy-created", ScopeType: "global", ScopeID: "", Mode: policy.ModeEnforce, DefaultAction: policy.ActionPass, CreatedAt: at}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	decision := phase5Decision("route-policy-creation", "event-policy-creation", "drop")
	decision.ClassifierReleaseID = release.ID
	decision.EnforceApprovalID = approval.ID
	decision.PolicyDigest = approval.PolicyDigest
	snapshot := ReplayableEventRecord{ID: "replay-policy-creation", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, SnapshotJSON: json.RawMessage(`{"schema_version":1,"route_decision_id":"route-policy-creation","event_id":"event-policy-creation"}`), EventType: "dynamic", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := repo.PersistDropIfAllowed(ctx, decision, snapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(2*time.Second)); !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("drop after policy creation = %v, want %v", err, ErrApprovalInvalid)
	}
}

func TestPhase5ProfileMutationRevokesInFlightApproval(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	release := aiTestRelease()
	release.ID = "release-profile-mutation"
	release.Fingerprint = "fingerprint-profile-mutation"
	release.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	approval := EnforceApprovalRecord{ID: "approval-profile-mutation", ClassifierReleaseID: release.ID, PolicyDigest: "policy-profile-before", ProfileDigest: "profile-before", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin-1", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	aiRepo := NewAIRepository(store)
	profile := policy.Profile{ID: "profile-mutation", Name: "Profile", DefaultAction: policy.ActionPass}
	if err := aiRepo.SaveProfile(ctx, profile, at, at); err != nil {
		t.Fatal(err)
	}
	profile.DefaultAction = policy.ActionDrop
	if err := aiRepo.SaveProfile(ctx, profile, at, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	decision := phase5Decision("route-profile-mutation", "event-profile-mutation", "drop")
	decision.ClassifierReleaseID = release.ID
	decision.EnforceApprovalID = approval.ID
	decision.PolicyDigest = approval.PolicyDigest
	snapshot := ReplayableEventRecord{ID: "replay-profile-mutation", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, SnapshotJSON: json.RawMessage(`{"schema_version":1,"route_decision_id":"route-profile-mutation","event_id":"event-profile-mutation"}`), EventType: "dynamic", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := repo.PersistDropIfAllowed(ctx, decision, snapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(2*time.Second)); !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("drop after profile mutation = %v, want %v", err, ErrApprovalInvalid)
	}
}

func TestPhase5ProfileDeletionRevokesInFlightApproval(t *testing.T) {
	store, repo := phase5Store(t)
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	release := aiTestRelease()
	release.ID = "release-profile-deletion"
	release.Fingerprint = "fingerprint-profile-deletion"
	release.Active = true
	if err := NewAIRepository(store).SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	approval := EnforceApprovalRecord{ID: "approval-profile-deletion", ClassifierReleaseID: release.ID, PolicyDigest: "policy-profile-deletion", ProfileDigest: "profile-profile-deletion", ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin-1", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	aiRepo := NewAIRepository(store)
	if err := aiRepo.SaveProfile(ctx, policy.Profile{ID: "profile-delete", Name: "Delete me", DefaultAction: policy.ActionPass}, at, at); err != nil {
		t.Fatal(err)
	}
	if err := aiRepo.DeleteProfile(ctx, "profile-delete"); err != nil {
		t.Fatal(err)
	}
	decision := phase5Decision("route-profile-deletion", "event-profile-deletion", "drop")
	decision.ClassifierReleaseID = release.ID
	decision.EnforceApprovalID = approval.ID
	decision.PolicyDigest = approval.PolicyDigest
	snapshot := ReplayableEventRecord{ID: "replay-profile-deletion", SchemaVersion: 1, RouteDecisionID: decision.ID, EventID: decision.EventID, SourceID: decision.SourceID, TargetID: decision.TargetID, SnapshotJSON: json.RawMessage(`{"schema_version":1,"route_decision_id":"route-profile-deletion","event_id":"event-profile-deletion"}`), EventType: "dynamic", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := repo.PersistDropIfAllowed(ctx, decision, snapshot, approval.ID, approval.PolicyDigest, approval.ProfileDigest, at.Add(time.Second)); !errors.Is(err, ErrApprovalInvalid) {
		t.Fatalf("drop after profile deletion = %v, want %v", err, ErrApprovalInvalid)
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
