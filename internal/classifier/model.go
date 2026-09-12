package classifier

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidUsage = errors.New("classifier: invalid usage")

type Release struct {
	ID                          string    `json:"id"`
	Fingerprint                 string    `json:"fingerprint"`
	ProviderType                string    `json:"provider_type"`
	BaseURL                     string    `json:"base_url"`
	Model                       string    `json:"model"`
	PromptVersion               string    `json:"prompt_version"`
	PromptDigest                string    `json:"prompt_digest"`
	SchemaVersion               string    `json:"schema_version"`
	SchemaDigest                string    `json:"schema_digest"`
	StructuredOutputMode        string    `json:"structured_output_mode"`
	PreprocessorVersion         string    `json:"preprocessor_version"`
	NormalizerVersion           string    `json:"normalizer_version"`
	PricingCurrency             string    `json:"pricing_currency"`
	InputPriceMicrosPerMillion  int64     `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion int64     `json:"output_price_micros_per_million"`
	RequestPriceMicros          int64     `json:"request_price_micros"`
	Active                      bool      `json:"active"`
	CreatedAt                   time.Time `json:"created_at"`
}

func (r Release) Validate() error {
	for name, value := range map[string]string{
		"id": r.ID, "fingerprint": r.Fingerprint, "provider_type": r.ProviderType,
		"base_url": r.BaseURL, "model": r.Model, "prompt_version": r.PromptVersion,
		"prompt_digest": r.PromptDigest, "schema_version": r.SchemaVersion,
		"schema_digest": r.SchemaDigest, "structured_output_mode": r.StructuredOutputMode,
		"preprocessor_version": r.PreprocessorVersion, "normalizer_version": r.NormalizerVersion,
	} {
		if strings.TrimSpace(value) == "" {
			return errors.New("classifier: missing release " + name)
		}
	}
	if r.ProviderType != "openai-compatible" || (r.StructuredOutputMode != "json_schema" && r.StructuredOutputMode != "json_object" && r.StructuredOutputMode != "json-schema") {
		return errors.New("classifier: unsupported release provider or output mode")
	}
	if err := ValidateBaseURL(r.BaseURL); err != nil {
		return err
	}
	if r.InputPriceMicrosPerMillion < 0 || r.OutputPriceMicrosPerMillion < 0 || r.RequestPriceMicros < 0 {
		return errors.New("classifier: release pricing cannot be negative")
	}
	if r.CreatedAt.IsZero() {
		return errors.New("classifier: release created_at is required")
	}
	return nil
}

type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	TotalTokens  *int64 `json:"total_tokens,omitempty"`
}

// Validate rejects provider accounting values that could underflow a cost
// calculation or produce misleading Dashboard totals. Providers may omit any
// field; when present, each value must be a non-negative integer. We do not
// require TotalTokens to equal input+output because compatible providers may
// include cached/system tokens in their reported total.
func (u Usage) Validate() error {
	for _, value := range []*int64{u.InputTokens, u.OutputTokens, u.TotalTokens} {
		if value != nil && *value < 0 {
			return ErrInvalidUsage
		}
	}
	return nil
}

func (u Usage) Total() *int64 {
	if u.TotalTokens != nil {
		value := *u.TotalTokens
		return &value
	}
	if u.InputTokens == nil || u.OutputTokens == nil {
		return nil
	}
	value := *u.InputTokens + *u.OutputTokens
	return &value
}

type DecisionStatus string

const (
	StatusQueued              DecisionStatus = "queued"
	StatusRunning             DecisionStatus = "running"
	StatusCompleted           DecisionStatus = "completed"
	StatusProviderError       DecisionStatus = "provider_error"
	StatusParseError          DecisionStatus = "parse_error"
	StatusTimeout             DecisionStatus = "timeout"
	StatusQueueDropped        DecisionStatus = "queue_dropped"
	StatusSkipped             DecisionStatus = "skipped"
	StatusProviderUnavailable DecisionStatus = "provider_unavailable"
	StatusUncertainCall       DecisionStatus = "uncertain_call"
)

type Decision struct {
	ID                  string         `json:"id"`
	NormalizedEventID   string         `json:"normalized_event_id"`
	ClassifierReleaseID string         `json:"classifier_release_id"`
	Status              DecisionStatus `json:"status"`
	ModeAtSchedule      string         `json:"mode_at_schedule"`
	Classification      Classification `json:"classification"`
	SuggestedAction     string         `json:"suggested_action"`
	EffectiveAction     string         `json:"effective_action"`
	HardPassReason      string         `json:"hard_pass_reason,omitempty"`
	Provider            string         `json:"provider,omitempty"`
	Model               string         `json:"model,omitempty"`
	Usage               Usage          `json:"usage"`
	CostMicros          *int64         `json:"cost_micros,omitempty"`
	CostCurrency        string         `json:"cost_currency,omitempty"`
	LatencyMS           *int64         `json:"latency_ms,omitempty"`
	ErrorCode           string         `json:"error_code,omitempty"`
	ScheduledAt         time.Time      `json:"scheduled_at"`
	CallStartedAt       *time.Time     `json:"call_started_at,omitempty"`
	CompletedAt         *time.Time     `json:"completed_at,omitempty"`
	Reviewed            bool          `json:"reviewed"`
	ReviewedAt          *time.Time    `json:"reviewed_at,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
}
