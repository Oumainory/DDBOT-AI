package adminapi

// This file owns the small, authenticated Phase 4 AI Shadow API.  The API is
// deliberately an observation/configuration surface: it never changes the
// Legacy delivery decision and it never returns provider credentials.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/classifier"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/internal/normalizer"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/policy"
	"github.com/cnxysoft/DDBOT-WSa/internal/provider"
	"github.com/cnxysoft/DDBOT-WSa/internal/secretstore"
)

const (
	aiPrimaryProviderID = "ai-primary"
	aiPrimaryCredential = "ai-primary-key"
	aiCursorVersion     = 1
)

type aiProviderDTO struct {
	ID                          string `json:"id"`
	Kind                        string `json:"kind"`
	BaseURL                     string `json:"base_url"`
	Model                       string `json:"model"`
	CredentialID                string `json:"credential_id,omitempty"`
	CredentialConfigured        bool   `json:"credential_configured"`
	CredentialMasked            string `json:"credential_masked,omitempty"`
	StructuredOutputMode        string `json:"structured_output_mode"`
	RequestTimeoutMS            int64  `json:"request_timeout_ms"`
	MaxConcurrency              int    `json:"max_concurrency"`
	QueueCapacity               int    `json:"queue_capacity"`
	PricingCurrency             string `json:"pricing_currency"`
	InputPriceMicrosPerMillion  int64  `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion int64  `json:"output_price_micros_per_million"`
	RequestPriceMicros          int64  `json:"request_price_micros"`
	Enabled                     bool   `json:"enabled"`
	CreatedAt                   string `json:"created_at"`
	UpdatedAt                   string `json:"updated_at"`
}

type aiProviderRequest struct {
	ID                          string `json:"id"`
	Kind                        string `json:"kind"`
	BaseURL                     string `json:"base_url"`
	Model                       string `json:"model"`
	CredentialID                string `json:"credential_id"`
	APIKey                      string `json:"api_key"`
	StructuredOutputMode        string `json:"structured_output_mode"`
	RequestTimeoutMS            int64  `json:"request_timeout_ms"`
	MaxConcurrency              int    `json:"max_concurrency"`
	QueueCapacity               int    `json:"queue_capacity"`
	PricingCurrency             string `json:"pricing_currency"`
	InputPriceMicrosPerMillion  int64  `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion int64  `json:"output_price_micros_per_million"`
	RequestPriceMicros          int64  `json:"request_price_micros"`
	Enabled                     *bool  `json:"enabled"`
}

type aiProfileRequest struct {
	ID              string                            `json:"id"`
	Name            string                            `json:"name"`
	Description     string                            `json:"description"`
	DefaultAction   policy.Action                     `json:"default_action"`
	CategoryActions map[domain.Category]policy.Action `json:"category_actions"`
	TagActions      map[string]policy.Action          `json:"tag_actions"`
	Safety          map[string]bool                   `json:"safety"`
}

type aiPolicyRequest struct {
	Mode            policy.Mode                       `json:"mode"`
	ProfileID       string                            `json:"profile_id"`
	Threshold       *float64                          `json:"threshold"`
	DefaultAction   policy.Action                     `json:"default_action"`
	CategoryActions map[domain.Category]policy.Action `json:"category_actions"`
	TagActions      map[string]policy.Action          `json:"tag_actions"`
}

type aiDecisionReviewRequest struct {
	Reviewed bool `json:"reviewed"`
}

// aiEvaluationCaseRequest is deliberately separate from the durable record:
// observation_event_id is a convenience input used to build a frozen public
// NormalizedEvent snapshot and is never persisted as an untrusted source blob.
type aiEvaluationCaseRequest struct {
	ID                      string          `json:"id"`
	NormalizedInputSnapshot json.RawMessage `json:"normalized_input_snapshot"`
	ObservationEventID      string          `json:"observation_event_id"`
	ExpectedImportance      string          `json:"expected_importance"`
	ExpectedAction          string          `json:"expected_action"`
	Critical                bool            `json:"critical"`
	LabelKind               string          `json:"label_kind"`
	Notes                   string          `json:"notes"`
	Source                  string          `json:"source"`
}

type aiEvaluationCasePatch struct {
	ExpectedImportance *string `json:"expected_importance"`
	ExpectedAction     *string `json:"expected_action"`
	Critical           *bool   `json:"critical"`
	LabelKind          *string `json:"label_kind"`
	Notes              *string `json:"notes"`
	Source             *string `json:"source"`
}

type aiEvaluationCaseImportRequest struct {
	Items []platformdb.EvaluationCaseRecord `json:"items"`
}

type aiCursorPayload struct {
	Version int    `json:"v"`
	At      int64  `json:"t"`
	ID      string `json:"id"`
}

type aiDecisionDTO struct {
	ID                  string                    `json:"id"`
	NormalizedEventID   string                    `json:"normalized_event_id"`
	ClassifierReleaseID string                    `json:"classifier_release_id"`
	Status              classifier.DecisionStatus `json:"status"`
	ModeAtSchedule      string                    `json:"mode_at_schedule"`
	Classification      classifier.Classification `json:"classification"`
	SuggestedAction     string                    `json:"suggested_action"`
	EffectiveAction     string                    `json:"effective_action"`
	HardPassReason      string                    `json:"hard_pass_reason,omitempty"`
	Provider            string                    `json:"provider,omitempty"`
	Model               string                    `json:"model,omitempty"`
	Usage               classifier.Usage          `json:"usage"`
	CostMicros          *int64                    `json:"cost_micros,omitempty"`
	CostCurrency        string                    `json:"cost_currency,omitempty"`
	LatencyMS           *int64                    `json:"latency_ms,omitempty"`
	ErrorCode           string                    `json:"error_code,omitempty"`
	ScheduledAt         string                    `json:"scheduled_at"`
	CallStartedAt       *string                   `json:"call_started_at,omitempty"`
	CompletedAt         *string                   `json:"completed_at,omitempty"`
	Reviewed            bool                      `json:"reviewed"`
	ReviewedAt          *string                   `json:"reviewed_at,omitempty"`
	CreatedAt           string                    `json:"created_at"`
}

