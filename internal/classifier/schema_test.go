package classifier

import (
	"strings"
	"testing"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

func validClassificationJSON(category string) string {
	return `{"schema_version":1,"category":"` + category + `","importance":"low","tags":["promo","promo"],"flags":[],"confidence":0.99,"uncertain":false,"insufficient_context":false,"prompt_injection_suspected":false,"summary":"short","reason_code":"routine"}`
}

func TestParseClassificationIsStrictAndSafe(t *testing.T) {
	value, err := ParseClassification([]byte(validClassificationJSON("future-taxonomy")))
	if err != nil {
		t.Fatal(err)
	}
	if value.Category != domain.CategoryUnknown || len(value.Tags) != 1 {
		t.Fatalf("unknown taxonomy = %#v", value)
	}
	if _, err := ParseClassification([]byte(validClassificationJSON("promotion") + " {}")); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	if _, err := ParseClassification([]byte(strings.Replace(validClassificationJSON("promotion"), `"confidence":0.99`, `"confidence":2`, 1))); err == nil {
		t.Fatal("invalid confidence was accepted")
	}
	if _, err := ParseClassification([]byte(`{"schema_version":1,"category":"promotion","importance":"low","confidence":0.9,"unknown":true}`)); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestReleaseFingerprintExcludesPricingAndCredential(t *testing.T) {
	base := ReleaseSpec{ProviderType: "openai-compatible", BaseURL: "HTTPS://Example.Test/v1/", Model: "m", Prompt: BuiltInPrompt, SchemaVersion: ClassificationSchemaVersion, StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-v1"}
	a, err := Fingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint(ReleaseSpec{ProviderType: base.ProviderType, BaseURL: "https://example.test/v1", Model: base.Model, Prompt: base.Prompt, SchemaVersion: base.SchemaVersion, StructuredOutputMode: base.StructuredOutputMode, PreprocessorVersion: base.PreprocessorVersion, NormalizerVersion: base.NormalizerVersion})
	if err != nil || a != b {
		t.Fatalf("canonical fingerprints differ: %q %q %v", a, b, err)
	}
	base.Model = "other"
	c, err := Fingerprint(base)
	if err != nil || c == a {
		t.Fatalf("semantic model change did not change fingerprint: %q %q", a, c)
	}
}
