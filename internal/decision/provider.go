// Package decision defines the pluggable Semantic Decision Layer for DDBOT-AI.
//
// Decision Providers produce Decision Evidence (semantic signals about an
// event). A Decision Strategy aggregates evidence from one or more providers
// into a final Semantic Decision. The Policy layer consumes only the final
// Semantic Decision and never knows which provider produced it.
//
// This package is the Phase 6+ foundation that realises the project's ultimate
// goal: a system where Rules, Local Classifiers, Jev, LLM, and future models
// are all interchangeable Decision Providers behind a unified contract.
package decision

import (
	"context"
	"errors"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// ProviderType identifies the kind of Decision Provider.
type ProviderType string

const (
	// ProviderNone means no Decision Provider is configured. This is a
	// first-class product state, not a degraded or error condition.
	ProviderNone ProviderType = "none"

	// ProviderRules is a deterministic rule-based provider that requires no
	// external API, model, or network access.
	ProviderRules ProviderType = "rules"

	// ProviderLocal is a local classifier (ONNX, small model, etc.) that runs
	// in-process without an external API.
	ProviderLocal ProviderType = "local"

	// ProviderJev is a remote Decision Model specialised in classification,
	// judgement, and scoring.
	ProviderJev ProviderType = "jev"

	// ProviderLLM is an OpenAI-compatible large language model provider.
	ProviderLLM ProviderType = "llm"
)

// DecisionEvidence is the structured output produced by a single Decision
// Provider for one NormalizedEvent. It carries the provider's semantic
// assessment plus metadata that the Strategy layer uses to combine multiple
// evidence sources.
type DecisionEvidence struct {
	// ProviderType identifies which kind of provider produced this evidence.
	ProviderType ProviderType `json:"provider_type"`

	// ProviderID is a stable identifier for the specific provider instance
	// (e.g. a release ID, a model fingerprint, or a ruleset version).
	ProviderID string `json:"provider_id"`

	// SemanticResult is the provider's classification of the event.
	// It must pass domain.SemanticResult.Validate().
	SemanticResult domain.SemanticResult `json:"semantic_result"`

	// Confidence is the provider's self-reported confidence in this
	// assessment, in [0,1]. This is separate from the SemanticResult's
	// own Confidence field and represents the provider's meta-confidence.
	ProviderConfidence float64 `json:"provider_confidence"`

	// Uncertain means the provider explicitly signals that it cannot make
	// a reliable assessment. The Strategy layer may escalate to the next
	// provider in the pipeline.
	Uncertain bool `json:"uncertain"`

	// InsufficientContext means the provider had too little information
	// to produce a meaningful assessment.
	InsufficientContext bool `json:"insufficient_context"`

	// Error is a non-nil error that occurred during evidence production.
	// A provider that returns an error should still return a valid
	// DecisionEvidence with Uncertain=true so the Strategy can decide
	// whether to escalate or PASS.
	Error error `json:"-"`

	// CostMicros is the optional cost of producing this evidence,
	// in micro-units of the pricing currency.
	CostMicros int64 `json:"cost_micros,omitempty"`

	// LatencyMS is the optional wall-clock latency of the provider call.
	LatencyMS int64 `json:"latency_ms,omitempty"`
}

// DecisionProvider is the unified interface for all semantic evidence
// producers. Every provider—whether rules, local model, Jev, LLM, or
// future technology—implements this single contract.
//
// A provider must never directly decide PASS/DROP or where to send a
// message. It only describes what the event is. The Policy layer owns
// the final routing decision.
type DecisionProvider interface {
	// Evaluate produces DecisionEvidence for a single NormalizedEvent.
	// It must return a valid DecisionEvidence even when an error occurs;
	// in that case Uncertain should be true and SemanticResult should
	// still pass Validate().
	Evaluate(ctx context.Context, event domain.NormalizedEvent) (DecisionEvidence, error)

	// Type returns the ProviderType of this provider.
	Type() ProviderType

	// ID returns a stable identifier for this provider instance.
	ID() string

	// HealthCheck returns nil if the provider is operational. A non-nil
	// error means the provider is currently unavailable and the Strategy
	// should treat it as if it returned Uncertain evidence.
	HealthCheck(ctx context.Context) error
}

// Common sentinel errors for Decision Providers.
var (
	ErrProviderUnavailable = errors.New("decision: provider unavailable")
	ErrEvidenceInvalid     = errors.New("decision: evidence failed validation")
	ErrNoProvider          = errors.New("decision: no provider configured")
)
