package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/discovery"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/internal/idempotency"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/lsp/subscription"
)

type sourceDTO struct {
	ID                string `json:"id"`
	Platform          string `json:"platform"`
	ExternalID        string `json:"external_id"`
	Handle            string `json:"handle,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	CanonicalURL      string `json:"canonical_url,omitempty"`
	Status            string `json:"status"`
	SubscriptionCount int    `json:"subscription_count"`
	Metadata          any    `json:"metadata,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type targetDTO struct {
	ID            string `json:"id"`
	ConnectorID   string `json:"connector_id"`
	ConnectorKind string `json:"connector_kind,omitempty"`
	TargetType    string `json:"target_type"`
	ExternalID    string `json:"external_id"`
	DisplayName   string `json:"display_name,omitempty"`
	Status        string `json:"status"`
	SourceCount   int    `json:"source_count"`
	Metadata      any    `json:"metadata,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type connectorDTO struct {
	ID                   string `json:"id"`
	Kind                 string `json:"kind"`
	Name                 string `json:"name"`
	Role                 string `json:"role"`
	Enabled              bool   `json:"enabled"`
	Status               string `json:"status"`
	Endpoint             string `json:"endpoint,omitempty"`
	CredentialConfigured bool   `json:"credential_configured"`
	CredentialMasked     string `json:"credential_masked,omitempty"`
	Config               any    `json:"config,omitempty"`
	Metadata             any    `json:"metadata,omitempty"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

type projectionDTO struct {
	ID               string `json:"id"`
	SourceID         string `json:"source_id"`
	TargetID         string `json:"target_id"`
	LegacyKey        string `json:"legacy_key"`
	Enabled          bool   `json:"enabled"`
	ProjectionStatus string `json:"projection_status"`
	ProjectedAt      string `json:"projected_at"`
}

type sourceCreateRequest struct {
	Platform    string `json:"platform"`
	ExternalID  string `json:"external_id"`
	Handle      string `json:"handle"`
	ProfileURL  string `json:"profile_url"`
	DisplayName string `json:"display_name"`
}

type sourcePatchRequest struct {
	DisplayName string         `json:"display_name"`
	Handle      string         `json:"handle"`
	Status      string         `json:"status"`
	Metadata    map[string]any `json:"metadata"`
}

type connectorPatchRequest struct {
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	Enabled      *bool          `json:"enabled"`
	Status       string         `json:"status"`
	Endpoint     string         `json:"endpoint"`
	CredentialID string         `json:"credential_id"`
	Config       map[string]any `json:"config"`
}

type targetCreateRequest struct {
	ConnectorID string         `json:"connector_id"`
	TargetType  string         `json:"target_type"`
	ExternalID  string         `json:"external_id"`
	DisplayName string         `json:"display_name"`
	Status      string         `json:"status"`
	Metadata    map[string]any `json:"metadata"`
}

type targetPatchRequest struct {
	DisplayName string         `json:"display_name"`
	Status      string         `json:"status"`
	Metadata    map[string]any `json:"metadata"`
}

type subscriptionCreateRequest struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Type     string `json:"type"`
}

type subscriptionPatchRequest struct {
	Type    string                    `json:"type"`
	Options subscription.OptionsPatch `json:"options"`
}

type discoveryRequest struct {
	Query string `json:"query"`
	Value string `json:"value"`
}

func timeDTO(seconds int64) string {
	if seconds == 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339Nano)
}

func decodeMetadata(raw string) any {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return map[string]any{}
	}
	return value
}

func sourceDTOFrom(source domain.Source, count int) sourceDTO {
	return sourceDTO{ID: source.ID, Platform: string(source.Platform), ExternalID: source.ExternalID, Handle: source.Handle,
		DisplayName: source.DisplayName, CanonicalURL: source.CanonicalURL, Status: source.Status,
		SubscriptionCount: count, Metadata: decodeMetadata(source.MetadataJSON), CreatedAt: timeDTO(source.CreatedAt), UpdatedAt: timeDTO(source.UpdatedAt)}
}

func targetDTOFrom(target domain.Target, connectorKind string, count int) targetDTO {
	return targetDTO{ID: target.ID, ConnectorID: target.ConnectorID, ConnectorKind: connectorKind,
		TargetType: string(target.TargetType), ExternalID: target.ExternalID, DisplayName: target.DisplayName,
		Status: target.Status, SourceCount: count, Metadata: decodeMetadata(target.MetadataJSON),
		CreatedAt: timeDTO(target.CreatedAt), UpdatedAt: timeDTO(target.UpdatedAt)}
}

