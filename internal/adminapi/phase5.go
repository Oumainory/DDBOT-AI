package adminapi

// Phase 5 API surfaces the durable routing, delivery, feedback, replay and
// Enforce controls.  Reads are authenticated and cursor based; every state
// changing command uses the same Origin/CSRF/Idempotency boundary as the
// existing Phase 1–4 API.  The handlers never expose media file paths or
// secret material.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
)

const phase5CursorVersion = 1

type phase5Cursor struct {
	Version int    `json:"v"`
	At      int64  `json:"t"`
	ID      string `json:"id"`
}

func encodePhase5Cursor(at time.Time, id string) string {
	raw, _ := json.Marshal(phase5Cursor{Version: phase5CursorVersion, At: at.UTC().Unix(), ID: strings.TrimSpace(id)})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodePhase5Cursor(raw string) (*phase5Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	var value phase5Cursor
	if err := json.Unmarshal(decoded, &value); err != nil || value.Version != phase5CursorVersion || value.At < 0 || value.ID == "" {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	return &value, nil
}

func (s *Server) phase5Available(w http.ResponseWriter) bool {
	if s == nil || s.phase5Repository == nil {
		s.writeError(w, http.StatusServiceUnavailable, "phase5_unavailable", "Phase 5 storage is unavailable")
		return false
	}
	return true
}

func (s *Server) handlePhase5Subresource(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v2/") {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	for i := range parts {
		// API resource IDs are intentionally treated as opaque path segments;
		// URL decoding is required for IDs containing escaped characters.
		if decoded, err := url.PathUnescape(parts[i]); err == nil {
			parts[i] = decoded
		}
	}
	switch parts[0] {
	case "route-decisions":
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return true
			}
			s.RequireAuth(http.HandlerFunc(s.handleRouteDecisions)).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleRouteDecision(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 3 && parts[2] == "replay" {
			if r.Method != http.MethodPost {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return true
			}
			s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleReplay(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
	case "deliveries":
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return true
			}
			s.RequireAuth(http.HandlerFunc(s.handleDeliveries)).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleDelivery(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 3 && parts[2] == "retry" {
			if r.Method != http.MethodPost {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return true
			}
			s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleManualRetry(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
	case "feedback":
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				s.RequireAuth(http.HandlerFunc(s.handleFeedbackList)).ServeHTTP(w, r)
			case http.MethodPost:
				s.RequireMutation(http.HandlerFunc(s.handleFeedbackCreate)).ServeHTTP(w, r)
			default:
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			}
			return true
		}
		if len(parts) == 2 && (r.Method == http.MethodPatch || r.Method == http.MethodPut) {
			s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleFeedbackUpdate(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleFeedbackDetail(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
	case "media-cache":
		if len(parts) == 2 && parts[1] == "summary" && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(s.handleMediaCacheSummary)).ServeHTTP(w, r)
			return true
		}
	case "ai":
		if len(parts) == 3 && parts[1] == "enforce" {
			switch parts[2] {
			case "readiness":
				if r.Method != http.MethodGet {
					s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
					return true
				}
				s.RequireAuth(http.HandlerFunc(s.handlePhase5Readiness)).ServeHTTP(w, r)
				return true
			case "approve":
				if r.Method != http.MethodPost {
					s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
					return true
				}
				s.RequireMutation(http.HandlerFunc(s.handleEnforceApprove)).ServeHTTP(w, r)
				return true
			case "revoke":
				if r.Method != http.MethodPost {
					s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
					return true
				}
				s.RequireMutation(http.HandlerFunc(s.handleEnforceRevoke)).ServeHTTP(w, r)
				return true
			case "emergency-disable", "emergency-enable":
				if r.Method != http.MethodPost {
					s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
					return true
				}
				disabled := parts[2] == "emergency-disable"
				s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleEmergency(w, r, disabled) })).ServeHTTP(w, r)
				return true
			}
		}
	}
	return false
}

func (s *Server) handlePhase5Readiness(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) || s.aiRepository == nil {
		return
	}
	value, err := s.aiRepository.EnforceReadiness(r.Context())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "enforce_readiness_unavailable", "Enforce readiness is unavailable")
		return
	}
	emergency, emergencyErr := s.phase5Repository.EnforceEmergencyDisabled(r.Context())
	if emergencyErr == nil {
		valueMap := map[string]any{"readiness": value, "emergency_disabled": emergency}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: valueMap})
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
}