type aiNormalizedEventDTO struct {
	ID                  string                  `json:"id"`
	SchemaVersion       int                     `json:"schema_version"`
	NormalizedEventID   string                  `json:"normalized_event_id"`
	ObservedEventID     string                  `json:"observed_event_id,omitempty"`
	Platform            string                  `json:"platform"`
	SourceID            string                  `json:"source_id"`
	ExternalID          string                  `json:"external_id"`
	SourceDisplayName   string                  `json:"source_display_name,omitempty"`
	EventType           string                  `json:"event_type"`
	AuthorID            string                  `json:"author_id,omitempty"`
	AuthorName          string                  `json:"author_name,omitempty"`
	Title               string                  `json:"title,omitempty"`
	Body                string                  `json:"body,omitempty"`
	RelatedBody         string                  `json:"related_body,omitempty"`
	URL                 string                  `json:"url,omitempty"`
	PublicURLs          []string                `json:"public_urls,omitempty"`
	Media               []domain.MediaReference `json:"media,omitempty"`
	SourceEventAt       *string                 `json:"source_event_at,omitempty"`
	ObservedAt          string                  `json:"observed_at"`
	NormalizerVersion   string                  `json:"normalizer_version"`
	PreprocessorVersion string                  `json:"preprocessor_version,omitempty"`
	Truncated           bool                    `json:"truncated,omitempty"`
	NormalizationFlags  []string                `json:"normalization_flags,omitempty"`
	CreatedAt           string                  `json:"created_at"`
}

func aiProviderDTOFrom(value platformdb.AIProviderConfigRecord, configured bool) aiProviderDTO {
	masked := ""
	if configured {
		masked = "configured"
	}
	return aiProviderDTO{ID: value.ID, Kind: value.Kind, BaseURL: safeAIBaseURL(value.BaseURL), Model: value.Model,
		CredentialID: value.CredentialID, CredentialConfigured: configured, CredentialMasked: masked,
		StructuredOutputMode: value.StructuredOutputMode, RequestTimeoutMS: value.RequestTimeoutMS,
		MaxConcurrency: value.MaxConcurrency, QueueCapacity: value.QueueCapacity, PricingCurrency: value.PricingCurrency,
		InputPriceMicrosPerMillion: value.InputPriceMicrosPerMillion, OutputPriceMicrosPerMillion: value.OutputPriceMicrosPerMillion,
		RequestPriceMicros: value.RequestPriceMicros, Enabled: value.Enabled, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

// safeAIBaseURL is a presentation-boundary sanitizer. Provider endpoints are
// user-configurable (including private/local endpoints), but a URL must never
// echo embedded userinfo or bearer-like query material back through the API.
func safeAIBaseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key") || strings.Contains(lower, "auth") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || lower == "sig" || strings.Contains(lower, "signature") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Server) aiAvailable(w http.ResponseWriter) bool {
	if s == nil || s.aiRepository == nil {
		s.writeError(w, http.StatusServiceUnavailable, "ai_unavailable", "AI Shadow is unavailable")
		return false
	}
	return true
}

func (s *Server) handleAIProvider(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.RequireAuth(http.HandlerFunc(s.getAIProvider)).ServeHTTP(w, r)
	case http.MethodPost, http.MethodPatch, http.MethodPut:
		s.RequireMutation(http.HandlerFunc(s.saveAIProvider)).ServeHTTP(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) getAIProvider(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	providers, err := s.aiRepository.Providers(r.Context())
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	items := make([]aiProviderDTO, 0, len(providers))
	for _, item := range providers {
		configured := strings.TrimSpace(item.CredentialID) != ""
		if configured && s.secretStore != nil {
			if view, metadataErr := s.secretStore.Metadata(r.Context(), item.CredentialID); metadataErr == nil {
				configured = view.Configured
			}
		}
		items = append(items, aiProviderDTOFrom(item, configured))
	}
	if len(items) == 0 {
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"configured": false, "items": []aiProviderDTO{}}})
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"configured": true, "provider": items[0], "items": items}})
}