func connectorDTOFrom(connector domain.Connector) connectorDTO {
	masked := ""
	if strings.TrimSpace(connector.CredentialID) != "" {
		masked = "configured"
	}
	return connectorDTO{ID: connector.ID, Kind: connector.Kind, Name: connector.Name, Role: connector.Role,
		Enabled: connector.Enabled, Status: connector.Status, Endpoint: connector.Endpoint,
		CredentialConfigured: masked != "", CredentialMasked: masked, Config: decodeSafeConfig(connector.ConfigJSON),
		Metadata: decodeMetadata(connector.MetadataJSON), CreatedAt: timeDTO(connector.CreatedAt), UpdatedAt: timeDTO(connector.UpdatedAt)}
}

// decodeSafeConfig never exposes values that look like credentials, even if a
// manually edited database contains an old unsafe config object. Connector
// credentials are represented only by CredentialID and resolved by Secret
// Store in later runtime phases.
func decodeSafeConfig(raw string) any {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return map[string]any{}
	}
	return redactConfig(value)
}

func redactConfig(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveConfigKey(key) {
				result[key] = "[redacted]"
				continue
			}
			result[key] = redactConfig(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactConfig(item)
		}
		return result
	default:
		return value
	}
}

func sensitiveConfigKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	for _, token := range []string{"token", "password", "secret", "api_key", "apikey", "private_key", "access_key", "client_secret"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func validateConfigObject(config map[string]any) error {
	for key, value := range config {
		if sensitiveConfigKey(key) {
			return errors.New("connector credentials must use credential_id")
		}
		switch nested := value.(type) {
		case map[string]any:
			if err := validateConfigObject(nested); err != nil {
				return err
			}
		case []any:
			for _, item := range nested {
				if object, ok := item.(map[string]any); ok {
					if err := validateConfigObject(object); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func projectionDTOFrom(value domain.SubscriptionProjection) projectionDTO {
	return projectionDTO{ID: value.ID, SourceID: value.SourceID, TargetID: value.TargetID, LegacyKey: value.LegacyKey,
		Enabled: value.Enabled, ProjectionStatus: value.ProjectionStatus, ProjectedAt: timeDTO(value.ProjectedAt)}
}

func (s *Server) domainAvailable(w http.ResponseWriter) bool {
	if s == nil || s.domainRepository == nil || s.legacySubscriptions == nil {
		s.writeError(w, http.StatusServiceUnavailable, "domain_unavailable", "domain service is unavailable")
		return false
	}
	return true
}

func readJSONBody(r *http.Request, target any) ([]byte, error) {
	if r == nil || r.Body == nil {
		return []byte("{}"), json.Unmarshal([]byte("{}"), target)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Server) executeDomainCommand(w http.ResponseWriter, r *http.Request, command string, body []byte, fn func() (int, apiEnvelope)) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || strings.TrimSpace(principal.AdminID) == "" {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		s.writeError(w, http.StatusBadRequest, "idempotency_required", "idempotency key required")
		return
	}
	fingerprint, err := idempotency.NewFingerprint(r.Method, r.URL.RequestURI(), body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "request fingerprint is invalid")
		return
	}
	if s.idempotency == nil {
		s.writeError(w, http.StatusServiceUnavailable, "domain_unavailable", "domain service is unavailable")
		return
	}
	record, outcome, err := s.idempotency.BeginCommand(principal.AdminID, key, command, fingerprint, s.now())
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrConflict):
			s.writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with another request")
		case errors.Is(err, idempotency.ErrInProgress):
			s.writeError(w, http.StatusConflict, "idempotency_in_progress", "request is already in progress")
		default:
			s.writeError(w, http.StatusBadRequest, "invalid_argument", "idempotency key is invalid")
		}
		return
	}
	if outcome == idempotency.OutcomeReplay {
		writeRawJSON(w, record.StatusCode, record.Body)
		return
	}
	status, response := fn()
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		s.writeError(w, http.StatusInternalServerError, "domain_unavailable", "domain response unavailable")
		return
	}
	if _, completeErr := s.idempotency.CompleteCommand(principal.AdminID, key, command, fingerprint, status, nil, raw, s.now()); completeErr != nil {
		s.writeError(w, http.StatusServiceUnavailable, "domain_unavailable", "domain response unavailable")
		return
	}
	writeRawJSON(w, status, raw)
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func domainError(err error) (int, string, string) {
	switch {
	case errors.Is(err, platformdb.ErrSourceNotFound), errors.Is(err, platformdb.ErrTargetNotFound), errors.Is(err, platformdb.ErrConnectorNotFound), errors.Is(err, platformdb.ErrProjectionNotFound):
		return http.StatusNotFound, "not_found", "domain resource not found"
	case errors.Is(err, domain.ErrSourceInUse):
		return http.StatusConflict, "source_in_use", "source has active subscriptions"
	case errors.Is(err, domain.ErrTargetInUse):
		return http.StatusConflict, "target_in_use", "target has active subscriptions"
	case errors.Is(err, domain.ErrMigrationRequired):
		return http.StatusConflict, "migration_required", "connector migration is required"
	case errors.Is(err, domain.ErrAmbiguousTarget):
		return http.StatusConflict, "ambiguous_target", "target identity is ambiguous"
	case errors.Is(err, platformdb.ErrSourceExists):
		return http.StatusConflict, "source_exists", "source already exists"
	case errors.Is(err, discovery.ErrInvalidProfile):
		return http.StatusBadRequest, "invalid_argument", "profile identity is invalid"
	case errors.Is(err, discovery.ErrSearchUnavailable), errors.Is(err, domain.ErrDiscoveryUnavailable):
		return http.StatusServiceUnavailable, "discovery_unavailable", "discovery is unavailable; direct resolve remains available"
	case errors.Is(err, platformdb.ErrDomainUnavailable):
		return http.StatusServiceUnavailable, "domain_unavailable", "domain service is unavailable"
	case errors.Is(err, platformdb.ErrLegacyAppliedProjectionDegraded):
		return http.StatusServiceUnavailable, "legacy_applied_projection_degraded", "Legacy subscription changed; projection reconciliation is degraded"
	case errors.Is(err, platformdb.ErrProjectionDegraded):
		return http.StatusServiceUnavailable, "projection_degraded", "Legacy subscription changed; projection reconciliation is degraded"
	default:
		return http.StatusBadRequest, "invalid_argument", "domain request is invalid"
	}
}

func errorEnvelope(err error) (int, apiEnvelope) {
	status, code, message := domainError(err)
	return status, apiEnvelope{Error: &apiError{Code: code, Message: message}, RequestID: requestID()}
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.RequireAuth(http.HandlerFunc(s.listSources)).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(s.createSource)).ServeHTTP(w, r)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	if err := s.reconcileProjection(r.Context()); err != nil {
		status, response := errorEnvelope(errors.Join(platformdb.ErrProjectionDegraded, err))
		s.writeJSON(w, status, response)
		return
	}
	sources, err := s.domainRepository.ListSources(r.Context())
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	projections, err := s.domainRepository.ListProjections(r.Context())
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	counts := make(map[string]int)
	for _, projection := range projections {
		counts[projection.SourceID]++
	}
	items := make([]sourceDTO, 0, len(sources))
	for _, source := range sources {
		items = append(items, sourceDTOFrom(source, counts[source.ID]))
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	var request sourceCreateRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "source request is invalid")
		return
	}
	s.executeDomainCommand(w, r, "create_source", body, func() (int, apiEnvelope) {
		platformName := domain.NormalizePlatform(request.Platform)
		var source domain.Source
		switch platformName {
		case "bilibili":
			candidate, resolveErr := s.bilibiliResolver.Resolve(r.Context(), firstNonEmpty(request.ExternalID, request.ProfileURL))
			if resolveErr != nil {
				status, response := errorEnvelope(resolveErr)
				return status, response
			}
			source = domain.Source{Platform: domain.Platform("bilibili"), ExternalID: candidate.UID, Handle: candidate.UID, DisplayName: firstNonEmpty(request.DisplayName, candidate.Name), CanonicalURL: candidate.ProfileURL, Status: domain.SourceActive, MetadataJSON: `{"resolver":"bilibili"}`}
		case "twitter", "x":
			resolved, resolveErr := s.twitterResolver.Resolve(r.Context(), firstNonEmpty(request.ExternalID, request.Handle, request.ProfileURL))
			if resolveErr != nil {
				status, response := errorEnvelope(resolveErr)
				return status, response
			}
			source = resolved
			source.DisplayName = firstNonEmpty(request.DisplayName, source.Handle, source.ExternalID)
		default:
			return errorEnvelope(errors.New("unsupported source platform"))
		}
		created, createErr := s.domainRepository.CreateSource(r.Context(), source)
		if createErr != nil {
			status, response := errorEnvelope(createErr)
			return status, response
		}
		return http.StatusCreated, apiEnvelope{Data: sourceDTOFrom(created, 0)}
	})
}

