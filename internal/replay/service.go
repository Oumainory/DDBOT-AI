package replay

// Service owns the manual replay boundary. It consumes only the durable
// public snapshot and a freshly resolved target; it never imports a source
// adapter or classifier, so a replay cannot accidentally refetch or re-run AI.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

var (
	ErrReplayNotAllowed    = errors.New("replay: route is not a durable drop")
	ErrTargetUnavailable   = errors.New("replay: target unavailable")
	ErrRendererUnavailable = errors.New("replay: renderer unavailable")
	ErrSenderUnavailable   = errors.New("replay: sender unavailable")
)

type TargetResolver func(context.Context, TargetIdentity) (TargetIdentity, error)
type Renderer func(context.Context, Snapshot) ([]byte, error)
type Sender func(context.Context, TargetIdentity, []byte) (SendResult, error)

// SnapshotSender is an optional richer send boundary. It receives the same
// durable snapshot that was rendered, allowing the owner to append media
// elements selected by the cache-first replay policy without giving replay a
// source adapter, renderer pointer or provider dependency.
type SnapshotSender func(context.Context, TargetIdentity, Snapshot, []byte) (SendResult, error)

type SendResult struct {
	Status          domain.DeliveryStatus
	ResultCode      string
	RemoteMessageID string
}

// replayOutcome is the only value stored in the idempotency cache. Keeping a
// typed outcome (instead of caching an arbitrary error string) makes repeated
// failures deterministic as well as repeated successes, while retaining the
// durable Delivery record for an unknown remote result.
type replayOutcome struct {
	Delivery  *platformdb.DeliveryRecord `json:"delivery,omitempty"`
	ErrorCode string                     `json:"error_code,omitempty"`
}

type Config struct {
	Repository    *platformdb.Phase5Repository
	Idempotency   idempotency.Store
	ResolveTarget TargetResolver
	Render        Renderer
	Send          Sender
	SendSnapshot  SnapshotSender
	Now           func() time.Time
}

type Service struct {
	repository    *platformdb.Phase5Repository
	idempotency   idempotency.Store
	resolveTarget TargetResolver
	render        Renderer
	send          Sender
	sendSnapshot  SnapshotSender
	now           func() time.Time
}

func NewService(config Config) *Service {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Idempotency == nil {
		// Replay is an externally visible side-effect boundary. Do not silently
		// replace a missing durable backend with an in-process store that would
		// forget claims/results after restart; callers must wire SQLite explicitly.
		config.Idempotency = platformdb.NewIdempotencyStore(nil)
	}
	return &Service{repository: config.Repository, idempotency: config.Idempotency, resolveTarget: config.ResolveTarget, render: config.Render, send: config.Send, sendSnapshot: config.SendSnapshot, now: config.Now}
}