func (s *Server) saveAIProvider(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	var request aiProviderRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "provider configuration is invalid")
		return
	}
	s.executeDomainCommand(w, r, "ai_provider_update", body, func() (int, apiEnvelope) {
		ctx := r.Context()
		current, currentErr := s.aiRepository.Provider(ctx)
		if currentErr != nil && !errors.Is(currentErr, platformdb.ErrAIProviderNotFound) {
			return s.aiErrorEnvelope(currentErr)
		}
		hasCurrent := currentErr == nil
		if request.ID == "" {
			if hasCurrent {
				request.ID = current.ID
			} else {
				request.ID = aiPrimaryProviderID
			}
		}
		if request.Kind == "" {
			if hasCurrent {
				request.Kind = current.Kind
			} else {
				request.Kind = "openai_compatible"
			}
		}
		if strings.TrimSpace(request.BaseURL) == "" && hasCurrent {
			request.BaseURL = current.BaseURL
		}
		if strings.TrimSpace(request.Model) == "" && hasCurrent {
			request.Model = current.Model
		}
		if strings.TrimSpace(request.StructuredOutputMode) == "" && hasCurrent {
			request.StructuredOutputMode = current.StructuredOutputMode
		}
		if strings.TrimSpace(request.CredentialID) == "" && hasCurrent {
			request.CredentialID = current.CredentialID
		}
		if request.RequestTimeoutMS == 0 && hasCurrent {
			request.RequestTimeoutMS = current.RequestTimeoutMS
		}
		if request.MaxConcurrency == 0 && hasCurrent {
			request.MaxConcurrency = current.MaxConcurrency
		}
		if request.QueueCapacity == 0 && hasCurrent {
			request.QueueCapacity = current.QueueCapacity
		}
		if strings.TrimSpace(request.PricingCurrency) == "" && hasCurrent {
			request.PricingCurrency = current.PricingCurrency
		}
		if request.InputPriceMicrosPerMillion == 0 && hasCurrent {
			request.InputPriceMicrosPerMillion = current.InputPriceMicrosPerMillion
		}
		if request.OutputPriceMicrosPerMillion == 0 && hasCurrent {
			request.OutputPriceMicrosPerMillion = current.OutputPriceMicrosPerMillion
		}
		if request.RequestPriceMicros == 0 && hasCurrent {
			request.RequestPriceMicros = current.RequestPriceMicros
		}
		if request.RequestTimeoutMS <= 0 {
			request.RequestTimeoutMS = provider.DefaultTimeout.Milliseconds()
		}
		if request.MaxConcurrency <= 0 {
			request.MaxConcurrency = 2
		}
		if request.QueueCapacity <= 0 {
			request.QueueCapacity = 64
		}
		if strings.TrimSpace(request.PricingCurrency) == "" {
			request.PricingCurrency = "USD"
		}
		if request.Enabled == nil {
			enabled := true
			if hasCurrent {
				enabled = current.Enabled
			}
			request.Enabled = &enabled
		}
		if request.CredentialID == "" && strings.TrimSpace(request.APIKey) != "" {
			request.CredentialID = aiPrimaryCredential
		}
		request.CredentialID = strings.TrimSpace(request.CredentialID)
		// Validate the complete provider shape before touching Secret Store. In
		// particular, malformed/private URLs and unsupported output modes must not
		// consume or replace an operator's credential as a side effect of a bad
		// configuration request.
		candidate := provider.Config{BaseURL: request.BaseURL, Model: request.Model, StructuredOutputMode: request.StructuredOutputMode, RequestTimeout: time.Duration(request.RequestTimeoutMS) * time.Millisecond}
		if _, validationErr := provider.New(candidate); validationErr != nil {
			return s.aiErrorEnvelope(validationErr)
		}
		if strings.TrimSpace(request.APIKey) != "" {
			if s.secretStore == nil || request.CredentialID == "" {
				return s.aiErrorEnvelope(secretstore.ErrSecretStoreUnavailable)
			}
			if _, metadataErr := s.secretStore.Metadata(ctx, request.CredentialID); errors.Is(metadataErr, secretstore.ErrCredentialNotFound) {
				if _, createErr := s.secretStore.CreateCredential(ctx, secretstore.CredentialInput{ID: request.CredentialID, Type: "openai_api_key", Label: "AI provider", Source: "ai"}); createErr != nil && !errors.Is(createErr, platformdb.ErrCredentialExists) {
					return s.aiErrorEnvelope(createErr)
				}
			} else if metadataErr != nil && !errors.Is(metadataErr, platformdb.ErrCredentialNotFound) {
				return s.aiErrorEnvelope(metadataErr)
			}
			if setErr := s.secretStore.SetSecret(ctx, request.CredentialID, []byte(request.APIKey)); setErr != nil {
				return s.aiErrorEnvelope(setErr)
			}
		}
		var client provider.Provider
		if *request.Enabled {
			if s.secretStore == nil || request.CredentialID == "" {
				return s.aiErrorEnvelope(secretstore.ErrSecretStoreUnavailable)
			}
			key, resolveErr := s.secretStore.ResolveSecret(ctx, request.CredentialID)
			if resolveErr != nil {
				return s.aiErrorEnvelope(resolveErr)
			}
			defer clearBytes(key)
			configuredClient, clientErr := provider.New(provider.Config{BaseURL: request.BaseURL, Model: request.Model, APIKey: string(key), StructuredOutputMode: request.StructuredOutputMode, RequestTimeout: time.Duration(request.RequestTimeoutMS) * time.Millisecond})
			if clientErr != nil {
				return s.aiErrorEnvelope(clientErr)
			}
			client = configuredClient
		}
		stamp := s.now()
		config := platformdb.AIProviderConfigRecord{ID: request.ID, Kind: "openai_compatible", BaseURL: classifier.CanonicalBaseURL(request.BaseURL), Model: strings.TrimSpace(request.Model), CredentialID: request.CredentialID, StructuredOutputMode: request.StructuredOutputMode, RequestTimeoutMS: request.RequestTimeoutMS, MaxConcurrency: request.MaxConcurrency, QueueCapacity: request.QueueCapacity, PricingCurrency: request.PricingCurrency, InputPriceMicrosPerMillion: request.InputPriceMicrosPerMillion, OutputPriceMicrosPerMillion: request.OutputPriceMicrosPerMillion, RequestPriceMicros: request.RequestPriceMicros, Enabled: *request.Enabled, CreatedAt: stamp, UpdatedAt: stamp}
		if saveErr := s.aiRepository.SaveProvider(ctx, config); saveErr != nil {
			return s.aiErrorEnvelope(saveErr)
		}
		fingerprint, fpErr := classifier.Fingerprint(classifier.ReleaseSpec{ProviderType: "openai-compatible", BaseURL: config.BaseURL, Model: config.Model, Prompt: classifier.BuiltInPrompt, SchemaVersion: classifier.ClassificationSchemaVersion, StructuredOutputMode: config.StructuredOutputMode, PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-twitter-v1"})
		if fpErr != nil {
			return s.aiErrorEnvelope(fpErr)
		}
		releaseID := "release_" + strings.TrimPrefix(fingerprint, "sha256:")[:24]
		if existing, listErr := s.aiRepository.Releases(ctx); listErr == nil {
			for _, candidate := range existing {
				if candidate.Fingerprint == fingerprint {
					releaseID = candidate.ID
					break
				}
			}
		}
		release := classifier.Release{ID: releaseID, Fingerprint: fingerprint, ProviderType: "openai-compatible", BaseURL: config.BaseURL, Model: config.Model, PromptVersion: classifier.PromptVersion, PromptDigest: classifier.PromptDigest(), SchemaVersion: classifier.ClassificationSchemaVersion, SchemaDigest: classifier.SchemaDigest(), StructuredOutputMode: config.StructuredOutputMode, PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-twitter-v1", PricingCurrency: config.PricingCurrency, InputPriceMicrosPerMillion: config.InputPriceMicrosPerMillion, OutputPriceMicrosPerMillion: config.OutputPriceMicrosPerMillion, RequestPriceMicros: config.RequestPriceMicros, Active: config.Enabled, CreatedAt: stamp}
		if saveErr := s.aiRepository.SaveRelease(ctx, release); saveErr != nil {
			return s.aiErrorEnvelope(saveErr)
		}
		if config.Enabled {
			if activateErr := s.aiRepository.ActivateRelease(ctx, release.ID); activateErr != nil {
				return s.aiErrorEnvelope(activateErr)
			}
		} else if deactivateErr := s.aiRepository.DeactivateReleases(ctx); deactivateErr != nil {
			return s.aiErrorEnvelope(deactivateErr)
		}
		if s.aiProvider != nil {
			s.aiProvider.Set(client)
		}
		configured := false
		if config.CredentialID != "" && s.secretStore != nil {
			if metadata, metadataErr := s.secretStore.Metadata(ctx, config.CredentialID); metadataErr == nil {
				configured = metadata.Configured
			}
		}
		if s.migrationRepository != nil {
			s.appendAIAudit(ctx, r, "ai.provider.configuration_commit", "ai_provider", config.ID, "success", map[string]any{"enabled": config.Enabled, "model": config.Model, "structured_output_mode": config.StructuredOutputMode})
		}
		return http.StatusOK, apiEnvelope{Data: aiProviderDTOFrom(config, configured)}
	})
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func (s *Server) handleAIProviderTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		principal, _ := PrincipalFromContext(r.Context())
		now := s.now()
		s.testMu.Lock()
		last := s.aiTestLast[principal.AdminID]
		if !last.IsZero() && now.Sub(last) < 10*time.Second {
			s.testMu.Unlock()
			s.writeError(w, http.StatusTooManyRequests, "rate_limited", "provider tests are temporarily rate limited")
			return
		}
		s.aiTestLast[principal.AdminID] = now
		s.testMu.Unlock()
		var request aiProviderRequest
		// net/http represents an empty request body as http.NoBody rather than
		// nil. Treat that case as "use the saved active provider" so a simple
		// POST probe does not fail JSON decoding before the provider boundary.
		if r.Body != nil && r.Body != http.NoBody {
			if _, err := readJSONBody(r, &request); err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "provider test request is invalid")
				return
			}
		}
		var testProvider provider.Provider
		if strings.TrimSpace(request.BaseURL) != "" || strings.TrimSpace(request.Model) != "" || strings.TrimSpace(request.APIKey) != "" {
			if s.secretStore == nil {
				s.writeAIError(w, secretstore.ErrSecretStoreUnavailable)
				return
			}
			key := []byte(request.APIKey)
			if len(key) == 0 && request.CredentialID != "" {
				var err error
				key, err = s.secretStore.ResolveSecret(r.Context(), request.CredentialID)
				if err != nil {
					s.writeAIError(w, err)
					return
				}
				defer clearBytes(key)
			}
			client, err := provider.New(provider.Config{BaseURL: request.BaseURL, Model: request.Model, APIKey: string(key), StructuredOutputMode: request.StructuredOutputMode})
			clearBytes(key)
			if err != nil {
				s.writeAIError(w, err)
				return
			}
			testProvider = client
		} else if s.aiProvider != nil {
			testProvider = s.aiProvider
		}
		if testProvider == nil {
			s.writeAIError(w, provider.ErrUnavailable)
			return
		}
		result, err := testProvider.TestConnection(r.Context())
		if err != nil {
			result.ErrorCode = provider.StableErrorCode(err)
			s.writeJSON(w, http.StatusBadGateway, apiEnvelope{Data: result, Error: &apiError{Code: result.ErrorCode, Message: "provider connection failed"}, RequestID: requestID()})
			return
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: result})
	})).ServeHTTP(w, r)
}

