package decision

import (
	"context"
	"errors"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// StrategyMode defines how multiple Decision Providers are combined.
type StrategyMode string

const (
	// StrategyNone means no providers are consulted. The event passes
	// through without any semantic assessment.
	StrategyNone StrategyMode = "none"

	// StrategyFirstAvailable runs providers in order and stops at the
	// first one that returns non-Uncertain evidence. This is the
	// classic cascade: Rules → Local → Jev → LLM.
	StrategyFirstAvailable StrategyMode = "first_available"

	// StrategyAll runs every configured provider and requires agreement
	// (all providers return the same category and importance).
	// Disagreement escalates to human review or PASS.
	StrategyAll StrategyMode = "all"

	// StrategyMajority runs every configured provider and accepts the
	// majority vote for category and importance. Ties PASS.
	StrategyMajority StrategyMode = "majority"

	// StrategyWeighted runs every configured provider and combines
	// their assessments using configured weights.
	StrategyWeighted StrategyMode = "weighted"
)

// StrategyConfig configures a Decision Strategy.
type StrategyConfig struct {
	// Mode is the combination strategy.
	Mode StrategyMode `json:"mode"`

	// Providers is the ordered list of providers to consult.
	// For StrategyFirstAvailable, order matters.
	Providers []DecisionProvider `json:"-"`

	// Weights maps provider IDs to their voting weight.
	// Only used by StrategyWeighted.
	Weights map[string]float64 `json:"weights,omitempty"`

	// MinProviders is the minimum number of providers that must
	// return non-Uncertain evidence before a decision is made.
	// Defaults to 1.
	MinProviders int `json:"min_providers,omitempty"`

	// AgreementThreshold is the fraction of providers that must
	// agree for StrategyAll and StrategyMajority. Defaults to 1.0
	// for StrategyAll and 0.5 for StrategyMajority.
	AgreementThreshold float64 `json:"agreement_threshold,omitempty"`
}

// StrategyResult is the output of a Decision Strategy execution.
type StrategyResult struct {
	// SemanticDecision is the final aggregated semantic assessment.
	SemanticDecision domain.SemanticResult `json:"semantic_decision"`

	// Evidence contains all individual provider assessments.
	Evidence []DecisionEvidence `json:"evidence"`

	// ProviderCount is the number of providers that returned
	// non-Uncertain evidence.
	ProviderCount int `json:"provider_count"`

	// TotalProviders is the total number of providers consulted.
	TotalProviders int `json:"total_providers"`

	// Agreement is true when all consulted providers agree on
	// category and importance.
	Agreement bool `json:"agreement"`

	// Escalated is true when the strategy could not reach a
	// decision and recommends human review or a higher-tier provider.
	Escalated bool `json:"escalated"`

	// Mode is the strategy mode that produced this result.
	Mode StrategyMode `json:"mode"`
}

// DecisionStrategy is the interface for combining multiple Decision
// Providers into a single Semantic Decision. The Strategy owns the
// orchestration; individual providers only produce evidence.
type DecisionStrategy interface {
	// Evaluate runs the configured providers according to the strategy
	// mode and returns an aggregated StrategyResult.
	Evaluate(ctx context.Context, event domain.NormalizedEvent) (StrategyResult, error)

	// Mode returns the strategy mode.
	Mode() StrategyMode
}

// Common sentinel errors for Decision Strategies.
var (
	ErrStrategyUnavailable = errors.New("decision: strategy unavailable")
	ErrNoEvidence          = errors.New("decision: no evidence produced")
	ErrInsufficientEvidence = errors.New("decision: insufficient evidence for decision")
)