func (s *Server) handleRouteDecisions(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) {
		return
	}
	limit := parsePhase5Limit(r.URL.Query().Get("limit"))
	var before *time.Time
	var beforeID string
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor, err := decodePhase5Cursor(raw)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
			return
		}
		stamp := time.Unix(cursor.At, 0).UTC()
		before, beforeID = &stamp, cursor.ID
	}
	values, hasNext, err := s.phase5Repository.ListRouteDecisions(r.Context(), limit, before, beforeID)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "phase5_unavailable", "route decisions are unavailable")
		return
	}
	data := map[string]any{"items": values}
	if hasNext && len(values) > 0 {
		last := values[len(values)-1]
		data["next_cursor"] = encodePhase5Cursor(last.CreatedAt, last.ID)
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: data})
}

func (s *Server) handleRouteDecision(w http.ResponseWriter, r *http.Request, id string) {
	if !s.phase5Available(w) {
		return
	}
	value, err := s.phase5Repository.RouteDecision(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found", "route decision not found")
		return
	}
	data := map[string]any{"route_decision": value}
	if deliveries, deliveryErr := s.phase5Repository.ListDeliveriesForEvent(r.Context(), value.EventID); deliveryErr == nil {
		data["deliveries"] = deliveries
	}
	if feedback, feedbackErr := s.phase5Repository.FeedbackForRoute(r.Context(), value.ID); feedbackErr == nil {
		data["feedback"] = feedback
	}
	if replay, replayErr := s.phase5Repository.ReplayableEvent(r.Context(), value.ID); replayErr == nil {
		data["replay_available"] = replay.ExpiresAt.After(s.now().UTC())
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: data})
}

func (s *Server) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) {
		return
	}
	limit := parsePhase5Limit(r.URL.Query().Get("limit"))
	var before *time.Time
	var beforeID string
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor, err := decodePhase5Cursor(raw)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
			return
		}
		stamp := time.Unix(cursor.At, 0).UTC()
		before, beforeID = &stamp, cursor.ID
	}
	values, hasNext, err := s.phase5Repository.ListDeliveries(r.Context(), limit, before, beforeID)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "phase5_unavailable", "deliveries are unavailable")
		return
	}
	data := map[string]any{"items": values}
	if hasNext && len(values) > 0 {
		last := values[len(values)-1]
		data["next_cursor"] = encodePhase5Cursor(last.CreatedAt, last.ID)
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: data})
}

func (s *Server) handleDelivery(w http.ResponseWriter, r *http.Request, id string) {
	if !s.phase5Available(w) {
		return
	}
	value, err := s.phase5Repository.Delivery(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found", "delivery not found")
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
}

func (s *Server) handleFeedbackList(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) {
		return
	}
	limit := parsePhase5Limit(r.URL.Query().Get("limit"))
	var before *time.Time
	var beforeID string
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor, err := decodePhase5Cursor(raw)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
			return
		}
		stamp := time.Unix(cursor.At, 0).UTC()
		before, beforeID = &stamp, cursor.ID
	}
	values, hasNext, err := s.phase5Repository.ListFeedback(r.Context(), limit, before, beforeID)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "phase5_unavailable", "feedback is unavailable")
		return
	}
	data := map[string]any{"items": values}
	if hasNext && len(values) > 0 {
		last := values[len(values)-1]
		data["next_cursor"] = encodePhase5Cursor(last.CreatedAt, last.ID)
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: data})
}