func (s *Server) handleSourceDetail(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.getSource(w, r, id) })).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPatch {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.patchSource(w, r, id) })).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.deleteSource(w, r, id) })).ServeHTTP(w, r)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) getSource(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	source, err := s.domainRepository.Source(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	projections, err := s.domainRepository.ProjectionsForSource(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"source": sourceDTOFrom(source, len(projections)), "projections": mapProjectionDTOs(projections)}})
}

func (s *Server) patchSource(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	var request sourcePatchRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "source patch is invalid")
		return
	}
	s.executeDomainCommand(w, r, "update_source", body, func() (int, apiEnvelope) {
		source, getErr := s.domainRepository.Source(r.Context(), id)
		if getErr != nil {
			status, response := errorEnvelope(getErr)
			return status, response
		}
		if request.DisplayName != "" {
			source.DisplayName = request.DisplayName
		}
		if request.Handle != "" {
			source.Handle = request.Handle
		}
		if request.Status != "" {
			source.Status = request.Status
		}
		if request.Metadata != nil {
			raw, _ := json.Marshal(request.Metadata)
			source.MetadataJSON = string(raw)
		}
		updated, updateErr := s.domainRepository.UpdateSource(r.Context(), source)
		if updateErr != nil {
			status, response := errorEnvelope(updateErr)
			return status, response
		}
		return http.StatusOK, apiEnvelope{Data: sourceDTOFrom(updated, 0)}
	})
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.executeDomainCommand(w, r, "delete_source", body, func() (int, apiEnvelope) {
		if err := s.domainRepository.DeleteSource(r.Context(), id); err != nil {
			status, response := errorEnvelope(err)
			return status, response
		}
		return http.StatusOK, apiEnvelope{Data: map[string]any{"deleted": true, "id": id}}
	})
}

