package decision

import (
	"context"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// FirstAvailableStrategy runs Decision Providers in order and stops at the
// first one that returns non-Uncertain evidence. This is the classic cascade
// pattern: Rules → Local → Jev → LLM.
//
// If no provider returns non-Uncertain evidence, the strategy returns an
// Escalated result with the last provider's evidence. The caller (Policy
// layer) should treat Escalated as PASS.
type FirstAvailableStrategy struct {
	config StrategyConfig
}

// NewFirstAvailableStrategy creates a cascade strategy with the given
// providers. Providers are consulted in the order they appear.
func NewFirstAvailableStrategy(providers ...DecisionProvider) *FirstAvailableStrategy {
	return &FirstAvailableStrategy{
		config: StrategyConfig{
			Mode:      StrategyFirstAvailable,
			Providers: providers,
		},
	}
}

func (s *FirstAvailableStrategy) Evaluate(ctx context.Context, event domain.NormalizedEvent) (StrategyResult, error) {
	if s == nil || len(s.config.Providers) == 0 {
		return StrategyResult{
			Mode:       StrategyFirstAvailable,
			Escalated:  true,
		}, ErrNoProvider
	}

	var allEvidence []DecisionEvidence
	for _, provider := range s.config.Providers {
		if provider == nil {
			continue
		}

		evidence, err := provider.Evaluate(ctx, event)
		if err != nil {
			// Provider error: record uncertain evidence and continue
			// to the next provider in the cascade.
			evidence.Error = err
			evidence.Uncertain = true
		}
		allEvidence = append(allEvidence, evidence)

		if !evidence.Uncertain && !evidence.InsufficientContext {
			// Found a confident provider. Stop the cascade.
			return StrategyResult{
				SemanticDecision: evidence.SemanticResult,
				Evidence:         allEvidence,
				ProviderCount:    1,
				TotalProviders:   len(allEvidence),
				Agreement:        true,
				Escalated:        false,
				Mode:             StrategyFirstAvailable,
			}, nil
		}
	}

	// No provider returned confident evidence. Escalate.
	return StrategyResult{
		SemanticDecision: domain.SemanticResult{
			SchemaVersion: 1,
			Category:      domain.CategoryUnknown,
			Importance:    domain.ImportanceLow,
			Confidence:    0,
			Uncertain:     true,
		},
		Evidence:       allEvidence,
		ProviderCount:  0,
		TotalProviders: len(allEvidence),
		Agreement:      false,
		Escalated:      true,
		Mode:           StrategyFirstAvailable,
	}, nil
}

func (s *FirstAvailableStrategy) Mode() StrategyMode {
	if s == nil {
		return StrategyFirstAvailable
	}
	return s.config.Mode
}