func (s *Server) handleFeedbackDetail(w http.ResponseWriter, r *http.Request, id string) {
	if !s.phase5Available(w) {
		return
	}
	value, err := s.phase5Repository.Feedback(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found", "feedback not found")
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
}

type phase5FeedbackRequest struct {
	RouteDecisionID string `json:"route_decision_id"`
	AIDecisionID    string `json:"ai_decision_id"`
	FeedbackType    string `json:"feedback_type"`
	Notes           string `json:"notes"`
	Resolved        *bool  `json:"resolved"`
}

func (s *Server) handleFeedbackCreate(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) {
		return
	}
	var request phase5FeedbackRequest
	body, err := readJSONBody(r, &request)
	if err != nil || strings.TrimSpace(request.RouteDecisionID) == "" || strings.TrimSpace(request.FeedbackType) == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "feedback is invalid")
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	s.executeDomainCommand(w, r, "feedback_create", body, func() (int, apiEnvelope) {
		resolved := true
		if request.Resolved != nil {
			resolved = *request.Resolved
		}
		value := platformdb.FeedbackRecord{RouteDecisionID: request.RouteDecisionID, AIDecisionID: request.AIDecisionID, FeedbackType: request.FeedbackType, ReviewedBy: principal.AdminID, Notes: request.Notes, Resolved: resolved, CreatedAt: s.now(), UpdatedAt: s.now()}
		if err := s.phase5Repository.CreateFeedback(r.Context(), value); err != nil {
			return s.phase5ErrorEnvelope(err)
		}
		feedback, err := s.phase5Repository.FeedbackForRoute(r.Context(), request.RouteDecisionID)
		if err != nil || len(feedback) == 0 {
			return http.StatusServiceUnavailable, apiEnvelope{Error: &apiError{Code: "phase5_unavailable", Message: "feedback is unavailable"}}
		}
		s.appendAIAudit(r.Context(), r, "feedback.create", "feedback", feedback[len(feedback)-1].ID, "success", map[string]any{"feedback_type": request.FeedbackType})
		return http.StatusCreated, apiEnvelope{Data: feedback[len(feedback)-1]}
	})
}

func (s *Server) handleFeedbackUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.phase5Available(w) {
		return
	}
	var request phase5FeedbackRequest
	body, err := readJSONBody(r, &request)
	if err != nil || strings.TrimSpace(request.FeedbackType) == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "feedback is invalid")
		return
	}
	s.executeDomainCommand(w, r, "feedback_update", body, func() (int, apiEnvelope) {
		resolved := true
		if request.Resolved != nil {
			resolved = *request.Resolved
		}
		value, err := s.phase5Repository.UpdateFeedback(r.Context(), id, request.FeedbackType, request.Notes, resolved, s.now())
		if err != nil {
			return s.phase5ErrorEnvelope(err)
		}
		s.appendAIAudit(r.Context(), r, "feedback.update", "feedback", id, "success", map[string]any{"feedback_type": request.FeedbackType, "resolved": resolved})
		return http.StatusOK, apiEnvelope{Data: value}
	})
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request, routeDecisionID string) {
	if s.replayService == nil {
		s.writeError(w, http.StatusServiceUnavailable, "replay_unavailable", "replay is unavailable")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	body, err := readJSONBody(r, &map[string]any{})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "replay request is invalid")
		return
	}
	fingerprint, err := idempotency.NewFingerprint(r.Method, r.URL.RequestURI(), body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "request fingerprint is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		s.writeError(w, http.StatusBadRequest, "idempotency_required", "idempotency key required")
		return
	}
	delivery, replayErr := s.replayService.Replay(r.Context(), routeDecisionID, principal.AdminID, key, fingerprint)
	if replayErr != nil {
		status, code, message := s.phase5Error(replayErr)
		s.writeError(w, status, code, message)
		return
	}
	s.appendAIAudit(r.Context(), r, "replay.request", "route_decision", routeDecisionID, "success", map[string]any{"delivery_id": delivery.ID})
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: delivery})
}

func (s *Server) handleManualRetry(w http.ResponseWriter, r *http.Request, id string) {
	if !s.phase5Available(w) {
		return
	}
	body, err := readJSONBody(r, &map[string]any{})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "retry request is invalid")
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	s.executeDomainCommand(w, r, "manual_retry", body, func() (int, apiEnvelope) {
		value, err := s.phase5Repository.ManualRetry(r.Context(), id, principal.AdminID, s.now())
		if err != nil {
			return s.phase5ErrorEnvelope(err)
		}
		s.appendAIAudit(r.Context(), r, "delivery.manual_retry", "delivery", value.ID, "success", map[string]any{"source_delivery_id": id})
		return http.StatusAccepted, apiEnvelope{Data: value}
	})
}