func (s *Server) listSourceTargets(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	if _, err := s.domainRepository.Source(r.Context(), id); err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	projections, err := s.domainRepository.ProjectionsForSource(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	items := make([]map[string]any, 0, len(projections))
	for _, projection := range projections {
		target, targetErr := s.domainRepository.Target(r.Context(), projection.TargetID)
		if targetErr != nil {
			continue
		}
		connector, connectorErr := s.domainRepository.Connector(r.Context(), target.ConnectorID)
		kind := ""
		if connectorErr == nil {
			kind = connector.Kind
		}
		items = append(items, map[string]any{"target": targetDTOFrom(target, kind, 1), "subscription": projectionDTOFrom(projection)})
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.RequireAuth(http.HandlerFunc(s.listTargets)).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(s.createTarget)).ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
}

func (s *Server) createTarget(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	var request targetCreateRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "target request is invalid")
		return
	}
	s.executeDomainCommand(w, r, "create_target", body, func() (int, apiEnvelope) {
		connector, getErr := s.domainRepository.Connector(r.Context(), strings.TrimSpace(request.ConnectorID))
		if getErr != nil {
			status, response := errorEnvelope(getErr)
			return status, response
		}
		targetType := domain.TargetType(strings.ToLower(strings.TrimSpace(request.TargetType)))
		if targetType != domain.TargetGroup && targetType != domain.TargetChannel {
			return errorEnvelope(errors.New("private targets are not supported"))
		}
		metadata := "{}"
		if request.Metadata != nil {
			raw, marshalErr := json.Marshal(request.Metadata)
			if marshalErr != nil {
				return errorEnvelope(marshalErr)
			}
			metadata = string(raw)
		}
		target, upsertErr := s.domainRepository.UpsertTarget(r.Context(), domain.Target{
			ConnectorID: connector.ID, TargetType: targetType, ExternalID: strings.TrimSpace(request.ExternalID),
			DisplayName: strings.TrimSpace(request.DisplayName), Status: firstNonEmpty(request.Status, domain.TargetResolved), MetadataJSON: metadata,
		})
		if upsertErr != nil {
			status, response := errorEnvelope(upsertErr)
			return status, response
		}
		return http.StatusCreated, apiEnvelope{Data: targetDTOFrom(target, connector.Kind, 0)}
	})
}

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	if err := s.reconcileProjection(r.Context()); err != nil {
		status, response := errorEnvelope(errors.Join(platformdb.ErrProjectionDegraded, err))
		s.writeJSON(w, status, response)
		return
	}
	targets, err := s.domainRepository.ListTargets(r.Context())
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	connectors, _ := s.domainRepository.ListConnectors(r.Context())
	connectorKinds := make(map[string]string)
	for _, connector := range connectors {
		connectorKinds[connector.ID] = connector.Kind
	}
	projections, _ := s.domainRepository.ListProjections(r.Context())
	counts := make(map[string]int)
	for _, projection := range projections {
		counts[projection.TargetID]++
	}
	items := make([]targetDTO, 0, len(targets))
	for _, target := range targets {
		items = append(items, targetDTOFrom(target, connectorKinds[target.ConnectorID], counts[target.ID]))
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) getTarget(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	target, err := s.domainRepository.Target(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	connector, _ := s.domainRepository.Connector(r.Context(), target.ConnectorID)
	projections, _ := s.domainRepository.ProjectionsForTarget(r.Context(), id)
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"target": targetDTOFrom(target, connector.Kind, len(projections)), "projections": mapProjectionDTOs(projections)}})
}

