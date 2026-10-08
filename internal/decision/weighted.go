package decision

import (
	"context"
	"sort"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// WeightedStrategy runs every configured Decision Provider and combines their
// assessments using configured weights. Providers with higher weights have
// more influence on the final decision.
//
// This strategy is useful when you have providers of varying reliability or
// cost, and want to give more weight to more trusted or more expensive
// providers while still considering cheaper ones.
type WeightedStrategy struct {
	config StrategyConfig
}

// NewWeightedStrategy creates a weighted-vote strategy with the given
// providers and weights. Providers without an entry in the weights map
// default to weight 1.0.
func NewWeightedStrategy(providers []DecisionProvider, weights map[string]float64) *WeightedStrategy {
	if weights == nil {
		weights = make(map[string]float64)
	}
	return &WeightedStrategy{
		config: StrategyConfig{
			Mode:      StrategyWeighted,
			Providers: providers,
			Weights:   weights,
		},
	}
}

func (s *WeightedStrategy) Evaluate(ctx context.Context, event domain.NormalizedEvent) (StrategyResult, error) {
	if s == nil || len(s.config.Providers) == 0 {
		return StrategyResult{
			Mode:      StrategyWeighted,
			Escalated: true,
		}, ErrNoProvider
	}

	var allEvidence []DecisionEvidence
	type weightedVote struct {
		category   domain.Category
		importance domain.Importance
		confidence float64
		weight     float64
		tags       []string
		flags      []string
	}
	var votes []weightedVote
	totalWeight := 0.0
	confidentWeight := 0.0

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

		weight := s.config.Weights[provider.ID()]
		if weight <= 0 {
			weight = 1.0
		}
		totalWeight += weight

		if !evidence.Uncertain && !evidence.InsufficientContext {
			confidentWeight += weight
			votes = append(votes, weightedVote{
				category:   evidence.SemanticResult.Category,
				importance: evidence.SemanticResult.Importance,
				confidence: evidence.SemanticResult.Confidence,
				weight:     weight,
				tags:       evidence.SemanticResult.Tags,
				flags:      evidence.SemanticResult.Flags,
			})
		}
	}

	totalProviders := len(allEvidence)
	if totalProviders == 0 {
		return StrategyResult{
			Mode:       StrategyWeighted,
			Escalated:  true,
		}, ErrNoEvidence
	}

	// Not enough weighted confidence to make a decision.
	minProviders := s.config.MinProviders
	if minProviders <= 0 {
		minProviders = 1
	}
	if len(votes) < minProviders || (totalWeight > 0 && confidentWeight/totalWeight < 0.5) {
		return StrategyResult{
			SemanticDecision: domain.SemanticResult{
				SchemaVersion: 1,
				Category:      domain.CategoryUnknown,
				Importance:    domain.ImportanceLow,
				Confidence:    0,
				Uncertain:     true,
			},
			Evidence:       allEvidence,
			ProviderCount:  len(votes),
			TotalProviders: totalProviders,
			Agreement:      false,
			Escalated:      true,
			Mode:           StrategyWeighted,
		}, nil
	}

	// Weighted category vote.
	categoryScores := make(map[domain.Category]float64)
	importanceScores := make(map[domain.Importance]float64)
	for _, vote := range votes {
		categoryScores[vote.category] += vote.weight
		importanceScores[vote.importance] += vote.weight
	}

	winningCategory := topScoring(categoryScores)
	winningImportance := topScoring(importanceScores)

	// Weighted average confidence.
	var weightedConfidence float64
	for _, vote := range votes {
		weightedConfidence += vote.confidence * vote.weight
	}
	if confidentWeight > 0 {
		weightedConfidence /= confidentWeight
	}

	// Collect tags and flags, weighted by occurrence.
	tagScores := make(map[string]float64)
	flagScores := make(map[string]float64)
	for _, vote := range votes {
		for _, tag := range vote.tags {
			tagScores[tag] += vote.weight
		}
		for _, flag := range vote.flags {
			flagScores[flag] += vote.weight
		}
	}

	// Only include tags/flags that appear in more than half the weighted votes.
	threshold := confidentWeight / 2
	tags := make([]string, 0)
	for tag, score := range tagScores {
		if score > threshold {
			tags = append(tags, tag)
		}
	}
	flags := make([]string, 0)
	for flag, score := range flagScores {
		if score > threshold {
			flags = append(flags, flag)
		}
	}

	return StrategyResult{
		SemanticDecision: domain.SemanticResult{
			SchemaVersion: 1,
			Category:      winningCategory,
			Importance:    winningImportance,
			Tags:          tags,
			Flags:         flags,
			Confidence:    weightedConfidence,
			Summary:       "weighted vote",
			Reason:        "weighted_vote",
			ReasonCode:    "strategy_weighted",
		},
		Evidence:       allEvidence,
		ProviderCount:  len(votes),
		TotalProviders: totalProviders,
		Agreement:      len(votes) == totalProviders,
		Escalated:      false,
		Mode:           StrategyWeighted,
	}, nil
}

func (s *WeightedStrategy) Mode() StrategyMode {
	if s == nil {
		return StrategyWeighted
	}
	return s.config.Mode
}

// topScoring returns the key with the highest score.
func topScoring[K comparable](scores map[K]float64) K {
	type entry struct {
		key   K
		score float64
	}
	entries := make([]entry, 0, len(scores))
	for key, score := range scores {
		entries = append(entries, entry{key, score})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})
	if len(entries) > 0 {
		return entries[0].key
	}
	var zero K
	return zero
}
