// Package provider implements the small OpenAI-compatible Chat Completions
// boundary used by Phase 4. It performs one non-streaming request and never
// retries; the Shadow runtime owns at-most-once scheduling.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

const (
	DefaultTimeout     = 15 * time.Second
	DefaultMaxResponse = 1024 * 1024
	DefaultMaxRequest  = 256 * 1024
)

var (
	ErrInvalidConfig         = errors.New("provider: invalid configuration")
	ErrUnavailable           = errors.New("provider: unavailable")
	ErrHTTPStatus            = errors.New("provider: http error")
	ErrResponseTooLarge      = errors.New("provider: response too large")
	ErrInvalidResponse       = errors.New("provider: invalid response")
	ErrParse                 = errors.New("provider: classification parse error")
	ErrStructuredUnsupported = errors.New("provider: structured output unsupported")
)

type Config struct {
	BaseURL              string
	Model                string
	APIKey               string
	StructuredOutputMode string
	RequestTimeout       time.Duration
	MaxResponseBytes     int64
	MaxRequestBytes      int64
	HTTPClient           *http.Client
}

type Usage = classifier.Usage

type Provider interface {
	Classify(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error)
	TestConnection(context.Context) (TestResult, error)
}

type TestResult struct {
	Reachable                 bool   `json:"reachable"`
	ModelAccepted             bool   `json:"model_accepted"`
	StructuredOutputSupported bool   `json:"structured_output_supported"`
	LatencyMS                 int64  `json:"latency_ms"`
	ErrorCode                 string `json:"error_code,omitempty"`
}

type OpenAICompatible struct {
	config Config
	client *http.Client
}

func New(config Config) (*OpenAICompatible, error) {
	if err := classifier.ValidateBaseURL(config.BaseURL); err != nil {
		return nil, ErrInvalidConfig
	}
	config.BaseURL = classifier.CanonicalBaseURL(config.BaseURL)
	config.Model = strings.TrimSpace(config.Model)
	config.StructuredOutputMode = strings.ToLower(strings.TrimSpace(config.StructuredOutputMode))
	if config.StructuredOutputMode == "json-schema" {
		config.StructuredOutputMode = "json_schema"
	}
	if config.StructuredOutputMode == "" {
		config.StructuredOutputMode = "json_schema"
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = DefaultTimeout
	}
	if config.MaxResponseBytes <= 0 || config.MaxResponseBytes > 16*1024*1024 {
		config.MaxResponseBytes = DefaultMaxResponse
	}
	if config.MaxRequestBytes <= 0 || config.MaxRequestBytes > 4*1024*1024 {
		config.MaxRequestBytes = DefaultMaxRequest
	}
	if !validBaseURL(config.BaseURL) || config.Model == "" || (config.StructuredOutputMode != "json_schema" && config.StructuredOutputMode != "json_object") {
		return nil, ErrInvalidConfig
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &OpenAICompatible{config: config, client: client}, nil
}

func (p *OpenAICompatible) Classify(ctx context.Context, event domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
	if p == nil {
		return classifier.Classification{}, classifier.Usage{}, ErrUnavailable
	}
	if err := event.Validate(); err != nil {
		return classifier.Classification{}, classifier.Usage{}, ErrInvalidResponse
	}
	requestBody, err := p.requestBody(event)
	if err != nil {
		return classifier.Classification{}, classifier.Usage{}, err
	}
	started := time.Now()
	response, err := p.do(ctx, requestBody)
	if err != nil {
		return classifier.Classification{}, classifier.Usage{}, err
	}
	_ = started // latency is measured by the runtime so provider stays pure.
	var envelope chatResponse
	if err := json.Unmarshal(response, &envelope); err != nil || len(envelope.Choices) == 0 {
		return classifier.Classification{}, usageFrom(envelope.Usage), ErrInvalidResponse
	}
	usage := usageFrom(envelope.Usage)
	if err := usage.Validate(); err != nil {
		return classifier.Classification{}, usage, ErrInvalidResponse
	}
	content := envelope.Choices[0].Message.Content
	result, err := classifier.ParseClassification([]byte(content))
	if err != nil {
		return classifier.Classification{}, usage, fmt.Errorf("%w: %v", ErrParse, err)
	}
	return result, usage, nil
}

func (p *OpenAICompatible) TestConnection(ctx context.Context) (TestResult, error) {
	start := time.Now()
	result := TestResult{}
	probe := domain.NormalizedEvent{ID: "probe", NormalizedEventID: "probe", SchemaVersion: 1, Platform: domain.PlatformBilibili, SourceID: "probe", ExternalID: "probe", EventType: domain.EventGeneric, Body: "Return a low-importance unknown classification.", NormalizerVersion: "probe", CreatedAt: time.Now().UTC(), ReplayPayload: json.RawMessage(`{"probe":true}`)}
	_, _, err := p.Classify(ctx, probe)
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		result.ErrorCode = StableErrorCode(err)
		return result, err
	}
	result.Reachable = true
	result.ModelAccepted = true
	result.StructuredOutputSupported = true
	return result, nil
}