func (s *Server) patchTarget(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	var request targetPatchRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "target patch is invalid")
		return
	}
	s.executeDomainCommand(w, r, "update_target", body, func() (int, apiEnvelope) {
		target, getErr := s.domainRepository.Target(r.Context(), id)
		if getErr != nil {
			status, response := errorEnvelope(getErr)
			return status, response
		}
		if request.DisplayName != "" {
			target.DisplayName = request.DisplayName
		}
		if request.Status != "" {
			target.Status = request.Status
		}
		if request.Metadata != nil {
			raw, marshalErr := json.Marshal(request.Metadata)
			if marshalErr != nil {
				return errorEnvelope(marshalErr)
			}
			target.MetadataJSON = string(raw)
		}
		updated, updateErr := s.domainRepository.UpdateTarget(r.Context(), target)
		if updateErr != nil {
			status, response := errorEnvelope(updateErr)
			return status, response
		}
		connector, _ := s.domainRepository.Connector(r.Context(), updated.ConnectorID)
		return http.StatusOK, apiEnvelope{Data: targetDTOFrom(updated, connector.Kind, 0)}
	})
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.executeDomainCommand(w, r, "delete_target", body, func() (int, apiEnvelope) {
		if err := s.domainRepository.DeleteTarget(r.Context(), id); err != nil {
			status, response := errorEnvelope(err)
			return status, response
		}
		return http.StatusOK, apiEnvelope{Data: map[string]any{"deleted": true, "id": id}}
	})
}

func (s *Server) listTargetSources(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	if _, err := s.domainRepository.Target(r.Context(), id); err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	projections, err := s.domainRepository.ProjectionsForTarget(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	items := make([]map[string]any, 0, len(projections))
	for _, projection := range projections {
		source, sourceErr := s.domainRepository.Source(r.Context(), projection.SourceID)
		if sourceErr == nil {
			items = append(items, map[string]any{"source": sourceDTOFrom(source, 1), "subscription": projectionDTOFrom(projection)})
		}
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(s.listConnectors)).ServeHTTP(w, r)
}

func (s *Server) listConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	connectors, err := s.domainRepository.ListConnectors(r.Context())
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	items := make([]connectorDTO, 0, len(connectors))
	for _, connector := range connectors {
		items = append(items, connectorDTOFrom(connector))
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) getConnector(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	connector, err := s.domainRepository.Connector(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: connectorDTOFrom(connector)})
}

func (s *Server) patchConnector(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	var request connectorPatchRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "connector patch is invalid")
		return
	}
	s.executeDomainCommand(w, r, "update_connector", body, func() (int, apiEnvelope) {
		connector, getErr := s.domainRepository.Connector(r.Context(), id)
		if getErr != nil {
			status, response := errorEnvelope(getErr)
			return status, response
		}
		if request.Kind != "" && request.Kind != connector.Kind {
			projections, projectionErr := s.domainRepository.ProjectionsForConnector(r.Context(), connector.ID)
			if projectionErr != nil {
				status, response := errorEnvelope(projectionErr)
				return status, response
			}
			if len(projections) > 0 {
				return errorEnvelope(domain.ErrMigrationRequired)
			}
			connector.Kind = request.Kind
		}
		if request.Name != "" {
			connector.Name = request.Name
		}
		if request.Enabled != nil {
			connector.Enabled = *request.Enabled
		}
		if request.Status != "" {
			connector.Status = request.Status
		}
		if request.Endpoint != "" {
			connector.Endpoint = request.Endpoint
		}
		if request.CredentialID != "" {
			connector.CredentialID = request.CredentialID
		}
		if request.Config != nil {
			if configErr := validateConfigObject(request.Config); configErr != nil {
				return errorEnvelope(configErr)
			}
			raw, _ := json.Marshal(request.Config)
			connector.ConfigJSON = string(raw)
		}
		updated, updateErr := s.domainRepository.UpdateConnector(r.Context(), connector)
		if updateErr != nil {
			status, response := errorEnvelope(updateErr)
			return status, response
		}
		return http.StatusOK, apiEnvelope{Data: connectorDTOFrom(updated)}
	})
}

