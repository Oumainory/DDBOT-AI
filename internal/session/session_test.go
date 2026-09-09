package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerateTokenStoresOnlyHashMaterial(t *testing.T) {
	first, firstHash, err := GenerateToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := GenerateToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || len(second) != 43 || first == second {
		t.Fatalf("token generation = %q, %q", first, second)
	}
	if firstHash == first || firstHash != HashToken(first) || secondHash != HashToken(second) {
		t.Fatalf("token hash leaked or did not match")
	}
	if len(firstHash) != 64 {
		t.Fatalf("token hash length = %d, want 64", len(firstHash))
	}
}

func TestCookiePolicyAndClearCookie(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	expires := now.Add(time.Hour)
	policy := CookiePolicy{Secure: true, SameSite: http.SameSiteStrictMode}
	cookie := NewCookie("raw-session-token", now, expires, policy)
	if cookie.Name != DefaultCookieName || cookie.Path != "/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie policy = %#v", cookie)
	}
	if cookie.Value == "" || cookie.MaxAge != 3600 {
		t.Fatalf("cookie expiry = %#v", cookie)
	}
	cleared := ClearCookie(now, policy)
	if cleared.Value != "" || cleared.MaxAge != -1 || !cleared.HttpOnly || !cleared.Secure {
		t.Fatalf("clear cookie = %#v", cleared)
	}

	request := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
	request.AddCookie(cookie)
	parsed, err := ParseCookie(request, policy)
	if err != nil || parsed != "raw-session-token" {
		t.Fatalf("ParseCookie = %q, %v", parsed, err)
	}
	if strings.Contains(cookie.String(), "csrf") {
		t.Fatal("cookie unexpectedly contains csrf material")
	}
}