func (s *Server) handleAIReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		values, err := s.aiRepository.Releases(r.Context())
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
	})).ServeHTTP(w, r)
}

func (s *Server) handleAIProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.RequireAuth(http.HandlerFunc(s.listAIProfiles)).ServeHTTP(w, r)
	case http.MethodPost:
		s.RequireMutation(http.HandlerFunc(s.createAIProfile)).ServeHTTP(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
func (s *Server) listAIProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	values, err := s.aiRepository.Profiles(r.Context())
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
}
func (s *Server) createAIProfile(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	var request aiProfileRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "profile is invalid")
		return
	}
	s.executeDomainCommand(w, r, "ai_profile_create", body, func() (int, apiEnvelope) {
		if request.ID == "" {
			request.ID, _ = domain.NewID()
		}
		value := policy.Profile{ID: request.ID, Name: strings.TrimSpace(request.Name), Description: request.Description, DefaultAction: request.DefaultAction, CategoryActions: request.CategoryActions, TagActions: request.TagActions, Safety: request.Safety}
		if value.DefaultAction == "" {
			value.DefaultAction = policy.ActionPass
		}
		if err := s.aiRepository.SaveProfile(r.Context(), value, s.now(), s.now()); err != nil {
			return s.aiErrorEnvelope(err)
		}
		s.appendAIAudit(r.Context(), r, "ai.profile.create", "ai_profile", value.ID, "success", map[string]any{"builtin": value.Builtin})
		return http.StatusCreated, apiEnvelope{Data: value}
	})
}

func (s *Server) handleAIPolicy(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) {
	canonicalScope, ok := normalizeAIPolicyScope(scopeType)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "AI policy resource not found")
		return
	}
	scopeType = canonicalScope
	if r.Method == http.MethodGet {
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.getAIPolicy(w, r, scopeType, scopeID) })).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPatch || r.Method == http.MethodPut || r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.saveAIPolicy(w, r, scopeType, scopeID) })).ServeHTTP(w, r)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

// normalizeAIPolicyScope keeps the public REST paths readable (sources,
// targets, subscriptions) while the durable contract remains the singular
// scope vocabulary used by ai_policy_overrides. Unknown scopes are rejected
// before any database read or write.
func normalizeAIPolicyScope(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "global":
		return "global", true
	case "source", "sources":
		return "source", true
	case "target", "targets":
		return "target", true
	case "subscription", "subscriptions":
		return "subscription", true
	case "system":
		return "system", true
	default:
		return "", false
	}
}
func (s *Server) getAIPolicy(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) {
	if !s.aiAvailable(w) {
		return
	}
	value, err := s.aiRepository.Policy(r.Context(), scopeType, scopeID)
	if errors.Is(err, platformdb.ErrAIPolicyNotFound) {
		value = platformdb.AIPolicyOverrideRecord{ScopeType: scopeType, ScopeID: scopeID, Mode: policy.ModeInherit}
	} else if err != nil {
		s.writeAIError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
}
func (s *Server) saveAIPolicy(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) {
	if !s.aiAvailable(w) {
		return
	}
	var request aiPolicyRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "policy is invalid")
		return
	}
	if err := policy.ValidatePhase4Mode(request.Mode); err != nil {
		s.writeJSON(w, http.StatusConflict, apiEnvelope{Error: &apiError{Code: "enforce_not_available", Message: "ENFORCE is not available in Phase 4"}, RequestID: requestID()})
		return
	}
	s.executeDomainCommand(w, r, "ai_policy_update", body, func() (int, apiEnvelope) {
		value := platformdb.AIPolicyOverrideRecord{ScopeType: scopeType, ScopeID: scopeID, Mode: request.Mode, ProfileID: request.ProfileID, Threshold: request.Threshold, DefaultAction: request.DefaultAction, CategoryActions: request.CategoryActions, TagActions: request.TagActions, CreatedAt: s.now(), UpdatedAt: s.now()}
		if err := s.aiRepository.SavePolicy(r.Context(), value, s.now()); err != nil {
			return s.aiErrorEnvelope(err)
		}
		s.appendAIAudit(r.Context(), r, "ai.policy.update", "ai_policy", scopeType+":"+scopeID, "success", map[string]any{"mode": value.Mode, "profile_id": value.ProfileID})
		return http.StatusOK, apiEnvelope{Data: value}
	})
}

func (s *Server) handleAIDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		query, err := parseAIDecisionQuery(r.URL.Query())
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		values, next, err := s.aiRepository.ListDecisionPage(r.Context(), query)
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		items := make([]aiDecisionDTO, 0, len(values))
		for _, value := range values {
			items = append(items, aiDecisionDTOFrom(value))
		}
		meta := map[string]any{}
		if next != nil {
			meta["next_cursor"] = encodeAICursor(next)
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": items}, Meta: meta})
	})).ServeHTTP(w, r)
}