func (s *Server) testConnector(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if !s.allowConnectorTest(id, now) {
		s.writeError(w, http.StatusTooManyRequests, "rate_limited", "connector tests are temporarily rate limited")
		return
	}
	connector, err := s.domainRepository.Connector(r.Context(), id)
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	if !connector.Enabled || strings.TrimSpace(connector.Endpoint) == "" {
		s.writeError(w, http.StatusServiceUnavailable, "connection_unavailable", "connector endpoint is not configured")
		return
	}
	// The connector adapter owns real network validation. This foundation
	// endpoint only performs safe structural validation and never echoes a raw
	// response or credential.
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"ok": true, "code": "connection_valid", "connector_id": id}})
}

func (s *Server) allowConnectorTest(id string, now time.Time) bool {
	if s == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.testMu.Lock()
	defer s.testMu.Unlock()
	if s.testLast == nil {
		s.testLast = make(map[string]time.Time)
	}
	last, exists := s.testLast[id]
	if exists && now.Sub(last) < 5*time.Second {
		return false
	}
	s.testLast[id] = now
	return true
}

func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.RequireAuth(http.HandlerFunc(s.listSubscriptions)).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(s.createSubscription)).ServeHTTP(w, r)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	if err := s.reconcileProjection(r.Context()); err != nil {
		status, response := errorEnvelope(errors.Join(platformdb.ErrProjectionDegraded, err))
		s.writeJSON(w, status, response)
		return
	}
	projections, err := s.domainRepository.ListProjections(r.Context())
	if err != nil {
		status, response := errorEnvelope(err)
		s.writeJSON(w, status, response)
		return
	}
	items := make([]map[string]any, 0, len(projections))
	for _, projection := range projections {
		source, sourceErr := s.domainRepository.Source(r.Context(), projection.SourceID)
		target, targetErr := s.domainRepository.Target(r.Context(), projection.TargetID)
		item := map[string]any{"subscription": projectionDTOFrom(projection)}
		if sourceErr == nil {
			item["source"] = sourceDTOFrom(source, 1)
		}
		if targetErr == nil {
			connector, _ := s.domainRepository.Connector(r.Context(), target.ConnectorID)
			item["target"] = targetDTOFrom(target, connector.Kind, 1)
		}
		items = append(items, item)
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}})
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.domainAvailable(w) {
		return
	}
	var request subscriptionCreateRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "subscription request is invalid")
		return
	}
	s.executeDomainCommand(w, r, "create_subscription", body, func() (int, apiEnvelope) {
		source, sourceErr := s.domainRepository.Source(r.Context(), request.SourceID)
		if sourceErr != nil {
			status, response := errorEnvelope(sourceErr)
			return status, response
		}
		target, targetErr := s.domainRepository.Target(r.Context(), request.TargetID)
		if targetErr != nil {
			status, response := errorEnvelope(targetErr)
			return status, response
		}
		connector, _ := s.domainRepository.Connector(r.Context(), target.ConnectorID)
		if target.TargetType != domain.TargetGroup || connector.Kind != domain.ConnectorOneBot {
			return errorEnvelope(errors.New("unsupported subscription target"))
		}
		groupCode, parseErr := strconv.ParseInt(target.ExternalID, 10, 64)
		if parseErr != nil || groupCode <= 0 {
			return errorEnvelope(errors.New("invalid target external id"))
		}
		_, legacyErr := s.legacySubscriptions.Subscribe(r.Context(), subscription.Request{Site: string(source.Platform), ID: source.ExternalID, Type: request.Type, GroupCode: groupCode})
		if legacyErr != nil {
			return errorEnvelope(legacyErr)
		}
		if projectionErr := s.rebuildProjection(r.Context()); projectionErr != nil {
			return errorEnvelope(errors.Join(platformdb.ErrLegacyAppliedProjectionDegraded, projectionErr))
		}
		return http.StatusCreated, apiEnvelope{Data: map[string]any{"status": "active", "source_id": source.ID, "target_id": target.ID, "type": request.Type}}
	})
}

func (s *Server) handleSubscriptionDetail(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodPatch {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.patchSubscription(w, r, id) })).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.deleteSubscription(w, r, id) })).ServeHTTP(w, r)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func projectionByID(ctx context.Context, repo *platformdb.DomainRepository, id string) (domain.SubscriptionProjection, error) {
	values, err := repo.ListProjections(ctx)
	if err != nil {
		return domain.SubscriptionProjection{}, err
	}
	for _, value := range values {
		if value.ID == id {
			return value, nil
		}
	}
	return domain.SubscriptionProjection{}, platformdb.ErrProjectionNotFound
}

