package classifier

import (
	"testing"
	"time"
)

func testReleaseSpec() ReleaseSpec {
	return ReleaseSpec{
		ProviderType:         "openai-compatible",
		BaseURL:              "https://llm.example.test/v1",
		Model:                "classifier-model",
		Prompt:               "classify content",
		SchemaVersion:        "semantic-result-v1",
		StructuredOutputMode: "json-schema",
		PreprocessorVersion:  "text-v1",
		NormalizerVersion:    "bilibili-v1",
	}
}

func TestPricingChangesDoNotChangeReleaseFingerprint(t *testing.T) {
	spec := testReleaseSpec()
	first, err := Fingerprint(spec)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	oldPrice := PricingRevision{
		ID:               "price_1",
		Currency:         "CNY",
		InputPerMillion:  "2",
		OutputPerMillion: "4",
		EffectiveAt:      time.Unix(1700000000, 0),
	}
	newPrice := oldPrice
	newPrice.ID = "price_2"
	newPrice.InputPerMillion = "1.8"
	newPrice.OutputPerMillion = "3.6"
	newPrice.EffectiveAt = time.Unix(1800000000, 0)
	if err := oldPrice.Validate(); err != nil {
		t.Fatalf("old price validation error = %v", err)
	}
	if err := newPrice.Validate(); err != nil {
		t.Fatalf("new price validation error = %v", err)
	}

	second, err := Fingerprint(spec)
	if err != nil {
		t.Fatalf("Fingerprint() after pricing revision error = %v", err)
	}
	if first != second {
		t.Fatalf("release fingerprint changed after pricing-only revision: %q != %q", first, second)
	}
}

func TestSemanticReleaseChangesFingerprint(t *testing.T) {
	first, err := Fingerprint(testReleaseSpec())
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	changed := testReleaseSpec()
	changed.Prompt = "classify content with boundary examples"
	second, err := Fingerprint(changed)
	if err != nil {
		t.Fatalf("Fingerprint() changed spec error = %v", err)
	}
	if first == second {
		t.Fatal("semantic release change retained the same fingerprint")
	}
}