func (s *Server) handleAIReviewDecision(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		var request aiDecisionReviewRequest
		body, err := readJSONBody(r, &request)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_argument", "review is invalid")
			return
		}
		s.executeDomainCommand(w, r, "ai_decision_review", body, func() (int, apiEnvelope) {
			if err := s.aiRepository.ReviewDecision(r.Context(), id, request.Reviewed, s.now()); err != nil {
				return s.aiErrorEnvelope(err)
			}
			decision, err := s.aiRepository.Decision(r.Context(), id)
			if err != nil {
				return s.aiErrorEnvelope(err)
			}
			s.appendAIAudit(r.Context(), r, "ai.decision.review", "ai_decision", id, "success", map[string]any{"reviewed": request.Reviewed})
			return http.StatusOK, apiEnvelope{Data: aiDecisionDTOFrom(decision)}
		})
	})).ServeHTTP(w, r)
}
func (s *Server) handleAIShadowSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		value, err := s.aiRepository.ShadowSummary(r.Context())
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		runtime := map[string]any{}
		if s.shadowRuntime != nil {
			runtime = map[string]any{"stats": s.shadowRuntime.Stats()}
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"summary": value, "runtime": runtime}})
	})).ServeHTTP(w, r)
}

func (s *Server) handleAIEvaluationCases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.RequireAuth(http.HandlerFunc(s.listAIEvaluationCases)).ServeHTTP(w, r)
	case http.MethodPost:
		s.RequireMutation(http.HandlerFunc(s.createAIEvaluationCase)).ServeHTTP(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
func (s *Server) listAIEvaluationCases(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	values, err := s.aiRepository.EvaluationCases(r.Context())
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
}
func (s *Server) createAIEvaluationCase(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	var request aiEvaluationCaseRequest
	body, err := readJSONBody(r, &request)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "evaluation case is invalid")
		return
	}
	s.executeDomainCommand(w, r, "ai_evaluation_case_create", body, func() (int, apiEnvelope) {
		value, buildErr := s.evaluationCaseFromRequest(r, request)
		if buildErr != nil {
			return s.aiErrorEnvelope(buildErr)
		}
		if value.ID == "" {
			var idErr error
			value.ID, idErr = domain.NewID()
			if idErr != nil {
				return s.aiErrorEnvelope(platformdb.ErrEvaluationInvalid)
			}
		}
		if value.LabelKind == "" {
			value.LabelKind = "synthetic"
		}
		value.CreatedAt, value.UpdatedAt = s.now(), s.now()
		if err := s.aiRepository.CreateEvaluationCase(r.Context(), value); err != nil {
			return s.aiErrorEnvelope(err)
		}
		s.appendAIAudit(r.Context(), r, "ai.evaluation_case.create", "ai_evaluation_case", value.ID, "success", map[string]any{"label_kind": value.LabelKind, "source": value.Source})
		return http.StatusCreated, apiEnvelope{Data: value}
	})
}

func (s *Server) evaluationCaseFromRequest(r *http.Request, request aiEvaluationCaseRequest) (platformdb.EvaluationCaseRecord, error) {
	value := platformdb.EvaluationCaseRecord{ID: strings.TrimSpace(request.ID), NormalizedInputSnapshot: append(json.RawMessage(nil), request.NormalizedInputSnapshot...), ExpectedImportance: strings.TrimSpace(request.ExpectedImportance), ExpectedAction: strings.TrimSpace(request.ExpectedAction), Critical: request.Critical, LabelKind: strings.TrimSpace(request.LabelKind), Notes: request.Notes, Source: strings.TrimSpace(request.Source), CreatedAt: s.now(), UpdatedAt: s.now()}
	if strings.TrimSpace(request.ObservationEventID) != "" {
		if len(value.NormalizedInputSnapshot) != 0 {
			return platformdb.EvaluationCaseRecord{}, platformdb.ErrEvaluationInvalid
		}
		if s.observationRepository == nil {
			return platformdb.EvaluationCaseRecord{}, platformdb.ErrEvaluationInvalid
		}
		observed, err := s.observationRepository.GetObservedEvent(r.Context(), strings.TrimSpace(request.ObservationEventID))
		if err != nil {
			return platformdb.EvaluationCaseRecord{}, platformdb.ErrEvaluationInvalid
		}
		event, err := normalizer.NormalizeObserved(observed)
		if err != nil {
			return platformdb.EvaluationCaseRecord{}, platformdb.ErrEvaluationInvalid
		}
		snapshot, err := json.Marshal(event)
		if err != nil {
			return platformdb.EvaluationCaseRecord{}, platformdb.ErrEvaluationInvalid
		}
		value.NormalizedInputSnapshot = snapshot
		if value.LabelKind == "" {
			value.LabelKind = "real_reviewed"
		}
		if value.Source == "" {
			value.Source = "observation:" + strings.TrimSpace(request.ObservationEventID)
		}
	}
	return value, nil
}

func (s *Server) handleAIEvaluationCaseDetail(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.aiAvailable(w) {
				return
			}
			value, err := s.aiRepository.EvaluationCase(r.Context(), id)
			if err != nil {
				s.writeAIError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
		})).ServeHTTP(w, r)
	case http.MethodPatch, http.MethodPut:
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.aiAvailable(w) {
				return
			}
			var patch aiEvaluationCasePatch
			body, err := readJSONBody(r, &patch)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "evaluation case is invalid")
				return
			}
			s.executeDomainCommand(w, r, "ai_evaluation_case_update", body, func() (int, apiEnvelope) {
				value, getErr := s.aiRepository.EvaluationCase(r.Context(), id)
				if getErr != nil {
					return s.aiErrorEnvelope(getErr)
				}
				if patch.ExpectedImportance != nil {
					value.ExpectedImportance = strings.TrimSpace(*patch.ExpectedImportance)
				}
				if patch.ExpectedAction != nil {
					value.ExpectedAction = strings.TrimSpace(*patch.ExpectedAction)
				}
				if patch.Critical != nil {
					value.Critical = *patch.Critical
				}
				if patch.LabelKind != nil {
					value.LabelKind = strings.TrimSpace(*patch.LabelKind)
				}
				if patch.Notes != nil {
					value.Notes = *patch.Notes
				}
				if patch.Source != nil {
					value.Source = strings.TrimSpace(*patch.Source)
				}
				value.UpdatedAt = s.now()
				if updateErr := s.aiRepository.UpdateEvaluationCase(r.Context(), value); updateErr != nil {
					return s.aiErrorEnvelope(updateErr)
				}
				value, _ = s.aiRepository.EvaluationCase(r.Context(), id)
				s.appendAIAudit(r.Context(), r, "ai.evaluation_case.update", "ai_evaluation_case", id, "success", map[string]any{"label_kind": value.LabelKind})
				return http.StatusOK, apiEnvelope{Data: value}
			})
		})).ServeHTTP(w, r)
	case http.MethodDelete:
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.aiAvailable(w) {
				return
			}
			body, _ := readJSONBody(r, &map[string]any{})
			s.executeDomainCommand(w, r, "ai_evaluation_case_delete", body, func() (int, apiEnvelope) {
				if err := s.aiRepository.DeleteEvaluationCase(r.Context(), id); err != nil {
					return s.aiErrorEnvelope(err)
				}
				s.appendAIAudit(r.Context(), r, "ai.evaluation_case.delete", "ai_evaluation_case", id, "success", nil)
				return http.StatusOK, apiEnvelope{Data: map[string]any{"deleted": true, "id": id}}
			})
		})).ServeHTTP(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) exportAIEvaluationCases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		values, err := s.aiRepository.EvaluationCases(r.Context())
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		if len(values) > 1000 {
			values = values[:1000]
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
	})).ServeHTTP(w, r)
}

