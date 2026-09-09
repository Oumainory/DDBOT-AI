// Package classifier contains semantic classifier release identity and
// accounting contracts. Pricing is intentionally separate from release
// identity so a price change does not reset Enforce eligibility.
package classifier

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type ReleaseSpec struct {
	ProviderType         string `json:"provider_type"`
	BaseURL              string `json:"base_url"`
	Model                string `json:"model"`
	Prompt               string `json:"prompt"`
	SchemaVersion        string `json:"schema_version"`
	StructuredOutputMode string `json:"structured_output_mode"`
	PreprocessorVersion  string `json:"preprocessor_version"`
	NormalizerVersion    string `json:"normalizer_version"`
}

func (s ReleaseSpec) Validate() error {
	for name, value := range map[string]string{
		"provider_type":          s.ProviderType,
		"base_url":               s.BaseURL,
		"model":                  s.Model,
		"prompt":                 s.Prompt,
		"schema_version":         s.SchemaVersion,
		"structured_output_mode": s.StructuredOutputMode,
		"preprocessor_version":   s.PreprocessorVersion,
		"normalizer_version":     s.NormalizerVersion,
	} {
		if strings.TrimSpace(value) == "" {
			return errors.New("classifier: missing " + name)
		}
	}
	return nil
}

func Fingerprint(spec ReleaseSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

type PricingRevision struct {
	ID                string    `json:"id"`
	Currency          string    `json:"currency"`
	InputPerMillion   string    `json:"input_per_million"`
	OutputPerMillion  string    `json:"output_per_million"`
	EffectiveAt       time.Time `json:"effective_at"`
	ProviderReference string    `json:"provider_reference,omitempty"`
}

func (p PricingRevision) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Currency) == "" {
		return errors.New("classifier: pricing revision id and currency are required")
	}
	if p.EffectiveAt.IsZero() {
		return errors.New("classifier: pricing revision effective_at is required")
	}
	return nil
}
