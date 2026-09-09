// Package migration contains the narrow Connector Migration hand-off
// contract. Held deliveries are serialized values, never references to a
// running Notify, Messenger, renderer, or template object.
package migration

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cnxysoft/DDBOT-WSa/internal/deliverysnapshot"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

var (
	ErrInvalidHeldDelivery = errors.New("migration: invalid held delivery")
	ErrMigrationMismatch   = errors.New("migration: migration id mismatch")
)

type HeldDelivery struct {
	DeliveryID  string                `json:"delivery_id"`
	MigrationID string                `json:"migration_id"`
	PayloadJSON []byte                `json:"payload_json"`
	Status      domain.DeliveryStatus `json:"status"`
}

func NewHeldDelivery(payload deliverysnapshot.Payload) (HeldDelivery, error) {
	data, err := payload.Marshal()
	if err != nil {
		return HeldDelivery{}, err
	}
	return HeldDelivery{
		DeliveryID:  payload.DeliveryID,
		MigrationID: payload.MigrationID,
		PayloadJSON: data,
		Status:      domain.DeliveryMigrationHeld,
	}, nil
}

// Decode reconstructs all delivery inputs from SQLite-shaped bytes. No
// process-local value is needed, which is the property required after a crash.
func (h HeldDelivery) Decode() (deliverysnapshot.Payload, error) {
	if strings.TrimSpace(h.DeliveryID) == "" || strings.TrimSpace(h.MigrationID) == "" ||
		h.Status != domain.DeliveryMigrationHeld || len(h.PayloadJSON) == 0 {
		return deliverysnapshot.Payload{}, ErrInvalidHeldDelivery
	}
	payload, err := deliverysnapshot.Unmarshal(h.PayloadJSON)
	if err != nil {
		return deliverysnapshot.Payload{}, err
	}
	if payload.DeliveryID != h.DeliveryID || payload.MigrationID != h.MigrationID {
		return deliverysnapshot.Payload{}, ErrInvalidHeldDelivery
	}
	return payload, nil
}

// Release validates ownership and returns the durable payload that the
// Migration Coordinator can hand to the normal delivery path. A coordinator
// may provide the route snapshot selected by the successful migration; nil
// preserves the snapshot already persisted for rollback/old-connector use.
func (h HeldDelivery) Release(migrationID string, routeSnapshot json.RawMessage) (deliverysnapshot.Payload, error) {
	if strings.TrimSpace(migrationID) == "" || migrationID != h.MigrationID {
		return deliverysnapshot.Payload{}, ErrMigrationMismatch
	}
	payload, err := h.Decode()
	if err != nil {
		return deliverysnapshot.Payload{}, err
	}
	if len(routeSnapshot) > 0 {
		if !json.Valid(routeSnapshot) {
			return deliverysnapshot.Payload{}, ErrInvalidHeldDelivery
		}
		payload.RouteSnapshot = append(json.RawMessage(nil), routeSnapshot...)
	}
	return payload, nil
}