type phase5ApprovalRequest struct {
	ClassifierReleaseID string                   `json:"classifier_release_id"`
	ProfileID           string                   `json:"profile_id,omitempty"`
	Threshold           *float64                 `json:"threshold,omitempty"`
	DefaultAction       policy.Action            `json:"default_action,omitempty"`
	CategoryActions     map[string]policy.Action `json:"category_actions,omitempty"`
	TagActions          map[string]policy.Action `json:"tag_actions,omitempty"`
	PolicyDigest        string                   `json:"policy_digest"`
	ProfileDigest       string                   `json:"profile_digest"`
	ReadinessEvidence   json.RawMessage          `json:"readiness_evidence"`
	Reason              string                   `json:"reason"`
}

// enforcePolicyFromOverride reconstructs the policy semantics that are
// actually persisted by the AI policy endpoint.  Approval and policy writes
// must hash the same effective values; otherwise a digest could accidentally
// approve a different profile after a sparse override is applied.
func (s *Server) enforcePolicyFromOverride(ctx context.Context, value platformdb.AIPolicyOverrideRecord) (policy.EffectivePolicy, policy.Profile, error) {
	profile := policy.OfficialGameProfile()
	if strings.TrimSpace(value.ProfileID) != "" {
		if s.aiRepository == nil {
			return policy.EffectivePolicy{}, policy.Profile{}, platformdb.ErrAIUnavailable
		}
		loaded, err := s.aiRepository.Profile(ctx, value.ProfileID)
		if err != nil {
			return policy.EffectivePolicy{}, policy.Profile{}, err
		}
		profile = loaded
	}
	if value.DefaultAction != "" && value.DefaultAction != policy.ActionInherit {
		profile.DefaultAction = value.DefaultAction
	}
	if profile.CategoryActions == nil {
		profile.CategoryActions = make(map[domain.Category]policy.Action)
	}
	for key, action := range value.CategoryActions {
		if action != policy.ActionInherit {
			profile.CategoryActions[domain.Category(key)] = action
		}
	}
	if profile.TagActions == nil {
		profile.TagActions = make(map[string]policy.Action)
	}
	for key, action := range value.TagActions {
		if action != policy.ActionInherit {
			profile.TagActions[key] = action
		}
	}
	if err := profile.Validate(); err != nil {
		return policy.EffectivePolicy{}, policy.Profile{}, err
	}
	threshold := 0.90
	if value.Threshold != nil {
		threshold = *value.Threshold
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0.90 || threshold > 1 {
		return policy.EffectivePolicy{}, policy.Profile{}, errors.New("policy: enforce threshold is outside the safe range")
	}
	return policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: threshold, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}, profile, nil
}