func projectionType(value domain.SubscriptionProjection) string {
	left := strings.SplitN(value.LegacyKey, "|", 2)[0]
	parts := strings.SplitN(left, ":", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	return ""
}

func (s *Server) patchSubscription(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	var request subscriptionPatchRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "subscription patch is invalid")
		return
	}
	s.executeDomainCommand(w, r, "update_subscription", body, func() (int, apiEnvelope) {
		projection, getErr := projectionByID(r.Context(), s.domainRepository, id)
		if getErr != nil {
			status, response := errorEnvelope(getErr)
			return status, response
		}
		source, sourceErr := s.domainRepository.Source(r.Context(), projection.SourceID)
		target, targetErr := s.domainRepository.Target(r.Context(), projection.TargetID)
		if sourceErr != nil {
			status, response := errorEnvelope(sourceErr)
			return status, response
		}
		if targetErr != nil {
			status, response := errorEnvelope(targetErr)
			return status, response
		}
		groupCode, parseErr := strconv.ParseInt(target.ExternalID, 10, 64)
		if parseErr != nil {
			return errorEnvelope(errors.New("invalid target external id"))
		}
		typeName := firstNonEmpty(request.Type, projectionType(projection))
		if typeName == "" {
			return errorEnvelope(errors.New("subscription type is required"))
		}
		if updateErr := s.legacySubscriptions.UpdateOptions(r.Context(), subscription.Request{Site: string(source.Platform), ID: source.ExternalID, Type: typeName, GroupCode: groupCode}, request.Options); updateErr != nil {
			return errorEnvelope(updateErr)
		}
		if rebuildErr := s.rebuildProjection(r.Context()); rebuildErr != nil {
			return errorEnvelope(errors.Join(platformdb.ErrLegacyAppliedProjectionDegraded, rebuildErr))
		}
		return http.StatusOK, apiEnvelope{Data: map[string]any{"id": id, "status": "active"}}
	})
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request, id string) {
	if !s.domainAvailable(w) {
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.executeDomainCommand(w, r, "delete_subscription", body, func() (int, apiEnvelope) {
		projection, getErr := projectionByID(r.Context(), s.domainRepository, id)
		if getErr != nil {
			status, response := errorEnvelope(getErr)
			return status, response
		}
		source, sourceErr := s.domainRepository.Source(r.Context(), projection.SourceID)
		target, targetErr := s.domainRepository.Target(r.Context(), projection.TargetID)
		if sourceErr != nil {
			status, response := errorEnvelope(sourceErr)
			return status, response
		}
		if targetErr != nil {
			status, response := errorEnvelope(targetErr)
			return status, response
		}
		groupCode, parseErr := strconv.ParseInt(target.ExternalID, 10, 64)
		if parseErr != nil {
			return errorEnvelope(errors.New("invalid target external id"))
		}
		if _, removeErr := s.legacySubscriptions.Unsubscribe(r.Context(), subscription.Request{Site: string(source.Platform), ID: source.ExternalID, Type: projectionType(projection), GroupCode: groupCode}); removeErr != nil {
			return errorEnvelope(removeErr)
		}
		if rebuildErr := s.rebuildProjection(r.Context()); rebuildErr != nil {
			return errorEnvelope(errors.Join(platformdb.ErrLegacyAppliedProjectionDegraded, rebuildErr))
		}
		return http.StatusOK, apiEnvelope{Data: map[string]any{"deleted": true, "id": id}}
	})
}

func (s *Server) handleRebuildProjection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.domainAvailable(w) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.executeDomainCommand(w, r, "rebuild_projection", body, func() (int, apiEnvelope) {
			if err := s.rebuildProjection(r.Context()); err != nil {
				status, response := errorEnvelope(err)
				return status, response
			}
			return http.StatusOK, apiEnvelope{Data: map[string]any{"status": "rebuilt"}}
		})
	})).ServeHTTP(w, r)
}

func (s *Server) rebuildProjection(ctx context.Context) error {
	records, err := s.legacySubscriptions.Snapshot(ctx)
	if err != nil {
		return err
	}
	return s.domainRepository.RebuildProjection(ctx, records)
}