func (s *Server) importAIEvaluationCases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		var raw json.RawMessage
		body, err := readJSONBody(r, &raw)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_argument", "evaluation import is invalid")
			return
		}
		values, err := parseEvaluationImport(raw)
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		s.executeDomainCommand(w, r, "ai_evaluation_case_import", body, func() (int, apiEnvelope) {
			if err := s.aiRepository.ImportEvaluationCases(r.Context(), values); err != nil {
				return s.aiErrorEnvelope(err)
			}
			s.appendAIAudit(r.Context(), r, "ai.evaluation_case.import", "ai_evaluation_case", "", "success", map[string]any{"count": len(values)})
			return http.StatusCreated, apiEnvelope{Data: map[string]any{"imported": len(values)}}
		})
	})).ServeHTTP(w, r)
}

func parseEvaluationImport(raw []byte) ([]platformdb.EvaluationCaseRecord, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 1<<20 {
		return nil, platformdb.ErrEvaluationInvalid
	}
	var values []platformdb.EvaluationCaseRecord
	if raw[0] == '[' {
		if err := decodeStrictJSON(raw, &values); err != nil {
			return nil, platformdb.ErrEvaluationInvalid
		}
	} else {
		var wrapper aiEvaluationCaseImportRequest
		if err := decodeStrictJSON(raw, &wrapper); err != nil {
			return nil, platformdb.ErrEvaluationInvalid
		}
		values = wrapper.Items
	}
	if len(values) == 0 || len(values) > 1000 {
		return nil, platformdb.ErrEvaluationInvalid
	}
	return values, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing json")
		}
		return err
	}
	return nil
}
func (s *Server) handleAIEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.RequireAuth(http.HandlerFunc(s.listAIEvaluationRuns)).ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(s.createAIEvaluationRun)).ServeHTTP(w, r)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}
func (s *Server) listAIEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	values, err := s.aiRepository.EvaluationRuns(r.Context(), 100)
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
}
func (s *Server) createAIEvaluationRun(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	var value platformdb.EvaluationRunRecord
	body, err := readJSONBody(r, &value)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "evaluation run is invalid")
		return
	}
	s.executeDomainCommand(w, r, "ai_evaluation_run_create", body, func() (int, apiEnvelope) {
		if value.ID == "" {
			value.ID, _ = domain.NewID()
		}
		if value.ClassifierReleaseID == "" {
			release, releaseErr := s.aiRepository.ActiveRelease(r.Context())
			if releaseErr != nil {
				return s.aiErrorEnvelope(releaseErr)
			}
			value.ClassifierReleaseID = release.ID
		}
		value.Status = "queued"
		value.CreatedAt = s.now()
		if err := s.aiRepository.CreateEvaluationRun(r.Context(), value); err != nil {
			return s.aiErrorEnvelope(err)
		}
		// Evaluation is an explicit administrator action, not part of the
		// Legacy delivery path. Run it synchronously when a runner is wired so
		// the returned record has a durable terminal result; a missing provider
		// remains a safe, visible unavailable response and never affects
		// readiness or Legacy sends.
		if s.evaluationRunner != nil {
			if _, runErr := s.evaluationRunner.Run(r.Context(), value.ID); runErr != nil {
				return s.aiErrorEnvelope(runErr)
			}
			if completed, getErr := s.aiRepository.EvaluationRun(r.Context(), value.ID); getErr == nil {
				value = completed
			}
		}
		s.appendAIAudit(r.Context(), r, "ai.evaluation_run.create", "ai_evaluation_run", value.ID, "success", map[string]any{"classifier_release_id": value.ClassifierReleaseID, "case_count": len(value.CaseIDs)})
		return http.StatusAccepted, apiEnvelope{Data: value}
	})
}
func (s *Server) getAIEvaluationRun(w http.ResponseWriter, r *http.Request) {
	if !s.aiAvailable(w) {
		return
	}
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v2/ai/evaluation/runs/"), "/")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "evaluation run id is required")
		return
	}
	value, err := s.aiRepository.EvaluationRun(r.Context(), id)
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	results, _ := s.aiRepository.EvaluationResults(r.Context(), id)
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"run": value, "results": results}})
}
func (s *Server) handleAIEnforceReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiAvailable(w) {
			return
		}
		value, err := s.aiRepository.EnforceReadiness(r.Context())
		if err != nil {
			s.writeAIError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
	})).ServeHTTP(w, r)
}