// validateEnforcePolicy is the Phase 5 activation gate.  It is intentionally
// evaluated inside the idempotent command boundary immediately before the
// policy mutation.  A policy row can therefore never switch to ENFORCE unless
// the current release, current readiness evidence, immutable profile digest
// and durable approval all agree.
func (s *Server) validateEnforcePolicy(ctx context.Context, value platformdb.AIPolicyOverrideRecord) error {
	if s.phase5Repository == nil || s.aiRepository == nil {
		return policy.ErrEnforceNotAvailable
	}
	readiness, err := s.aiRepository.EnforceReadiness(ctx)
	if err != nil {
		return err
	}
	if !readiness.Ready {
		return platformdb.ErrEnforceNotReady
	}
	if disabled, err := s.phase5Repository.EnforceEmergencyDisabled(ctx); err != nil {
		return err
	} else if disabled {
		return platformdb.ErrEmergencyDisabled
	}
	release, err := s.aiRepository.ActiveRelease(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(readiness.CurrentReleaseID) == "" || readiness.CurrentReleaseID != release.ID {
		return platformdb.ErrEnforceNotReady
	}
	effective, profile, err := s.enforcePolicyFromOverride(ctx, value)
	if err != nil {
		return err
	}
	_, err = s.phase5Repository.ValidEnforceApproval(ctx, release.ID, policy.Digest(effective), policy.ProfileDigest(profile), s.now())
	return err
}

func (s *Server) handleEnforceApprove(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) || s.aiRepository == nil {
		return
	}
	var request phase5ApprovalRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "approval request is invalid")
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	s.executeDomainCommand(w, r, "enforce_approve", body, func() (int, apiEnvelope) {
		readiness, readinessErr := s.aiRepository.EnforceReadiness(r.Context())
		if readinessErr != nil {
			return http.StatusServiceUnavailable, apiEnvelope{Error: &apiError{Code: "enforce_readiness_unavailable", Message: "Enforce readiness is unavailable"}}
		}
		if !readiness.Ready {
			return http.StatusUnprocessableEntity, apiEnvelope{Error: &apiError{Code: "enforce_not_ready", Message: "Enforce readiness gates have not passed"}, Data: readiness}
		}
		// EnforceReadiness returns the release that all metrics were measured
		// against. Use that identity directly; a second lookup is only a
		// presentation/validation step and must never silently pair a newer
		// release with older readiness evidence.
		if strings.TrimSpace(readiness.CurrentReleaseID) == "" {
			return http.StatusUnprocessableEntity, apiEnvelope{Error: &apiError{Code: "enforce_not_ready", Message: "an active classifier release is required"}}
		}
		releaseID := readiness.CurrentReleaseID
		if request.ClassifierReleaseID != "" && request.ClassifierReleaseID != releaseID {
			return http.StatusConflict, apiEnvelope{Error: &apiError{Code: "enforce_not_ready", Message: "classifier release is not current"}}
		}
		valueForApproval := platformdb.AIPolicyOverrideRecord{ProfileID: request.ProfileID, Threshold: request.Threshold, DefaultAction: request.DefaultAction, CategoryActions: make(map[domain.Category]policy.Action), TagActions: request.TagActions}
		for key, action := range request.CategoryActions {
			valueForApproval.CategoryActions[domain.Category(key)] = action
		}
		effective, profile, policyErr := s.enforcePolicyFromOverride(r.Context(), valueForApproval)
		if policyErr != nil {
			return s.phase5ErrorEnvelope(policyErr)
		}
		policyDigest, profileDigest := policy.Digest(effective), policy.ProfileDigest(profile)
		if request.PolicyDigest != "" && request.PolicyDigest != policyDigest || request.ProfileDigest != "" && request.ProfileDigest != profileDigest {
			return http.StatusConflict, apiEnvelope{Error: &apiError{Code: "enforce_not_ready", Message: "policy or profile digest is stale"}}
		}
		// Never persist caller-supplied readiness evidence: it can be stale or
		// describe a different release. The approval records the canonical
		// server-side snapshot that just passed the current-release gate.
		evidence, _ := json.Marshal(readiness)
		id := "approval_" + strconv.FormatInt(s.now().UTC().UnixNano(), 10)
		value := platformdb.EnforceApprovalRecord{ID: id, ClassifierReleaseID: releaseID, PolicyDigest: policyDigest, ProfileDigest: profileDigest, ReadinessEvidenceJSON: evidence, ApprovedAt: s.now(), ApprovedBy: principal.AdminID, Reason: request.Reason, CreatedAt: s.now()}
		if err := s.phase5Repository.SaveEnforceApproval(r.Context(), value); err != nil {
			return s.phase5ErrorEnvelope(err)
		}
		value, _ = s.phase5Repository.ValidEnforceApproval(r.Context(), releaseID, policyDigest, profileDigest, s.now())
		s.appendAIAudit(r.Context(), r, "enforce.approve", "enforce_approval", id, "success", map[string]any{"classifier_release_id": releaseID, "policy_digest": policyDigest, "profile_digest": profileDigest})
		return http.StatusCreated, apiEnvelope{Data: value}
	})
}

