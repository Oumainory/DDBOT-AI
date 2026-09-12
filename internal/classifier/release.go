// Package classifier contains semantic classifier release identity and
// accounting contracts. Pricing is intentionally separate from release
// identity so a price change does not reset Enforce eligibility.
package classifier

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrInvalidBaseURL = errors.New("classifier: invalid base URL")

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
	if err := ValidateBaseURL(s.BaseURL); err != nil {
		return err
	}
	return nil
}

func Fingerprint(spec ReleaseSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	spec.BaseURL = CanonicalBaseURL(spec.BaseURL)
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func CanonicalBaseURL(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return strings.TrimRight(value, "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.Fragment = ""
	// Query order is not semantically significant for an HTTP endpoint. Keep
	// ordinary query parameters, but canonicalize their escaped ordering so
	// equivalent provider URLs receive the same release identity.
	parsed.RawQuery = parsed.Query().Encode()
	return strings.TrimRight(parsed.String(), "/")
}

// ValidateBaseURL accepts public, private, and localhost HTTP(S) endpoints
// while rejecting URL components that are commonly used to smuggle a
// credential into configuration or logs. Authentication belongs in Secret
// Store; a provider URL must not contain userinfo or bearer-like query keys.
func ValidateBaseURL(value string) error {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return ErrInvalidBaseURL
	}
	for key := range parsed.Query() {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key") || strings.Contains(lower, "auth") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || lower == "sig" || strings.Contains(lower, "signature") {
			return ErrInvalidBaseURL
		}
	}
	return nil
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
