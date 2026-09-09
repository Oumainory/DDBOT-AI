package origin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginPolicyRejectsCrossSiteAndNull(t *testing.T) {
	policy := Policy{AllowedOrigin: "https://admin.example.test"}
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v2/setup", nil)
	request.Header.Set("Origin", "https://admin.example.test")
	if err := policy.Validate(request); err != nil {
		t.Fatalf("same-origin validation = %v", err)
	}
	request.Header.Set("Origin", "https://evil.example.test")
	if err := policy.Validate(request); err == nil {
		t.Fatal("cross-site origin was accepted")
	}
	request.Header.Set("Origin", "null")
	if err := policy.Validate(request); err == nil {
		t.Fatal("null origin was accepted")
	}
}

func TestOriginPolicyIgnoresForwardedHeadersUnlessTrusted(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:15631/api/v2/setup", nil)
	request.Header.Set("Origin", "https://proxy.example.test")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "proxy.example.test")
	if err := (Policy{TrustForwarded: false}).Validate(request); err == nil {
		t.Fatal("untrusted forwarded origin was accepted")
	}
	if err := (Policy{TrustForwarded: true}).Validate(request); err != nil {
		t.Fatalf("trusted forwarded origin = %v", err)
	}
}
