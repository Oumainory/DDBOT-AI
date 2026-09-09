package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cnxysoft/DDBOT-WSa/internal/deliverysnapshot"
)

func heldPayload() deliverysnapshot.Payload {
	return deliverysnapshot.Payload{
		SchemaVersion: 1,
		DeliveryID:    "delivery-1",
		MigrationID:   "migration-1",
		EventID:       "event-1",
		RouteID:       "route-1",
		RouteSnapshot: json.RawMessage(`{"connector_id":"onebot-new","target_id":"group-1"}`),
		LogicalTarget: deliverysnapshot.LogicalTarget{TargetID: "target-1", TargetType: "group", ExternalID: "123"},
		Message: deliverysnapshot.MessageSnapshot{
			SchemaVersion: 1,
			Segments:      []deliverysnapshot.Segment{{Type: "text", Data: map[string]any{"text": "hello"}}},
		},
	}
}

func TestHeldDeliverySurvivesSerializationAndReleasesOnlyForItsMigration(t *testing.T) {
	held, err := NewHeldDelivery(heldPayload())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the original payload and holder before recovery. The assertions
	// below must be satisfied by persisted bytes only, not shared references.
	held = HeldDelivery{}
	var afterRestart HeldDelivery
	if err := json.Unmarshal(encoded, &afterRestart); err != nil {
		t.Fatal(err)
	}

	released, err := afterRestart.Release("migration-1", json.RawMessage(`{"connector_id":"onebot-new","target_id":"group-1"}`))
	if err != nil || released.DeliveryID != "delivery-1" ||
		released.LogicalTarget.ExternalID != "123" ||
		string(released.Message.Segments[0].Data["text"].(string)) != "hello" ||
		!bytes.Equal(released.RouteSnapshot, json.RawMessage(`{"connector_id":"onebot-new","target_id":"group-1"}`)) {
		t.Fatalf("released = %#v, err = %v", released, err)
	}
	if _, err := afterRestart.Release("migration-other", nil); !errors.Is(err, ErrMigrationMismatch) {
		t.Fatalf("wrong migration error = %v", err)
	}
}