func (p *OpenAICompatible) requestBody(event domain.NormalizedEvent) ([]byte, error) {
	// Only allowlisted normalized fields enter the prompt. The replay payload is
	// deliberately omitted even though it is public, preventing accidental raw
	// source expansion in a future event implementation.
	input := struct {
		SchemaVersion      int      `json:"schema_version"`
		Platform           string   `json:"platform"`
		SourceID           string   `json:"source_id"`
		ExternalID         string   `json:"external_id"`
		EventType          string   `json:"event_type"`
		AuthorName         string   `json:"author_name,omitempty"`
		Title              string   `json:"title,omitempty"`
		Body               string   `json:"body,omitempty"`
		RelatedBody        string   `json:"related_body,omitempty"`
		URL                string   `json:"url,omitempty"`
		PublicURLs         []string `json:"public_urls,omitempty"`
		Truncated          bool     `json:"truncated"`
		NormalizationFlags []string `json:"normalization_flags,omitempty"`
	}{event.SchemaVersion, string(event.Platform), event.SourceID, event.ExternalID, string(event.EventType), event.AuthorName, event.Title, event.Body, event.RelatedBody, event.URL, event.PublicURLs, event.Truncated, event.NormalizationFlags}
	content, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	body := chatRequest{
		Model:          p.config.Model,
		Stream:         false,
		Messages:       []message{{Role: "system", Content: classifier.BuiltInPrompt}, {Role: "user", Content: string(content)}},
		ResponseFormat: responseFormat{Type: p.config.StructuredOutputMode},
	}
	if p.config.StructuredOutputMode == "json_schema" {
		body.ResponseFormat.JSONSchema = &jsonSchema{Name: "semantic_result_v1", Strict: true, Schema: map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"schema_version", "category", "importance", "tags", "flags", "confidence", "uncertain", "insufficient_context", "prompt_injection_suspected"},
			"properties": map[string]any{
				"schema_version": map[string]any{"type": "integer"}, "category": map[string]any{"type": "string"}, "importance": map[string]any{"type": "string"},
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 32}, "flags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 32},
				"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1}, "uncertain": map[string]any{"type": "boolean"}, "insufficient_context": map[string]any{"type": "boolean"}, "prompt_injection_suspected": map[string]any{"type": "boolean"},
				"summary": map[string]any{"type": "string", "maxLength": 240}, "reason": map[string]any{"type": "string", "maxLength": 240}, "reason_code": map[string]any{"type": "string", "maxLength": 64},
			},
		}}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > p.config.MaxRequestBytes {
		return nil, ErrInvalidResponse
	}
	return encoded, nil
}

func (p *OpenAICompatible) do(ctx context.Context, payload []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.RequestTimeout)
	defer cancel()
	endpoint := strings.TrimRight(p.config.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrInvalidConfig
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(p.config.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, p.config.MaxResponseBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, ErrUnavailable
	}
	if int64(len(body)) > p.config.MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status_%d", ErrHTTPStatus, resp.StatusCode)
	}
	return body, nil
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []message      `json:"messages"`
	Stream         bool           `json:"stream"`
	ResponseFormat responseFormat `json:"response_format"`
}
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}
type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}
type chatResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Usage responseUsage `json:"usage"`
}
type responseUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}

func usageFrom(value responseUsage) classifier.Usage {
	return classifier.Usage{InputTokens: value.PromptTokens, OutputTokens: value.CompletionTokens, TotalTokens: value.TotalTokens}
}

func validBaseURL(value string) bool {
	return classifier.ValidateBaseURL(value) == nil
}

func StableErrorCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrResponseTooLarge):
		return "response_too_large"
	case errors.Is(err, ErrHTTPStatus):
		return "provider_http_error"
	case errors.Is(err, ErrInvalidResponse):
		return "invalid_response"
	case errors.Is(err, ErrParse):
		return "parse_error"
	case errors.Is(err, ErrInvalidConfig):
		return "invalid_config"
	default:
		return "provider_unavailable"
	}
}
