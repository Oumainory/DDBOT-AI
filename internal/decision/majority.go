package decision

import (
	"context"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// MajorityStrategy runs every configured Decision Provider and accepts the
// majority vote for category and importance. Ties result in Escalated (PASS).
//
// This strategy is useful when you have multiple independent providers and
// want a consensus-based decision. It trades latency for reliability: every
// provider must complete before a decision is made.
type MajorityStrategy struct {
	config StrategyConfig
}

// NewMajorityStrategy creates a majority-vote strategy with the given
// providers. All providers are consulted on every evaluation.
func NewMajorityStrategy(providers ...DecisionProvider) *MajorityStrategy {
	return &MajorityStrategy{
		config: StrategyConfig{
			Mode:               StrategyMajority,
			Providers:          providers,
			AgreementThreshold: 0.5,
		},
	}
}

func (s *MajorityStrategy) Evaluate(ctx context.Context, event domain.NormalizedEvent) (StrategyResult, error) {
	if s == nil || len(s.config.Providers) == 0 {
		return StrategyResult{
			Mode:      StrategyMajority,
			Escalated: true,
		}, ErrNoProvider
	}

	threshold := s.config.AgreementThreshold
	if threshold <= 0 || threshold > 1 {
		threshold = 0.5
	}

	var allEvidence []DecisionEvidence
	categoryVotes := make(map[domain.Category]int)
	importanceVotes := make(map[domain.Importance]int)
	confidentCount := 0

	for _, provider := range s.config.Providers {
		if provider == nil {
			continue
		}

		evidence, err := provider.Evaluate(ctx, event)
		if err != nil {
			evidence.Error = err
			evidence.Uncertain = true
		}
		allEvidence = append(allEvidence, evidence)

		if !evidence.Uncertain && !evidence.InsufficientContext {
			confidentCount++
			categoryVotes[evidence.SemanticResult.Category]++
			importanceVotes[evidence.SemanticResult.Importance]++
		}
	}

	totalProviders := len(allEvidence)
	if totalProviders == 0 {
		return StrategyResult{
			Mode:       StrategyMajority,
			Escalated:  true,
		}, ErrNoEvidence
	}

	// Not enough confident providers to reach the threshold.
	// Uncertain providers are treated as abstentions; they do not count
	// against the majority. The threshold applies to the fraction of
	// confident providers among all providers, but a single confident
	// provider is always sufficient when it is the only one with an opinion.
	if confidentCount == 0 {
		return StrategyResult{
			SemanticDecision: domain.SemanticResult{
				SchemaVersion: 1,
				Category:      domain.CategoryUnknown,
				Importance:    domain.ImportanceLow,
				Confidence:    0,
				Uncertain:     true,
			},
			Evidence:       allEvidence,
			ProviderCount:  confidentCount,
			TotalProviders: totalProviders,
			Agreement:      false,
			Escalated:      true,
			Mode:           StrategyMajority,
		}, nil
	}

	// Find the majority category.
	winningCategory, categoryAgreement := majorityWinner(categoryVotes, confidentCount)
	winningImportance, importanceAgreement := majorityWinner(importanceVotes, confidentCount)

	if !categoryAgreement || !importanceAgreement {
		// Tie or no clear majority: escalate.
		return StrategyResult{
			SemanticDecision: domain.SemanticResult{
				SchemaVersion: 1,
				Category:      domain.CategoryUnknown,
				Importance:    domain.ImportanceLow,
				Confidence:    0,
				Uncertain:     true,
			},
			Evidence:       allEvidence,
			ProviderCount:  confidentCount,
			TotalProviders: totalProviders,
			Agreement:      false,
			Escalated:      true,
			Mode:           StrategyMajority,
		}, nil
	}

	// Compute aggregate confidence as the average of confident providers.
	var totalConfidence float64
	for _, evidence := range allEvidence {
		if !evidence.Uncertain && !evidence.InsufficientContext {
			totalConfidence += evidence.SemanticResult.Confidence
		}
	}
	avgConfidence := totalConfidence / float64(confidentCount)

	// Collect all tags and flags from confident providers.
	tagSet := make(map[string]bool)
	flagSet := make(map[string]bool)
	for _, evidence := range allEvidence {
		if !evidence.Uncertain && !evidence.InsufficientContext {
			for _, tag := range evidence.SemanticResult.Tags {
				tagSet[tag] = true
			}
			for _, flag := range evidence.SemanticResult.Flags {
				flagSet[flag] = true
			}
		}
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	flags := make([]string, 0, len(flagSet))
	for flag := range flagSet {
		flags = append(flags, flag)
	}

	return StrategyResult{
		SemanticDecision: domain.SemanticResult{
			SchemaVersion: 1,
			Category:      winningCategory,
			Importance:    winningImportance,
			Tags:          tags,
			Flags:         flags,
			Confidence:    avgConfidence,
			Summary:       "majority vote",
			Reason:        "majority_vote",
			ReasonCode:    "strategy_majority",
		},
		Evidence:       allEvidence,
		ProviderCount:  confidentCount,
		TotalProviders: totalProviders,
		Agreement:      categoryAgreement && importanceAgreement,
		Escalated:      false,
		Mode:           StrategyMajority,
	}, nil
}

func (s *MajorityStrategy) Mode() StrategyMode {
	if s == nil {
		return StrategyMajority
	}
	return s.config.Mode
}

// majorityWinner returns the key with the most votes and whether it
// constitutes a strict majority (>50% of total).
func majorityWinner[K comparable](votes map[K]int, total int) (K, bool) {
	var winner K
	maxVotes := 0
	for key, count := range votes {
		if count > maxVotes {
			maxVotes = count
			winner = key
		}
	}
	return winner, maxVotes > total/2
}