func (s *Server) handleAISubresource(w http.ResponseWriter, r *http.Request) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v2/ai/")
	if trimmed == r.URL.Path {
		return false
	}
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	for i := range parts {
		if decoded, err := url.PathUnescape(parts[i]); err == nil {
			parts[i] = decoded
		}
	}
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "provider":
		if len(parts) == 2 && parts[1] == "test" {
			s.handleAIProviderTest(w, r)
			return true
		}
	case "releases":
		if len(parts) == 3 && parts[2] == "activate" {
			if r.Method != http.MethodPost {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return true
			}
			s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !s.aiAvailable(w) {
					return
				}
				body, _ := readJSONBody(r, &map[string]any{})
				s.executeDomainCommand(w, r, "ai_release_activate", body, func() (int, apiEnvelope) {
					if err := s.aiRepository.ActivateRelease(r.Context(), parts[1]); err != nil {
						return s.aiErrorEnvelope(err)
					}
					value, err := s.aiRepository.Release(r.Context(), parts[1])
					if err != nil {
						return s.aiErrorEnvelope(err)
					}
					s.appendAIAudit(r.Context(), r, "ai.release.activate", "classifier_release", parts[1], "success", nil)
					return http.StatusOK, apiEnvelope{Data: value}
				})
			})).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !s.aiAvailable(w) {
					return
				}
				value, err := s.aiRepository.Release(r.Context(), parts[1])
				if err != nil {
					s.writeAIError(w, err)
					return
				}
				s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
			})).ServeHTTP(w, r)
			return true
		}
	case "shadow":
		if len(parts) == 3 && parts[1] == "decisions" && r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.getAIDecisionDetail(w, r, parts[2])
			})).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 4 && parts[1] == "decisions" && parts[3] == "review" {
			s.handleAIReviewDecision(w, r, parts[2])
			return true
		}
	case "profiles":
		if len(parts) == 2 {
			s.handleAIProfileDetail(w, r, parts[1])
			return true
		}
	case "policy":
		if len(parts) == 3 {
			s.handleAIPolicy(w, r, parts[1], parts[2])
			return true
		}
	case "evaluation":
		if len(parts) == 3 && parts[1] == "cases" && parts[2] == "export" {
			s.exportAIEvaluationCases(w, r)
			return true
		}
		if len(parts) == 3 && parts[1] == "cases" && parts[2] == "import" {
			s.importAIEvaluationCases(w, r)
			return true
		}
		if len(parts) == 3 && parts[1] == "cases" && parts[2] == "from-observation" {
			if r.Method != http.MethodPost {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return true
			}
			s.RequireMutation(http.HandlerFunc(s.createAIEvaluationCase)).ServeHTTP(w, r)
			return true
		}
		if len(parts) == 3 && parts[1] == "cases" {
			s.handleAIEvaluationCaseDetail(w, r, parts[2])
			return true
		}
		if len(parts) == 3 && parts[1] == "runs" {
			s.RequireAuth(http.HandlerFunc(s.getAIEvaluationRun)).ServeHTTP(w, r)
			return true
		}
	}
	if parts[0] == "provider" || parts[0] == "releases" || parts[0] == "profiles" || parts[0] == "policy" || parts[0] == "evaluation" {
		s.writeError(w, http.StatusNotFound, "not_found", "AI resource not found")
		return true
	}
	return false
}

func (s *Server) handleAIProfileDetail(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.aiAvailable(w) {
				return
			}
			value, err := s.aiRepository.Profile(r.Context(), id)
			if err != nil {
				s.writeAIError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value})
		})).ServeHTTP(w, r)
	case http.MethodPatch, http.MethodPut:
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.aiAvailable(w) {
				return
			}
			var request aiProfileRequest
			body, err := readJSONBody(r, &request)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "profile is invalid")
				return
			}
			s.executeDomainCommand(w, r, "ai_profile_update", body, func() (int, apiEnvelope) {
				value, getErr := s.aiRepository.Profile(r.Context(), id)
				if getErr != nil {
					return s.aiErrorEnvelope(getErr)
				}
				if request.Name != "" {
					value.Name = request.Name
				}
				if request.Description != "" {
					value.Description = request.Description
				}
				if request.DefaultAction != "" {
					value.DefaultAction = request.DefaultAction
				}
				if request.CategoryActions != nil {
					value.CategoryActions = request.CategoryActions
				}
				if request.TagActions != nil {
					value.TagActions = request.TagActions
				}
				if request.Safety != nil {
					value.Safety = request.Safety
				}
				if err := s.aiRepository.SaveProfile(r.Context(), value, s.now(), s.now()); err != nil {
					return s.aiErrorEnvelope(err)
				}
				s.appendAIAudit(r.Context(), r, "ai.profile.update", "ai_profile", id, "success", map[string]any{"builtin": value.Builtin})
				return http.StatusOK, apiEnvelope{Data: value}
			})
		})).ServeHTTP(w, r)
	case http.MethodDelete:
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.aiAvailable(w) {
				return
			}
			body, _ := readJSONBody(r, &map[string]any{})
			s.executeDomainCommand(w, r, "ai_profile_delete", body, func() (int, apiEnvelope) {
				if err := s.aiRepository.DeleteProfile(r.Context(), id); err != nil {
					return s.aiErrorEnvelope(err)
				}
				s.appendAIAudit(r.Context(), r, "ai.profile.delete", "ai_profile", id, "success", nil)
				return http.StatusOK, apiEnvelope{Data: map[string]any{"deleted": true, "id": id}}
			})
		})).ServeHTTP(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) getAIDecisionDetail(w http.ResponseWriter, r *http.Request, id string) {
	if !s.aiAvailable(w) {
		return
	}
	decision, err := s.aiRepository.Decision(r.Context(), id)
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	routes, err := s.aiRepository.RouteEvaluationsForDecision(r.Context(), id)
	if err != nil {
		s.writeAIError(w, err)
		return
	}
	data := map[string]any{
		"decision":          aiDecisionDTOFrom(decision),
		"route_evaluations": routes,
	}
	if event, eventErr := s.aiRepository.NormalizedEvent(r.Context(), decision.NormalizedEventID); eventErr == nil {
		data["event"] = aiNormalizedEventDTOFrom(event)
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: data})
}

