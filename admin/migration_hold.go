package admin

// This file is the narrow bootstrap bridge between the Legacy QQ sender and
// the Phase 3 migration coordinator. It does not become a delivery worker: it
// only turns a message that is about to cross the Messenger boundary into a
// durable snapshot when the currently committing migration explicitly owns
// that Legacy target.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Oumainory/DDBOT-AI/adapter"
	"github.com/Oumainory/DDBOT-AI/internal/deliverysnapshot"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/migration"
	"github.com/Oumainory/DDBOT-AI/internal/observation"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	ddbotlsp "github.com/Oumainory/DDBOT-AI/lsp"
	"github.com/Oumainory/DDBOT-AI/lsp/mmsg"
)

type legacyMigrationHolder struct {
	coordinator *migration.Coordinator
	repository  *platformdb.MigrationRepository
	domain      *platformdb.DomainRepository
	now         func() time.Time
}

func (h legacyMigrationHolder) current(ctx context.Context, groupCode int64, trace observation.RouteTrace) (platformdb.ConnectorMigration, domain.Target, bool, error) {
	if h.coordinator == nil || h.repository == nil || h.domain == nil {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, nil
	}
	m, err := h.repository.ActiveMigration(ctx)
	if errors.Is(err, platformdb.ErrMigrationNotFound) {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, nil
	}
	if err != nil {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, err
	}
	if m.State != platformdb.MigrationCommitting {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, nil
	}
	target, err := h.domain.TargetByConnectorExternal(ctx, m.OldConnectorID, domain.TargetGroup, strconv.FormatInt(groupCode, 10))
	if errors.Is(err, platformdb.ErrTargetNotFound) {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, nil
	}
	if err != nil {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, err
	}
	mappings, err := h.repository.Mappings(ctx, m.MigrationID)
	if err != nil {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, err
	}
	affected := false
	for _, mapping := range mappings {
		if mapping.OldTargetID == target.ID {
			affected = true
			break
		}
	}
	if !affected {
		// A target row may exist without an active Legacy subscription. It is
		// not part of this migration's hold set and follows normal delivery.
		return platformdb.ConnectorMigration{}, domain.Target{}, false, nil
	}
	// A durable snapshot cannot invent an event or route identity. Once the
	// target is known to be affected, fail closed instead of sending a message
	// that the coordinator could not later correlate. Unaffected targets above
	// deliberately retain their normal Legacy path even when observation is
	// unavailable.
	if !trace.Valid() {
		return platformdb.ConnectorMigration{}, domain.Target{}, false, errors.New("migration hold: route observation unavailable")
	}
	return m, target, true, nil
}

func (h legacyMigrationHolder) timestamp() time.Time {
	if h.now == nil {
		return time.Now().UTC()
	}
	return h.now().UTC()
}

func (h legacyMigrationHolder) hold(ctx context.Context, groupCode int64, trace observation.RouteTrace, message deliverysnapshot.MessageSnapshot) (bool, error) {
	m, target, affected, err := h.current(ctx, groupCode, trace)
	if err != nil || !affected {
		return false, err
	}
	deliveryID, err := domain.NewID()
	if err != nil {
		return false, err
	}
	now := h.timestamp()
	route, err := json.Marshal(map[string]any{
		"connector_id":            m.OldConnectorID,
		"target_id":               target.ID,
		"target_type":             string(target.TargetType),
		"external_id":             target.ExternalID,
		"event_id":                trace.EventObservationID(),
		"route_decision_id":       trace.RouteObservationID(),
		"destination_external_id": strconv.FormatInt(groupCode, 10),
	})
	if err != nil {
		return false, err
	}
	logical := deliverysnapshot.LogicalTarget{
		TargetID: target.ID, TargetType: string(target.TargetType),
		ExternalID: target.ExternalID, LegacyRouteKey: fmt.Sprintf("group:%d", groupCode),
	}
	payload := deliverysnapshot.Payload{
		SchemaVersion: 1,
		DeliveryID:    deliveryID,
		MigrationID:   m.MigrationID,
		EventID:       trace.EventObservationID(),
		RouteID:       trace.RouteObservationID(),
		RouteSnapshot: route,
		LogicalTarget: logical, LogicalTargetSnapshot: logical,
		Message:   message,
		CreatedAt: now.Unix(), HeldAt: now.Unix(),
		Metadata: map[string]string{"connector_kind": "onebot"},
	}
	return h.coordinator.TryHoldForTarget(ctx, payload)
}

func (h legacyMigrationHolder) holdMessage(ctx context.Context, message *adapter.SendingMessage, target mmsg.Target, trace observation.RouteTrace) (bool, error) {
	if target.TargetType() != mmsg.TargetGroup {
		return false, nil
	}
	snapshot, err := deliverysnapshot.FromSendingMessage(message)
	if err != nil {
		return false, err
	}
	return h.hold(ctx, target.TargetCode(), trace, snapshot)
}

func (h legacyMigrationHolder) holdForward(ctx context.Context, groupCode int64, nodes []map[string]interface{}, options *adapter.ForwardOptions, trace observation.RouteTrace) (bool, error) {
	snapshot, err := deliverysnapshot.FromForwardMessage(nodes, options)
	if err != nil {
		return false, err
	}
	return h.hold(ctx, groupCode, trace, snapshot)
}

func newLegacyMigrationHolder(coordinator *migration.Coordinator, repository *platformdb.MigrationRepository, domainRepository *platformdb.DomainRepository, now func() time.Time) legacyMigrationHolder {
	return legacyMigrationHolder{coordinator: coordinator, repository: repository, domain: domainRepository, now: now}
}

// installMigrationHoldHooks wires the callbacks only after all platform
// dependencies exist. The hooks are nil whenever the optional platform store
// is unavailable, preserving the historical Legacy sender path.
func installMigrationHoldHooks(holder legacyMigrationHolder) {
	if holder.coordinator == nil || holder.repository == nil || holder.domain == nil {
		return
	}
	ddbotlsp.Instance.SetMigrationHoldHook(holder.holdMessage)
	ddbotlsp.Instance.SetMigrationForwardHoldHook(holder.holdForward)
}