func (s *Server) handleEnforceRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "revoke request is invalid")
		return
	}
	s.executeDomainCommand(w, r, "enforce_revoke", body, func() (int, apiEnvelope) {
		if err := s.phase5Repository.RevokeEnforceApprovals(r.Context(), request.Reason, s.now()); err != nil {
			return s.phase5ErrorEnvelope(err)
		}
		s.appendAIAudit(r.Context(), r, "enforce.revoke", "enforce_approval", "", "success", map[string]any{"reason": request.Reason})
		return http.StatusOK, apiEnvelope{Data: map[string]any{"revoked": true}}
	})
}

func (s *Server) handleEmergency(w http.ResponseWriter, r *http.Request, disabled bool) {
	if !s.phase5Available(w) {
		return
	}
	body, err := readJSONBody(r, &map[string]any{})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "emergency request is invalid")
		return
	}
	command := "enforce_emergency_enable"
	if disabled {
		command = "enforce_emergency_disable"
	}
	s.executeDomainCommand(w, r, command, body, func() (int, apiEnvelope) {
		if err := s.phase5Repository.SetEnforceEmergencyDisabled(r.Context(), disabled, s.now()); err != nil {
			return s.phase5ErrorEnvelope(err)
		}
		if s.enforceRuntime != nil {
			s.enforceRuntime.SetEmergencyDisabled(disabled)
		}
		action := "enforce.emergency.enable"
		if disabled {
			action = "enforce.emergency.disable"
		}
		s.appendAIAudit(r.Context(), r, action, "enforce", "emergency", "success", map[string]any{"disabled": disabled})
		return http.StatusOK, apiEnvelope{Data: map[string]any{"disabled": disabled}}
	})
}

func (s *Server) handleMediaCacheSummary(w http.ResponseWriter, r *http.Request) {
	if !s.phase5Available(w) {
		return
	}
	value, err := s.phase5Repository.MediaCacheSummary(r.Context(), s.now())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "phase5_unavailable", "media cache is unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
}

func parsePhase5Limit(raw string) int {
	if raw == "" {
		return 50
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 200 {
		return 50
	}
	return value
}

func (s *Server) phase5ErrorEnvelope(err error) (int, apiEnvelope) {
	status, code, message := s.phase5Error(err)
	return status, apiEnvelope{Error: &apiError{Code: code, Message: message}, RequestID: requestID()}
}

func (s *Server) phase5Error(err error) (int, string, string) {
	switch {
	case errors.Is(err, platformdb.ErrDeliveryRetryNotAllowed):
		return http.StatusConflict, "delivery_retry_not_allowed", "delivery is not eligible for manual retry"
	case errors.Is(err, platformdb.ErrReplayExpired):
		return http.StatusConflict, "replay_expired", "replay snapshot has expired"
	case errors.Is(err, platformdb.ErrApprovalInvalid), errors.Is(err, platformdb.ErrApprovalNotFound), errors.Is(err, platformdb.ErrEnforceNotReady):
		return http.StatusUnprocessableEntity, "enforce_not_ready", "Enforce approval is not valid"
	case errors.Is(err, platformdb.ErrDeliveryNotFound), errors.Is(err, platformdb.ErrRouteDecisionNotFound), errors.Is(err, platformdb.ErrFeedbackNotFound), errors.Is(err, platformdb.ErrReplayNotFound):
		return http.StatusNotFound, "not_found", "Phase 5 resource not found"
	case errors.Is(err, idempotency.ErrConflict):
		return http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with another request"
	case errors.Is(err, idempotency.ErrInProgress):
		return http.StatusConflict, "idempotency_in_progress", "request is already in progress"
	case errors.Is(err, platformdb.ErrEmergencyDisabled):
		return http.StatusConflict, "enforce_emergency_disabled", "Enforce is emergency-disabled"
	case errors.Is(err, platformdb.ErrPhase5Unavailable):
		return http.StatusServiceUnavailable, "phase5_unavailable", "Phase 5 storage is unavailable"
	case errors.Is(err, platformdb.ErrIdempotencyUnavailable):
		return http.StatusServiceUnavailable, "phase5_unavailable", "Phase 5 storage is unavailable"
	default:
		return http.StatusBadRequest, "invalid_argument", "Phase 5 request is invalid"
	}
}
