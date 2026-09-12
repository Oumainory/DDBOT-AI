package adminapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cnxysoft/DDBOT-WSa/internal/observation"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

const observationCursorVersion = 1

type observationCursorPayload struct {
	Version    int    `json:"v"`
	ObservedAt int64  `json:"t"`
	ID         string `json:"id"`
}

func encodeObservationCursor(cursor *platformdb.ObservationCursor) string {
	if cursor == nil || strings.TrimSpace(cursor.ID) == "" {
		return ""
	}
	payload, err := json.Marshal(observationCursorPayload{
		Version: observationCursorVersion, ObservedAt: cursor.ObservedAt, ID: cursor.ID,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeObservationCursor(raw string) (*platformdb.ObservationCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > 512 {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	var payload observationCursorPayload
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload.Version != observationCursorVersion || payload.ObservedAt < 0 || strings.TrimSpace(payload.ID) == "" {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	return &platformdb.ObservationCursor{ObservedAt: payload.ObservedAt, ID: payload.ID}, nil
}

func parseObservationTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	value = value.UTC()
	return &value, nil
}

func parseObservationQuery(values url.Values) (platformdb.ObservationEventQuery, error) {
	query := platformdb.ObservationEventQuery{
		Platform:         strings.TrimSpace(values.Get("platform")),
		EventType:        strings.TrimSpace(values.Get("event_type")),
		SourceKind:       strings.TrimSpace(values.Get("source_kind")),
		SourceExternalID: strings.TrimSpace(values.Get("source_external_id")),
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return platformdb.ObservationEventQuery{}, platformdb.ErrObservationInvalidQuery
		}
		query.Limit = limit
	}
	cursor, err := decodeObservationCursor(values.Get("cursor"))
	if err != nil {
		return platformdb.ObservationEventQuery{}, err
	}
	query.Cursor = cursor
	if query.From, err = parseObservationTime(values.Get("from")); err != nil {
		return platformdb.ObservationEventQuery{}, err
	}
	if query.To, err = parseObservationTime(values.Get("to")); err != nil {
		return platformdb.ObservationEventQuery{}, err
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return platformdb.ObservationEventQuery{}, platformdb.ErrObservationInvalidQuery
	}
	if query.Limit == 0 {
		query.Limit = platformdb.DefaultObservationPageSize
	}
	if query.Limit < 1 || query.Limit > platformdb.MaxObservationPageSize {
		return platformdb.ObservationEventQuery{}, platformdb.ErrObservationInvalidQuery
	}
	return query, nil
}

type observationPublicSnapshot struct {
	Platform         string   `json:"platform,omitempty"`
	SourceKind       string   `json:"source_kind,omitempty"`
	SourceExternalID string   `json:"source_external_id,omitempty"`
	UpstreamEventID  string   `json:"upstream_event_id,omitempty"`
	EventType        string   `json:"event_type,omitempty"`
	SourceEventAt    *int64   `json:"source_event_at,omitempty"`
	Text             string   `json:"text,omitempty"`
	URL              string   `json:"url,omitempty"`
	MediaURLs        []string `json:"media_urls,omitempty"`
	AuthorID         string   `json:"author_id,omitempty"`
	AuthorName       string   `json:"author_name,omitempty"`
}

type observationPublicSummary struct {
	Text       string   `json:"text,omitempty"`
	URL        string   `json:"url,omitempty"`
	MediaURLs  []string `json:"media_urls,omitempty"`
	AuthorID   string   `json:"author_id,omitempty"`
	AuthorName string   `json:"author_name,omitempty"`
}

type observationEventDTO struct {
	ID                  string                   `json:"id"`
	Platform            string                   `json:"platform"`
	SourceKind          string                   `json:"source_kind"`
	SourceExternalID    string                   `json:"source_external_id"`
	UpstreamEventID     string                   `json:"upstream_event_id"`
	EventType           string                   `json:"event_type"`
	ObservedAt          string                   `json:"observed_at"`
	SourceEventAt       *string                  `json:"source_event_at,omitempty"`
	ContentFingerprint  string                   `json:"content_fingerprint"`
	PublicSummary       observationPublicSummary `json:"public_summary"`
	RouteCount          int                      `json:"route_count,omitempty"`
	DeliveryCount       int                      `json:"delivery_count,omitempty"`
	FinalDeliveryStatus string                   `json:"final_delivery_status,omitempty"`
}

type observationEventDetailDTO struct {
	ID                 string                    `json:"id"`
	Platform           string                    `json:"platform"`
	SourceKind         string                    `json:"source_kind"`
	SourceExternalID   string                    `json:"source_external_id"`
	UpstreamEventID    string                    `json:"upstream_event_id"`
	EventType          string                    `json:"event_type"`
	ObservedAt         string                    `json:"observed_at"`
	SourceEventAt      *string                   `json:"source_event_at,omitempty"`
	ContentFingerprint string                    `json:"content_fingerprint"`
	PublicSnapshot     observationPublicSnapshot `json:"public_snapshot"`
}

type routeObservationDTO struct {
	ID                    string `json:"id"`
	EventID               string `json:"event_id"`
	RouteOrdinal          int    `json:"route_ordinal"`
	DestinationKind       string `json:"destination_kind"`
	DestinationExternalID string `json:"destination_external_id"`
	Outcome               string `json:"outcome"`
	ReasonCode            string `json:"reason_code"`
	ObservedAt            string `json:"observed_at"`
}

type deliveryObservationDTO struct {
	ID                    string `json:"id"`
	EventID               string `json:"event_id"`
	RouteObservationID    string `json:"route_observation_id"`
	ConnectorKind         string `json:"connector_kind"`
	DestinationExternalID string `json:"destination_external_id"`
	Status                string `json:"status"`
	ResultCode            string `json:"result_code"`
	ObservedAt            string `json:"observed_at"`
}

type observationPageDTO struct {
	Items      []observationEventDTO `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type observationDetailDTO struct {
	Event      observationEventDetailDTO `json:"event"`
	Routes     []routeObservationDTO     `json:"routes"`
	Deliveries []deliveryObservationDTO  `json:"deliveries"`
	AI         *observationAIShadowDTO   `json:"ai_shadow,omitempty"`
}

type observationAIShadowDTO struct {
	Decisions        []aiDecisionDTO                    `json:"decisions"`
	RouteEvaluations []platformdb.RouteEvaluationRecord `json:"route_evaluations"`
}

type observationRuntimeDTO struct {
	Status             string `json:"status"`
	QueueCapacity      int    `json:"queue_capacity"`
	QueueDepth         int    `json:"queue_depth"`
	EventsAccepted     uint64 `json:"events_accepted"`
	RoutesAccepted     uint64 `json:"routes_accepted"`
	DeliveriesAccepted uint64 `json:"deliveries_accepted"`
	QueueDropped       uint64 `json:"queue_dropped"`
	PersistenceErrors  uint64 `json:"persistence_errors"`
	WorkerPanics       uint64 `json:"worker_panics"`
	PruneErrors        uint64 `json:"prune_errors"`
}

type observationSummaryDTO struct {
	Window struct {
		RetentionDays int `json:"retention_days"`
	} `json:"window"`
	Runtime observationRuntimeDTO `json:"runtime"`
	Recent  struct {
		Events24h     int `json:"events_24h"`
		Routes24h     int `json:"routes_24h"`
		Deliveries24h int `json:"deliveries_24h"`
	} `json:"recent"`
}

func parseSnapshot(raw string) observationPublicSnapshot {
	if len(raw) > 512*1024 {
		return observationPublicSnapshot{}
	}
	var snapshot observationPublicSnapshot
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&snapshot); err != nil {
		return observationPublicSnapshot{}
	}
	snapshot.Text = truncateRunes(snapshot.Text, 12000)
	snapshot.MediaURLs = allowlistedMediaURLs(snapshot.MediaURLs, 32)
	return snapshot
}

func publicSummary(snapshot observationPublicSnapshot) observationPublicSummary {
	return observationPublicSummary{
		Text: truncateRunes(snapshot.Text, 280), URL: truncateRunes(snapshot.URL, 2048),
		MediaURLs: allowlistedMediaURLs(snapshot.MediaURLs, 4),
		AuthorID:  truncateRunes(snapshot.AuthorID, 256), AuthorName: truncateRunes(snapshot.AuthorName, 256),
	}
}

func allowlistedMediaURLs(values []string, max int) []string {
	if len(values) > max {
		values = values[:max]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = truncateRunes(strings.TrimSpace(value), 2048)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func truncateRunes(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

func sourceTime(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func eventDTO(record platformdb.ObservedEventRecord, routeCount, deliveryCount int, finalDeliveryStatus string) observationEventDTO {
	snapshot := parseSnapshot(record.PublicSnapshotJSON)
	return observationEventDTO{
		ID: record.ID, Platform: record.Platform, SourceKind: record.SourceKind,
		SourceExternalID: record.SourceExternalID, UpstreamEventID: record.UpstreamEventID,
		EventType: record.EventType, ObservedAt: record.ObservedAt.UTC().Format(time.RFC3339Nano),
		SourceEventAt: sourceTime(record.SourceEventAt), ContentFingerprint: record.ContentFingerprint,
		PublicSummary: publicSummary(snapshot), RouteCount: routeCount, DeliveryCount: deliveryCount,
		FinalDeliveryStatus: finalDeliveryStatus,
	}
}

func eventDetailDTO(record platformdb.ObservedEventRecord) observationEventDetailDTO {
	return observationEventDetailDTO{
		ID: record.ID, Platform: record.Platform, SourceKind: record.SourceKind,
		SourceExternalID: record.SourceExternalID, UpstreamEventID: record.UpstreamEventID,
		EventType: record.EventType, ObservedAt: record.ObservedAt.UTC().Format(time.RFC3339Nano),
		SourceEventAt: sourceTime(record.SourceEventAt), ContentFingerprint: record.ContentFingerprint,
		PublicSnapshot: parseSnapshot(record.PublicSnapshotJSON),
	}
}

func routeDTO(record platformdb.RouteObservationRecord) routeObservationDTO {
	return routeObservationDTO{ID: record.ID, EventID: record.EventID, RouteOrdinal: record.RouteOrdinal,
		DestinationKind: record.DestinationKind, DestinationExternalID: record.DestinationExternalID,
		Outcome: record.Outcome, ReasonCode: record.ReasonCode, ObservedAt: record.ObservedAt.UTC().Format(time.RFC3339Nano)}
}

func deliveryDTO(record platformdb.DeliveryObservationRecord) deliveryObservationDTO {
	return deliveryObservationDTO{ID: record.ID, EventID: record.EventID, RouteObservationID: record.RouteObservationID,
		ConnectorKind: record.ConnectorKind, DestinationExternalID: record.DestinationExternalID,
		Status: record.Status, ResultCode: record.ResultCode, ObservedAt: record.ObservedAt.UTC().Format(time.RFC3339Nano)}
}

func (s *Server) observationAvailable() bool {
	return s != nil && s.observationRepository != nil
}

func (s *Server) requireObservation(w http.ResponseWriter) bool {
	if !s.observationAvailable() {
		s.writeError(w, http.StatusServiceUnavailable, "observation_unavailable", "observation service is unavailable")
		return false
	}
	return true
}

func (s *Server) handleObservationEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.requireObservation(w) {
		return
	}
	query, err := parseObservationQuery(r.URL.Query())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "observation query is invalid")
		return
	}
	page, err := s.observationRepository.ListObservedEvents(r.Context(), query)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	items := make([]observationEventDTO, 0, len(page.Events))
	for _, record := range page.Events {
		items = append(items, eventDTO(record.ObservedEventRecord, record.RouteCount, record.DeliveryCount, record.FinalDeliveryStatus))
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: observationPageDTO{Items: items, NextCursor: encodeObservationCursor(page.NextCursor)}})
}

func (s *Server) handleObservationEventDetail(w http.ResponseWriter, r *http.Request, eventID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.requireObservation(w) {
		return
	}
	event, err := s.observationRepository.GetObservedEvent(r.Context(), eventID)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	routes, err := s.observationRepository.ListRouteObservationsForEvent(r.Context(), eventID)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	deliveries, err := s.observationRepository.ListDeliveryObservationsForEvent(r.Context(), eventID)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	response := observationDetailDTO{Event: eventDetailDTO(event), Routes: make([]routeObservationDTO, 0, len(routes)), Deliveries: make([]deliveryObservationDTO, 0, len(deliveries))}
	for _, route := range routes {
		response.Routes = append(response.Routes, routeDTO(route))
	}
	for _, delivery := range deliveries {
		response.Deliveries = append(response.Deliveries, deliveryDTO(delivery))
	}
	if s.aiRepository != nil {
		if decisions, decisionErr := s.aiRepository.DecisionsForEvent(r.Context(), eventID); decisionErr == nil {
			aiView := &observationAIShadowDTO{Decisions: make([]aiDecisionDTO, 0, len(decisions)), RouteEvaluations: make([]platformdb.RouteEvaluationRecord, 0)}
			seen := make(map[string]struct{})
			for _, decision := range decisions {
				aiView.Decisions = append(aiView.Decisions, aiDecisionDTOFrom(decision))
				if values, routeErr := s.aiRepository.RouteEvaluationsForDecision(r.Context(), decision.ID); routeErr == nil {
					for _, value := range values {
						seen[value.ID] = struct{}{}
						aiView.RouteEvaluations = append(aiView.RouteEvaluations, value)
					}
				}
			}
			for _, route := range routes {
				if value, routeErr := s.aiRepository.RouteEvaluation(r.Context(), route.ID); routeErr == nil {
					if _, exists := seen[value.ID]; !exists {
						aiView.RouteEvaluations = append(aiView.RouteEvaluations, value)
					}
				}
			}
			response.AI = aiView
		}
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: response})
}

func (s *Server) handleObservationRoutes(w http.ResponseWriter, r *http.Request, eventID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.requireObservation(w) {
		return
	}
	if _, err := s.observationRepository.GetObservedEvent(r.Context(), eventID); err != nil {
		s.writeObservationError(w, err)
		return
	}
	routes, err := s.observationRepository.ListRouteObservationsForEvent(r.Context(), eventID)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	items := make([]routeObservationDTO, 0, len(routes))
	for _, route := range routes {
		items = append(items, routeDTO(route))
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) handleObservationDeliveries(w http.ResponseWriter, r *http.Request, eventID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.requireObservation(w) {
		return
	}
	if _, err := s.observationRepository.GetObservedEvent(r.Context(), eventID); err != nil {
		s.writeObservationError(w, err)
		return
	}
	deliveries, err := s.observationRepository.ListDeliveryObservationsForEvent(r.Context(), eventID)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	items := make([]deliveryObservationDTO, 0, len(deliveries))
	for _, delivery := range deliveries {
		items = append(items, deliveryDTO(delivery))
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) handleObservationSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.requireObservation(w) {
		return
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	recent, err := s.observationRepository.ObservationRecentCounts(r.Context(), now)
	if err != nil {
		s.writeObservationError(w, err)
		return
	}
	stats := observation.Stats{}
	status := "unknown"
	if s.observationRecorder != nil {
		stats = s.observationRecorder.Stats()
		status = "available"
		if stats.QueueDropped > 0 || stats.PersistenceErrors > 0 || stats.WorkerPanics > 0 || stats.PruneErrors > 0 {
			status = "degraded"
		}
	}
	result := observationSummaryDTO{}
	result.Window.RetentionDays = int(observation.DefaultRetention / (24 * time.Hour))
	result.Runtime = observationRuntimeDTO{Status: status, QueueCapacity: stats.QueueCapacity, QueueDepth: stats.QueueDepth,
		EventsAccepted: stats.EventsAccepted, RoutesAccepted: stats.RoutesAccepted, DeliveriesAccepted: stats.DeliveriesAccepted,
		QueueDropped: stats.QueueDropped, PersistenceErrors: stats.PersistenceErrors, WorkerPanics: stats.WorkerPanics, PruneErrors: stats.PruneErrors}
	result.Recent.Events24h = recent.Events24h
	result.Recent.Routes24h = recent.Routes24h
	result.Recent.Deliveries24h = recent.Deliveries24h
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: result})
}

func (s *Server) writeObservationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, platformdb.ErrObservationNotFound):
		s.writeError(w, http.StatusNotFound, "observation_not_found", "observation not found")
	case errors.Is(err, platformdb.ErrObservationInvalidQuery):
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "observation query is invalid")
	default:
		s.writeError(w, http.StatusServiceUnavailable, "observation_unavailable", "observation service is unavailable")
	}
}

func observationPathParts(path string) []string {
	trimmed := strings.TrimPrefix(path, "/api/v2/observations/events/")
	if trimmed == path || trimmed == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(trimmed, "/"), "/")
	for index, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" {
			return nil
		}
		parts[index] = decoded
	}
	return parts
}
