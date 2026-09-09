package domain

import (
	"testing"
	"time"
)

func TestNormalizedEventRequiresVersionedReplayPayload(t *testing.T) {
	event := NormalizedEvent{
		ID:                "event_1",
		Platform:          PlatformBilibili,
		SourceID:          "source_1",
		ExternalID:        "dynamic_1",
		EventType:         EventDynamic,
		ReplayPayload:     []byte(`{"card":{"id":"dynamic_1"}}`),
		NormalizerVersion: "bilibili-v1",
		CreatedAt:         time.Unix(1700000000, 0),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("NormalizedEvent.Validate() error = %v", err)
	}

	event.ReplayPayload = []byte("not-json")
	if err := event.Validate(); err == nil {
		t.Fatal("NormalizedEvent.Validate() accepted invalid replay payload")
	}
}

func TestSemanticResultRejectsInvalidConfidenceAndLongReason(t *testing.T) {
	result := SemanticResult{
		Category:   CategoryAnnouncement,
		Importance: ImportanceHigh,
		Confidence: 0.98,
		Reason:     "正常",
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("SemanticResult.Validate() error = %v", err)
	}

	result.Confidence = 1.01
	if err := result.Validate(); err == nil {
		t.Fatal("SemanticResult.Validate() accepted confidence above 1")
	}
}

func TestTargetIdentityIncludesTargetType(t *testing.T) {
	group := Target{ID: "group-1", ConnectorID: "onebot", TargetType: TargetGroup, ExternalID: "123"}
	channel := Target{ID: "channel-1", ConnectorID: "onebot", TargetType: TargetChannel, ExternalID: "123"}
	if group.IdentityKey() == channel.IdentityKey() {
		t.Fatal("group and channel with the same external id must not collide")
	}
	if err := group.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownDeliveryIsNeverAutoRetried(t *testing.T) {
	if DeliveryUnknown.AutoRetryAllowed() {
		t.Fatal("unknown delivery must not be auto-retried")
	}
	if DeliveryMigrationHeld.AutoRetryAllowed() {
		t.Fatal("migration_held delivery must be released by Migration Coordinator")
	}
	if !DeliveryNotSent.AutoRetryAllowed() {
		t.Fatal("not_sent delivery should remain safely retryable")
	}
}