func aiDecisionDTOFrom(value classifier.Decision) aiDecisionDTO {
	return aiDecisionDTO{ID: value.ID, NormalizedEventID: value.NormalizedEventID, ClassifierReleaseID: value.ClassifierReleaseID, Status: value.Status, ModeAtSchedule: value.ModeAtSchedule, Classification: value.Classification, SuggestedAction: value.SuggestedAction, EffectiveAction: value.EffectiveAction, HardPassReason: value.HardPassReason, Provider: value.Provider, Model: value.Model, Usage: value.Usage, CostMicros: value.CostMicros, CostCurrency: value.CostCurrency, LatencyMS: value.LatencyMS, ErrorCode: value.ErrorCode, ScheduledAt: value.ScheduledAt.UTC().Format(time.RFC3339Nano), CallStartedAt: optionalTime(value.CallStartedAt), CompletedAt: optionalTime(value.CompletedAt), Reviewed: value.Reviewed, ReviewedAt: optionalTime(value.ReviewedAt), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func aiNormalizedEventDTOFrom(value domain.NormalizedEvent) aiNormalizedEventDTO {
	var sourceEventAt *string
	if value.SourceEventAt != nil {
		stamp := value.SourceEventAt.UTC().Format(time.RFC3339Nano)
		sourceEventAt = &stamp
	}
	return aiNormalizedEventDTO{ID: value.ID, SchemaVersion: value.SchemaVersion, NormalizedEventID: value.NormalizedEventID, ObservedEventID: value.ObservedEventID, Platform: string(value.Platform), SourceID: value.SourceID, ExternalID: value.ExternalID, SourceDisplayName: value.SourceDisplayName, EventType: string(value.EventType), AuthorID: value.AuthorID, AuthorName: value.AuthorName, Title: value.Title, Body: value.Body, RelatedBody: value.RelatedBody, URL: value.URL, PublicURLs: value.PublicURLs, Media: value.Media, SourceEventAt: sourceEventAt, ObservedAt: value.ObservedAt.UTC().Format(time.RFC3339Nano), NormalizerVersion: value.NormalizerVersion, PreprocessorVersion: value.PreprocessorVersion, Truncated: value.Truncated, NormalizationFlags: value.NormalizationFlags, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}
}
func optionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func parseAIDecisionQuery(values url.Values) (platformdb.DecisionQuery, error) {
	query := platformdb.DecisionQuery{Status: strings.TrimSpace(values.Get("status")), Category: strings.TrimSpace(values.Get("category")), Importance: strings.TrimSpace(values.Get("importance")), SuggestedAction: strings.TrimSpace(values.Get("suggested_action")), SourceID: strings.TrimSpace(values.Get("source_id")), ReleaseID: strings.TrimSpace(values.Get("release_id"))}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			return query, platformdb.ErrObservationInvalidQuery
		}
		query.Limit = n
	}
	if raw := strings.TrimSpace(values.Get("cursor")); raw != "" {
		cursor, err := decodeAICursor(raw)
		if err != nil {
			return query, err
		}
		query.Cursor = cursor
	}
	var err error
	if query.From, err = parseAIDate(values.Get("from")); err != nil {
		return query, err
	}
	if query.To, err = parseAIDate(values.Get("to")); err != nil {
		return query, err
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return query, platformdb.ErrObservationInvalidQuery
	}
	return query, nil
}
func parseAIDate(raw string) (*time.Time, error) {
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
func encodeAICursor(value *platformdb.DecisionCursor) string {
	if value == nil || value.ID == "" {
		return ""
	}
	raw, _ := json.Marshal(aiCursorPayload{Version: aiCursorVersion, At: value.CreatedAt, ID: value.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeAICursor(raw string) (*platformdb.DecisionCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) == 0 || len(decoded) > 512 {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	var payload aiCursorPayload
	dec := json.NewDecoder(strings.NewReader(string(decoded)))
	dec.DisallowUnknownFields()
	if dec.Decode(&payload) != nil {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	if payload.Version != aiCursorVersion || payload.At < 0 || payload.ID == "" {
		return nil, platformdb.ErrObservationInvalidQuery
	}
	return &platformdb.DecisionCursor{CreatedAt: payload.At, ID: payload.ID}, nil
}

func (s *Server) writeAIError(w http.ResponseWriter, err error) {
	status, response := s.aiErrorEnvelope(err)
	s.writeJSON(w, status, response)
}
func (s *Server) aiErrorEnvelope(err error) (int, apiEnvelope) {
	status, code, message := aiError(err)
	return status, apiEnvelope{Error: &apiError{Code: code, Message: message}, RequestID: requestID()}
}

// appendAIAudit is the API-owned audit boundary for Phase 4 mutations. Only
// bounded, non-secret metadata is supplied by callers; MigrationRepository
// applies its own recursive redaction and hash-chain transaction as a second
// safety net. Audit failures are intentionally best-effort so an unavailable
// platform audit table cannot alter Legacy delivery semantics.
func (s *Server) appendAIAudit(ctx context.Context, request *http.Request, action, resourceType, resourceID, outcome string, metadata map[string]any) {
	if s == nil || s.migrationRepository == nil {
		return
	}
	principal, _ := PrincipalFromContext(ctx)
	raw, err := json.Marshal(metadata)
	if err != nil {
		raw = []byte(`{}`)
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	entry := platformdb.AuditEntry{
		OccurredAt:   now.Unix(),
		PrincipalID:  principal.AdminID,
		Action:       strings.TrimSpace(action),
		ResourceType: strings.TrimSpace(resourceType),
		ResourceID:   boundedAuditValue(resourceID),
		Outcome:      boundedAuditValue(outcome),
		MetadataJSON: string(raw),
	}
	if request != nil {
		entry.RequestID = boundedAuditValue(request.Header.Get("X-Request-ID"))
		entry.IdempotencyKey = boundedAuditValue(request.Header.Get("Idempotency-Key"))
	}
	_, _ = s.migrationRepository.AppendAudit(ctx, entry)
}

func boundedAuditValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func aiError(err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusOK, "", ""
	case errors.Is(err, policy.ErrEnforceNotAvailable):
		return http.StatusConflict, "enforce_not_available", "ENFORCE is not available in Phase 4"
	case errors.Is(err, platformdb.ErrAIProviderNotFound), errors.Is(err, platformdb.ErrReleaseNotFound), errors.Is(err, platformdb.ErrAIProfileNotFound), errors.Is(err, platformdb.ErrAIPolicyNotFound), errors.Is(err, platformdb.ErrEvaluationNotFound), errors.Is(err, platformdb.ErrDecisionNotFound), errors.Is(err, platformdb.ErrRouteEvaluationNotFound):
		return http.StatusNotFound, "not_found", "AI resource not found"
	case errors.Is(err, platformdb.ErrAIProfileInUse):
		return http.StatusConflict, "profile_in_use", "profile is referenced by a policy"
	case errors.Is(err, platformdb.ErrAIProfileBuiltin):
		return http.StatusConflict, "profile_immutable", "built-in profile cannot be deleted"
	case errors.Is(err, platformdb.ErrAIUnavailable), errors.Is(err, secretstore.ErrSecretStoreUnavailable), errors.Is(err, secretstore.ErrSecretStoreRecovery):
		return http.StatusServiceUnavailable, "ai_unavailable", "AI Shadow is unavailable"
	case errors.Is(err, provider.ErrInvalidConfig):
		return http.StatusBadRequest, "invalid_provider_config", "provider configuration is invalid"
	case errors.Is(err, platformdb.ErrAIProviderInvalid):
		return http.StatusBadRequest, "invalid_provider_config", "provider configuration is invalid"
	case errors.Is(err, platformdb.ErrEvaluationInvalid):
		return http.StatusBadRequest, "invalid_argument", "AI request is invalid"
	case errors.Is(err, provider.ErrUnavailable):
		return http.StatusBadGateway, "provider_unavailable", "provider is unavailable"
	default:
		return http.StatusBadRequest, "invalid_argument", "AI request is invalid"
	}
}