// Replay sends exactly one new delivery from a durable snapshot. The original
// DROP RouteDecision remains immutable. A matching idempotency key returns the
// previously serialized delivery, including an unknown result; unknown is
// never retried implicitly.
func (s *Service) Replay(ctx context.Context, routeDecisionID, principal, key string, fingerprint idempotency.Fingerprint) (platformdb.DeliveryRecord, error) {
	if s == nil || s.repository == nil {
		return platformdb.DeliveryRecord{}, platformdb.ErrPhase5Unavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now().UTC()
	if strings.TrimSpace(fingerprint.Method) == "" {
		var err error
		fingerprint, err = idempotency.NewFingerprint(http.MethodPost, "/api/v2/route-decisions/"+strings.TrimSpace(routeDecisionID)+"/replay", []byte("{}"))
		if err != nil {
			return platformdb.DeliveryRecord{}, err
		}
	}
	if s.idempotency != nil {
		record, outcome, err := s.idempotency.BeginCommand(principal, key, "manual_replay", fingerprint, now)
		if err != nil {
			return platformdb.DeliveryRecord{}, err
		}
		if outcome == idempotency.OutcomeReplay {
			var cached replayOutcome
			if err := json.Unmarshal(record.Body, &cached); err == nil && (cached.Delivery != nil || cached.ErrorCode != "") {
				if cached.Delivery != nil {
					if cached.ErrorCode != "" {
						return *cached.Delivery, replayErrorFromCode(cached.ErrorCode)
					}
					return *cached.Delivery, nil
				}
				return platformdb.DeliveryRecord{}, replayErrorFromCode(cached.ErrorCode)
			}
			// Backward-compatible read for records written by the initial
			// implementation before replayOutcome was introduced.
			var delivery platformdb.DeliveryRecord
			if err := json.Unmarshal(record.Body, &delivery); err != nil {
				return platformdb.DeliveryRecord{}, err
			}
			return delivery, nil
		}
	}
	completeError := func(err error) (platformdb.DeliveryRecord, error) {
		s.completeOutcome(principal, key, fingerprint, http.StatusConflict, replayOutcome{ErrorCode: replayErrorCode(err)})
		return platformdb.DeliveryRecord{}, err
	}
	route, err := s.repository.RouteDecision(ctx, routeDecisionID)
	if err != nil {
		return completeError(err)
	}
	if route.EffectiveAction != "drop" {
		return completeError(ErrReplayNotAllowed)
	}
	record, err := s.repository.ReplayableEvent(ctx, routeDecisionID)
	if err != nil {
		return completeError(err)
	}
	if !record.ExpiresAt.After(now) {
		return completeError(platformdb.ErrReplayExpired)
	}
	snapshot, err := Unmarshal(record.SnapshotJSON)
	if err != nil {
		return completeError(err)
	}
	if err := snapshot.Validate(now); err != nil {
		return completeError(err)
	}
	target := snapshot.Target
	if s.resolveTarget != nil {
		target, err = s.resolveTarget(ctx, target)
		if err != nil {
			return completeError(ErrTargetUnavailable)
		}
	}
	if strings.TrimSpace(target.TargetID) == "" || strings.TrimSpace(target.TargetType) == "" || strings.TrimSpace(target.ExternalID) == "" || strings.TrimSpace(target.ConnectorID) == "" {
		return completeError(ErrTargetUnavailable)
	}
	if s.render == nil {
		return completeError(ErrRendererUnavailable)
	}
	message, err := s.render(ctx, snapshot)
	if err != nil {
		return completeError(ErrRendererUnavailable)
	}
	if len(strings.TrimSpace(string(message))) == 0 {
		return completeError(platformdb.ErrDeliveryRetryNotAllowed)
	}
	delivery := platformdb.DeliveryRecord{RouteDecisionID: "", EventID: snapshot.EventID, TargetID: target.TargetID, ConnectorID: target.ConnectorID, TargetType: target.TargetType, ExternalID: target.ExternalID, Status: domain.DeliveryPlanned, ReplayOfRouteDecisionID: routeDecisionID, InitiatedBy: principal, MessageSnapshotJSON: string(message), CreatedAt: now, UpdatedAt: now}
	var createErr error
	delivery, createErr = s.repository.CreateDeliveryRecord(ctx, delivery)
	if createErr != nil {
		return completeError(createErr)
	}
	if err := s.repository.TransitionDelivery(ctx, delivery.ID, domain.DeliverySending, "", "", now); err != nil {
		return completeError(err)
	}
	if s.send == nil && s.sendSnapshot == nil {
		_ = s.repository.TransitionDelivery(ctx, delivery.ID, domain.DeliveryUnknown, "sender_unavailable", "", s.now())
		delivery, _ = s.repository.Delivery(ctx, delivery.ID)
		return s.completeOutcomeAndReturn(principal, key, fingerprint, delivery, ErrSenderUnavailable)
	}
	var result SendResult
	var sendErr error
	if s.sendSnapshot != nil {
		result, sendErr = s.sendSnapshot(ctx, target, snapshot, message)
	} else {
		result, sendErr = s.send(ctx, target, message)
	}
	if sendErr != nil {
		result.Status = domain.DeliveryUnknown
		if result.ResultCode == "" {
			result.ResultCode = "transport_unknown"
		}
	}
	if result.Status == "" {
		result.Status = domain.DeliveryUnknown
	}
	if !replayTerminalStatus(result.Status) {
		result.Status = domain.DeliveryUnknown
		if result.ResultCode == "" {
			result.ResultCode = "invalid_sender_status"
		}
	}
	if err := s.repository.TransitionDelivery(ctx, delivery.ID, result.Status, result.ResultCode, result.RemoteMessageID, s.now()); err != nil {
		_ = s.repository.TransitionDelivery(ctx, delivery.ID, domain.DeliveryUnknown, "delivery_transition_failed", "", s.now())
		if recovered, getErr := s.repository.Delivery(ctx, delivery.ID); getErr == nil {
			delivery = recovered
		}
		return s.completeOutcomeAndReturn(principal, key, fingerprint, delivery, err)
	}
	delivery, _ = s.repository.Delivery(ctx, delivery.ID)
	if sendErr != nil {
		return s.completeOutcomeAndReturn(principal, key, fingerprint, delivery, sendErr)
	}
	return s.completeOutcomeAndReturn(principal, key, fingerprint, delivery, nil)
}

func (s *Service) completeOutcomeAndReturn(principal, key string, fingerprint idempotency.Fingerprint, delivery platformdb.DeliveryRecord, resultErr error) (platformdb.DeliveryRecord, error) {
	status := http.StatusOK
	if resultErr != nil && delivery.ID == "" {
		status = http.StatusConflict
	}
	s.completeOutcome(principal, key, fingerprint, status, replayOutcome{Delivery: &delivery, ErrorCode: replayErrorCode(resultErr)})
	return delivery, resultErr
}

func (s *Service) completeOutcome(principal, key string, fingerprint idempotency.Fingerprint, status int, outcome replayOutcome) {
	if s == nil || s.idempotency == nil {
		return
	}
	body, err := json.Marshal(outcome)
	if err != nil {
		return
	}
	_, _ = s.idempotency.CompleteCommand(principal, key, "manual_replay", fingerprint, status, nil, body, s.now())
}

func replayTerminalStatus(status domain.DeliveryStatus) bool {
	switch status {
	case domain.DeliverySent, domain.DeliveryPartial, domain.DeliveryNotSent, domain.DeliveryUnknown, domain.DeliveryRejected, domain.DeliveryExpired, domain.DeliveryAbandonedRestart, domain.DeliverySkippedEmpty, domain.DeliveryQueued:
		return true
	default:
		return false
	}
}

func replayErrorCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, platformdb.ErrRouteDecisionNotFound):
		return "route_not_found"
	case errors.Is(err, platformdb.ErrReplayNotFound):
		return "replay_not_found"
	case errors.Is(err, platformdb.ErrReplayExpired):
		return "replay_expired"
	case errors.Is(err, ErrReplayNotAllowed):
		return "replay_not_allowed"
	case errors.Is(err, ErrTargetUnavailable):
		return "target_unavailable"
	case errors.Is(err, ErrRendererUnavailable):
		return "renderer_unavailable"
	case errors.Is(err, ErrSenderUnavailable):
		return "sender_unavailable"
	case errors.Is(err, platformdb.ErrDeliveryRetryNotAllowed):
		return "delivery_retry_not_allowed"
	case errors.Is(err, platformdb.ErrPhase5Unavailable):
		return "phase5_unavailable"
	case errors.Is(err, platformdb.ErrIdempotencyUnavailable):
		return "phase5_unavailable"
	case errors.Is(err, ErrInvalidSnapshot), errors.Is(err, ErrSnapshotExpired):
		return "invalid_snapshot"
	default:
		return "replay_unavailable"
	}
}

func replayErrorFromCode(code string) error {
	switch code {
	case "route_not_found":
		return platformdb.ErrRouteDecisionNotFound
	case "replay_not_found":
		return platformdb.ErrReplayNotFound
	case "replay_expired":
		return platformdb.ErrReplayExpired
	case "replay_not_allowed":
		return ErrReplayNotAllowed
	case "target_unavailable":
		return ErrTargetUnavailable
	case "renderer_unavailable":
		return ErrRendererUnavailable
	case "sender_unavailable":
		return ErrSenderUnavailable
	case "delivery_retry_not_allowed":
		return platformdb.ErrDeliveryRetryNotAllowed
	case "phase5_unavailable":
		return platformdb.ErrPhase5Unavailable
	case "invalid_snapshot":
		return ErrInvalidSnapshot
	default:
		return platformdb.ErrPhase5Unavailable
	}
}
