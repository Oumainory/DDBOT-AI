package idempotency

import "testing"

func TestFingerprintNormalizesPathAndJSONBody(t *testing.T) {
	first, err := NewFingerprint("post", "/api/v2/targets/target_a/?b=2&a=1", []byte(`{"name":"target","enabled":true}`))
	if err != nil {
		t.Fatalf("NewFingerprint(first) error = %v", err)
	}
	second, err := NewFingerprint("POST", "/api/v2/targets/target_a?a=1&b=2", []byte("{\n  \"enabled\": true, \"name\": \"target\"\n}"))
	if err != nil {
		t.Fatalf("NewFingerprint(second) error = %v", err)
	}

	if !first.Equal(second) {
		t.Fatalf("equivalent requests produced different fingerprints: %#v vs %#v", first, second)
	}
	if first.CanonicalQuery != "a=1&b=2" {
		t.Fatalf("canonical query = %q, want a=1&b=2", first.CanonicalQuery)
	}
	if got, err := CanonicalQuery("/api/v2/targets/target_a/?b=2&a=1"); err != nil || got != "a=1&b=2" {
		t.Fatalf("CanonicalQuery() = %q, %v", got, err)
	}
	if NormalizePathMust("/api/v2/targets/target_a/?b=2&a=1") != "/api/v2/targets/target_a?a=1&b=2" {
		t.Fatal("path was not normalized as expected")
	}
}

func TestFingerprintQuerySemanticsArePartOfIdentity(t *testing.T) {
	base, err := NewFingerprint("POST", "/api/v2/events/replay?event_id=one", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	differentQuery, err := NewFingerprint("POST", "/api/v2/events/replay?event_id=two", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if base.Equal(differentQuery) || Compare(base, differentQuery) != ConflictingRequest {
		t.Fatal("different canonical queries were treated as the same request")
	}
}

func TestFingerprintConcreteResourceIDsConflict(t *testing.T) {
	first, err := NewFingerprint("POST", "/api/v2/targets/target_a", []byte(`{"name":"target"}`))
	if err != nil {
		t.Fatalf("NewFingerprint(first) error = %v", err)
	}
	second, err := NewFingerprint("POST", "/api/v2/targets/target_b", []byte(`{"name":"target"}`))
	if err != nil {
		t.Fatalf("NewFingerprint(second) error = %v", err)
	}
	if Compare(first, second) != ConflictingRequest {
		t.Fatal("different concrete resources were treated as the same request")
	}
}

func TestNormalizeKeyBoundsAndCharacters(t *testing.T) {
	if _, err := NormalizeKey("short"); err != ErrInvalidKey {
		t.Fatalf("short key error = %v, want ErrInvalidKey", err)
	}
	if got, err := NormalizeKey("  1234567890abcdef  "); err != nil || got != "1234567890abcdef" {
		t.Fatalf("NormalizeKey() = %q, %v", got, err)
	}
	if _, err := NormalizeKey("1234567890abc\ndef"); err != ErrInvalidKey {
		t.Fatalf("control character key error = %v, want ErrInvalidKey", err)
	}
}

func TestCanonicalBodyRejectsMultipleJSONValues(t *testing.T) {
	if _, err := CanonicalBody([]byte(`{"one":1} {"two":2}`)); err == nil {
		t.Fatal("CanonicalBody accepted multiple JSON values")
	}
}

func TestFingerprintRequiresConcreteMethod(t *testing.T) {
	if _, err := NewFingerprint("", "/api/v2/test", nil); err != ErrInvalidMethod {
		t.Fatalf("empty method error = %v, want ErrInvalidMethod", err)
	}
	if _, err := NewFingerprint("POST\nX", "/api/v2/test", nil); err != ErrInvalidMethod {
		t.Fatalf("line-break method error = %v, want ErrInvalidMethod", err)
	}
}

func NormalizePathMust(value string) string {
	got, err := NormalizePath(value)
	if err != nil {
		panic(err)
	}
	return got
}
