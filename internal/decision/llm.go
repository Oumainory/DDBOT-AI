package decision

import (
	"context"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
)

// LLMProvider adapts an existing provider.Provider (OpenAI-compatible LLM) into
// the DecisionProvider interface. This is the bridge between the Phase 4/5
// architecture and the Phase 6+ pluggable Decision Provider model.
//
// LLMProvider delegates Classify to the underlying provider and wraps the
// result in DecisionEvidence. It never makes PASS/DROP decisions; it only
// describes what the event is.
type LLMProvider struct {
	id       string
	provider provider.Provider
}

// NewLLMProvider creates an LLMProvider that wraps an existing Provider.
// The id should be a stable identifier, typically the classifier release ID.
func NewLLMProvider(id string, p provider.Provider) *LLMProvider {
	if id == "" {
		id = "llm-default"
	}
	return &LLMProvider{id: id, provider: p}
}

func (p *LLMProvider) Evaluate(ctx context.Context, event domain.NormalizedEvent) (DecisionEvidence, error) {
	if p == nil || p.provider == nil {
		return uncertainEvidence(ProviderLLM, p.id), provider.ErrUnavailable
	}

	classification, usage, err := p.provider.Classify(ctx, event)
	if err != nil {
		// Provider error: return uncertain evidence so the Strategy
		// can escalate to the next provider or PASS.
		evidence := uncertainEvidence(ProviderLLM, p.id)
		evidence.Error = err
		return evidence, err
	}

	// Map classifier.Classification to domain.SemanticResult.
	// The Classification struct mirrors SemanticResult fields; this is
	// a straightforward field mapping.
	result := domain.SemanticResult{
		SchemaVersion: classification.SchemaVersion,
		Category:      classification.Category,
		Importance:    classification.Importance,
		Tags:          classification.Tags,
		Flags:         classification.Flags,
		Confidence:    classification.Confidence,
		Uncertain:     classification.Uncertain,
		Summary:       classification.Summary,
		Reason:        classification.Reason,
		ReasonCode:    classification.ReasonCode,
	}

	// Compute cost from usage if available.
	var costMicros int64
	if usage.TotalTokens != nil {
		// Approximate cost: this is a placeholder; real cost calculation
		// belongs in the provider layer with pricing data.
		_ = usage.TotalTokens
	}

	return DecisionEvidence{
		ProviderType:        ProviderLLM,
		ProviderID:          p.id,
		SemanticResult:      result,
		ProviderConfidence:  classification.Confidence,
		Uncertain:           classification.Uncertain,
		InsufficientContext: classification.InsufficientContext,
		CostMicros:          costMicros,
	}, nil
}

func (p *LLMProvider) Type() ProviderType { return ProviderLLM }

func (p *LLMProvider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *LLMProvider) HealthCheck(ctx context.Context) error {
	if p == nil || p.provider == nil {
		return ErrProviderUnavailable
	}
	_, err := p.provider.TestConnection(ctx)
	return err
}

// Ensure LLMProvider implements DecisionProvider.
var _ DecisionProvider = (*LLMProvider)(nil)
