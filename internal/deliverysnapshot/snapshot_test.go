package deliverysnapshot

import (
	"bytes"
	"testing"
)

func TestPayloadRoundTripIsProcessIndependent(t *testing.T) {
	original := Payload{
		SchemaVersion: CurrentSchemaVersion,
		DeliveryID:    "delivery_1",
		MigrationID:   "migration_1",
		EventID:       "event_1",
		RouteID:       "route_1",
		RouteSnapshot: []byte(`{"mode":"enforce","action":"pass"}`),
		LogicalTarget: LogicalTarget{
			TargetID:       "target_1",
			TargetType:     "group",
			ExternalID:     "123456",
			LegacyRouteKey: "onebot:group:123456",
		},
		Message: MessageSnapshot{
			SchemaVersion: CurrentSchemaVersion,
			Segments:      []Segment{{Type: "text", Data: map[string]any{"text": "hello"}}},
			Media:         []MediaReference{{LocalPath: "media/sha256/abc", RemoteURL: "https://example.test/a.png", Durable: true}},
			TextFallback:  "hello https://example.test/a.png",
			TemplateName:  "notify.group.bilibili.news",
			TemplateHash:  "sha256:template",
		},
	}

	encoded, err := original.Marshal()
	if err != nil {
		t.Fatalf("Payload.Marshal() error = %v", err)
	}
	decoded, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	reencoded, err := decoded.Marshal()
	if err != nil {
		t.Fatalf("decoded.Marshal() error = %v", err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("payload changed across restart serialization:\n%s\n%s", encoded, reencoded)
	}
}

func TestPayloadRequiresDurableMessageData(t *testing.T) {
	payload := Payload{
		SchemaVersion: CurrentSchemaVersion,
		DeliveryID:    "delivery_1",
		EventID:       "event_1",
		MigrationID:   "migration_1",
		RouteID:       "route_1",
		RouteSnapshot: []byte(`{"action":"pass"}`),
		LogicalTarget: LogicalTarget{TargetID: "target_1", TargetType: "group", ExternalID: "123"},
		Message:       MessageSnapshot{SchemaVersion: CurrentSchemaVersion},
	}
	if err := payload.Validate(); err != ErrMissingMessage {
		t.Fatalf("Payload.Validate() error = %v, want ErrMissingMessage", err)
	}
}

func TestPayloadRejectsNonDurableLocalMedia(t *testing.T) {
	payload := Payload{
		SchemaVersion: CurrentSchemaVersion,
		DeliveryID:    "delivery_1",
		EventID:       "event_1",
		MigrationID:   "migration_1",
		RouteID:       "route_1",
		RouteSnapshot: []byte(`{"action":"pass"}`),
		LogicalTarget: LogicalTarget{TargetID: "target_1", TargetType: "group", ExternalID: "123"},
		Message: MessageSnapshot{
			SchemaVersion: CurrentSchemaVersion,
			Segments:      []Segment{{Type: "text", Data: map[string]any{"text": "hello"}}},
			Media:         []MediaReference{{LocalPath: "temp/file.png"}},
		},
	}
	if err := payload.Validate(); err != ErrNonDurableMedia {
		t.Fatalf("Payload.Validate() error = %v, want ErrNonDurableMedia", err)
	}
}
