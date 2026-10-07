package decision

import (
	"context"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// NoneProvider is the first-class "no AI" Decision Provider. It represents
// the intentional product state where no semantic assessment is performed.
// This is not a degraded mode, not a configuration error, and not a provider
// failure. It is a valid, supported, and recommended operating mode.
//
// NoneProvider always returns Uncertain evidence with a neutral
// SemanticResult. The Strategy layer treats this as "no evidence" and
// the Policy layer defaults to PASS.
type NoneProvider struct {
	id string
}

// NewNoneProvider creates a NoneProvider with the given stable identifier.
func NewNoneProvider(id string) *NoneProvider {
	if id == "" {
		id = "none-default"
	}
	return &NoneProvider{id: id}
}

func (p *NoneProvider) Evaluate(_ context.Context, _ domain.NormalizedEvent) (DecisionEvidence, error) {
	return DecisionEvidence{
		ProviderType:        ProviderNone,
		ProviderID:          p.id,
		SemanticResult: domain.SemanticResult{
			SchemaVersion: 1,
			Category:      domain.CategoryUnknown,
			Importance:    domain.ImportanceLow,
			Confidence:    0,
			Uncertain:     true,
		},
		ProviderConfidence:  0,
		Uncertain:           true,
		InsufficientContext: true,
	}, nil
}

func (p *NoneProvider) Type() ProviderType { return ProviderNone }

func (p *NoneProvider) ID() string { return p.id }

func (p *NoneProvider) HealthCheck(_ context.Context) error { return nil }