func (s *Server) reconcileProjection(ctx context.Context) error {
	records, err := s.legacySubscriptions.Snapshot(ctx)
	if err != nil {
		return err
	}
	current, err := s.domainRepository.ListProjections(ctx)
	if err != nil {
		return err
	}
	if len(records) != len(current) {
		return s.domainRepository.RebuildProjection(ctx, records)
	}
	known := make(map[string]struct{}, len(current))
	for _, value := range current {
		known[value.LegacyKey] = struct{}{}
	}
	for _, record := range records {
		if _, ok := known[legacyRecordKey(record)]; !ok {
			return s.domainRepository.RebuildProjection(ctx, records)
		}
	}
	byKey := make(map[string]domain.SubscriptionProjection, len(current))
	for _, value := range current {
		byKey[value.LegacyKey] = value
	}
	for _, record := range records {
		value := byKey[legacyRecordKey(record)]
		if value.Enabled != record.Enabled || domainJSONForCompare(value.LegacyOptionsSnapshotJSON) != domainJSONForCompare(record.OptionsJSON) {
			return s.domainRepository.RebuildProjection(ctx, records)
		}
	}
	return nil
}

func legacyRecordKey(record domain.LegacySubscription) string {
	if strings.TrimSpace(record.LegacyKey) != "" {
		return record.LegacyKey
	}
	return record.Platform + ":" + record.ExternalID + ":" + record.SubscriptionType + "|" + record.TargetType + ":" + record.TargetExternalID
}

func domainJSONForCompare(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "{}"
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(canonical)
}

func (s *Server) handleBilibiliSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request discoveryRequest
		if _, err := readJSONBody(r, &request); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_argument", "discovery request is invalid")
			return
		}
		candidates, err := s.bilibiliResolver.Search(r.Context(), strings.TrimSpace(request.Query))
		if err != nil {
			status, response := errorEnvelope(err)
			s.writeJSON(w, status, response)
			return
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": candidates}})
	})).ServeHTTP(w, r)
}

func (s *Server) handleBilibiliResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request discoveryRequest
		if _, err := readJSONBody(r, &request); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_argument", "discovery request is invalid")
			return
		}
		candidate, err := s.bilibiliResolver.Resolve(r.Context(), firstNonEmpty(request.Value, request.Query))
		if err != nil {
			status, response := errorEnvelope(err)
			s.writeJSON(w, status, response)
			return
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: candidate})
	})).ServeHTTP(w, r)
}

func (s *Server) handleTwitterResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request discoveryRequest
		if _, err := readJSONBody(r, &request); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_argument", "discovery request is invalid")
			return
		}
		source, err := s.twitterResolver.Resolve(r.Context(), firstNonEmpty(request.Value, request.Query))
		if err != nil {
			status, response := errorEnvelope(err)
			s.writeJSON(w, status, response)
			return
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: sourceDTOFrom(source, 0)})
	})).ServeHTTP(w, r)
}

func (s *Server) handleDomainSubresource(w http.ResponseWriter, r *http.Request) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	if trimmed == r.URL.Path {
		return false
	}
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 {
		return false
	}
	decode := func(value string) string {
		value = strings.TrimSpace(value)
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return value
		}
		return decoded
	}
	for index := range parts {
		parts[index] = decode(parts[index])
	}
	switch parts[0] {
	case "sources":
		if len(parts) == 2 {
			s.handleSourceDetail(w, r, parts[1])
			return true
		}
		if len(parts) == 3 && parts[2] == "targets" {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.listSourceTargets(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
	case "targets":
		if len(parts) == 2 {
			if r.Method == http.MethodGet {
				s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.getTarget(w, r, parts[1]) })).ServeHTTP(w, r)
			} else if r.Method == http.MethodPatch {
				s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.patchTarget(w, r, parts[1]) })).ServeHTTP(w, r)
			} else if r.Method == http.MethodDelete {
				s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.deleteTarget(w, r, parts[1]) })).ServeHTTP(w, r)
			} else {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			}
			return true
		}
		if len(parts) == 3 && parts[2] == "sources" {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.listTargetSources(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
	case "connectors":
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.getConnector(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 2 && r.Method == http.MethodPatch {
			s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.patchConnector(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 3 && parts[2] == "test" {
			s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.testConnector(w, r, parts[1]) })).ServeHTTP(w, r)
			return true
		}
	case "subscriptions":
		if len(parts) == 2 {
			s.handleSubscriptionDetail(w, r, parts[1])
			return true
		}
	}
	if parts[0] == "sources" || parts[0] == "targets" || parts[0] == "connectors" || parts[0] == "subscriptions" {
		s.writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return true
	}
	return false
}

func mapProjectionDTOs(values []domain.SubscriptionProjection) []projectionDTO {
	items := make([]projectionDTO, 0, len(values))
	for _, value := range values {
		items = append(items, projectionDTOFrom(value))
	}
	return items
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
