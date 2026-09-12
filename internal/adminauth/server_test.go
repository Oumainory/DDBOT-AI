package adminauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/auth"
	"github.com/Oumainory/DDBOT-AI/internal/origin"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/session"
)

const testOrigin = "https://admin.example.test"

func newAuthTestServer(t *testing.T, requireOrigin bool) (*Server, auth.BootstrapResult, func()) {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "admin.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	service := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: clock})
	server, err := NewServer(Config{
		Auth:          service,
		Origin:        origin.Policy{AllowedOrigin: testOrigin},
		RequireOrigin: requireOrigin,
		Cookie:        session.CookiePolicy{SameSite: http.SameSiteLaxMode},
		Now:           clock,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	bootstrap, err := server.Bootstrap(context.Background())
	if err != nil || !bootstrap.Created || bootstrap.Token == "" {
		store.Close()
		t.Fatalf("Bootstrap = %#v, %v", bootstrap, err)
	}
	return server, bootstrap, func() { _ = store.Close() }
}

func makeJSONRequest(method, target string, body any) *http.Request {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testOrigin)
	request.RemoteAddr = "127.0.0.1:1234"
	return request
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON = %q: %v", recorder.Body.String(), err)
	}
	return response
}

func TestSetupLoginSessionAndLogoutContract(t *testing.T) {
	server, bootstrap, cleanup := newAuthTestServer(t, false)
	defer cleanup()
	handler := server.Handler()

	setup := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "Admin",
		"password":    "correct horse battery staple",
	})
	setupRecorder := httptest.NewRecorder()
	handler.ServeHTTP(setupRecorder, setup)
	if setupRecorder.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRecorder.Code, setupRecorder.Body.String())
	}
	if strings.Contains(setupRecorder.Body.String(), bootstrap.Token) {
		t.Fatal("setup token leaked in setup response")
	}
	replay := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "second",
		"password":    "correct horse battery staple",
	})
	replayRecorder := httptest.NewRecorder()
	handler.ServeHTTP(replayRecorder, replay)
	if replayRecorder.Code != http.StatusConflict || !strings.Contains(replayRecorder.Body.String(), "setup_complete") {
		t.Fatalf("setup replay = %d %s", replayRecorder.Code, replayRecorder.Body.String())
	}

	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/setup/status", nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "ready") {
		t.Fatalf("setup status response = %d %s", statusRecorder.Code, statusRecorder.Body.String())
	}

	login := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
		"username": "ADMIN",
		"password": "correct horse battery staple",
	})
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	loginBody := decodeResponse(t, loginRecorder)
	data, ok := loginBody["data"].(map[string]any)
	if !ok || data["username"] != "admin" || data["csrf_token"] == nil {
		t.Fatalf("login data = %#v", loginBody)
	}
	if _, exists := data["session_token"]; exists || strings.Contains(loginRecorder.Body.String(), "session_token") {
		t.Fatal("raw session token was included in login response")
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != session.DefaultCookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("login cookie = %#v", cookies)
	}
	rawCookie := cookies[0]

	sessionRequest := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/auth/session", nil)
	sessionRequest.AddCookie(rawCookie)
	sessionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sessionRecorder, sessionRequest)
	if sessionRecorder.Code != http.StatusOK || !strings.Contains(sessionRecorder.Body.String(), "authenticated") || !strings.Contains(sessionRecorder.Body.String(), "csrf_token") {
		t.Fatalf("session response = %d %s", sessionRecorder.Code, sessionRecorder.Body.String())
	}
	csrfToken := data["csrf_token"].(string)

	logoutMissingCSRF := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/logout", nil)
	logoutMissingCSRF.Header.Set("Origin", testOrigin)
	logoutMissingCSRF.AddCookie(rawCookie)
	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(missingRecorder, logoutMissingCSRF)
	if missingRecorder.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf status = %d", missingRecorder.Code)
	}

	logout := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/logout", nil)
	logout.Header.Set("Origin", testOrigin)
	logout.Header.Set("X-CSRF-Token", csrfToken)
	logout.AddCookie(rawCookie)
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
	if cleared := logoutRecorder.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge != -1 || cleared[0].Value != "" {
		t.Fatalf("logout clear cookie = %#v", cleared)
	}

	afterLogout := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/auth/session", nil)
	afterLogout.AddCookie(rawCookie)
	afterLogoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(afterLogoutRecorder, afterLogout)
	if afterLogoutRecorder.Code != http.StatusOK {
		t.Fatalf("session after logout status = %d", afterLogoutRecorder.Code)
	}
	body := decodeResponse(t, afterLogoutRecorder)
	if body["data"].(map[string]any)["authenticated"] != false {
		t.Fatalf("session after logout = %#v", body)
	}
}

func TestSetupRequiresOriginAndLoginRejectsBadOrigin(t *testing.T) {
	server, bootstrap, cleanup := newAuthTestServer(t, false)
	defer cleanup()
	handler := server.Handler()
	request := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "admin",
		"password":    "correct horse battery staple",
	})
	request.Header.Del("Origin")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("setup without origin status = %d", recorder.Code)
	}

	badLogin := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
		"username": "admin",
		"password": "wrong password value",
	})
	badLogin.Header.Set("Origin", "https://evil.example.test")
	badRecorder := httptest.NewRecorder()
	handler.ServeHTTP(badRecorder, badLogin)
	if badRecorder.Code != http.StatusForbidden || strings.Contains(badRecorder.Body.String(), "invalid_credentials") {
		t.Fatalf("bad origin login = %d %s", badRecorder.Code, badRecorder.Body.String())
	}
}

func TestLoginAllowsMissingOriginByDefault(t *testing.T) {
	server, bootstrap, cleanup := newAuthTestServer(t, false)
	defer cleanup()
	handler := server.Handler()
	setup := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "admin",
		"password":    "correct horse battery staple",
	})
	setupRecorder := httptest.NewRecorder()
	handler.ServeHTTP(setupRecorder, setup)
	if setupRecorder.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRecorder.Code, setupRecorder.Body.String())
	}

	login := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
		"username": "admin",
		"password": "correct horse battery staple",
	})
	login.Header.Del("Origin")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login without origin status = %d, body = %s", loginRecorder.Code, loginRecorder.Body.String())
	}
}

func TestSetupTokenErrorsUseStableExternalCode(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "setup-errors.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }})
	server, err := NewServer(Config{Auth: service, Origin: origin.Policy{AllowedOrigin: testOrigin}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := server.Bootstrap(ctx)
	if err != nil || !bootstrap.Created {
		t.Fatalf("Bootstrap = %#v, %v", bootstrap, err)
	}

	wrong := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": "wrong-token",
		"username":    "admin",
		"password":    "correct horse battery staple",
	})
	wrongRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongRecorder, wrong)
	if wrongRecorder.Code != http.StatusUnauthorized || !strings.Contains(wrongRecorder.Body.String(), "invalid_setup_token") || strings.Contains(wrongRecorder.Body.String(), "wrong-token") {
		t.Fatalf("wrong setup token response = %d %s", wrongRecorder.Code, wrongRecorder.Body.String())
	}

	now = now.Add(auth.DefaultSetupTokenTTL + time.Second)
	expired := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "admin",
		"password":    "correct horse battery staple",
	})
	expiredRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(expiredRecorder, expired)
	if expiredRecorder.Code != http.StatusUnauthorized || !strings.Contains(expiredRecorder.Body.String(), "invalid_setup_token") || strings.Contains(expiredRecorder.Body.String(), bootstrap.Token) {
		t.Fatalf("expired setup token response = %d %s", expiredRecorder.Code, expiredRecorder.Body.String())
	}
}

func TestLoginRateLimitAndStableCredentialError(t *testing.T) {
	server, bootstrap, cleanup := newAuthTestServer(t, false)
	defer cleanup()
	handler := server.Handler()
	setup := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "admin",
		"password":    "correct horse battery staple",
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, setup)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("setup status = %d", recorder.Code)
	}

	responses := make([]string, 0, 6)
	for index := 0; index < 6; index++ {
		login := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
			"username": "admin",
			"password": "wrong password value",
		})
		login.RemoteAddr = "127.0.0.2:2345"
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, login)
		responses = append(responses, result.Body.String())
		if index < 5 && result.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d status = %d", index, result.Code)
		}
		if index == 5 && result.Code != http.StatusTooManyRequests {
			t.Fatalf("rate-limited login status = %d", result.Code)
		}
	}
	if responses[0] == "" || responses[0] != responses[1] {
		// request_id is intentionally unique, so compare only stable error data.
		var first, second map[string]any
		_ = json.Unmarshal([]byte(responses[0]), &first)
		_ = json.Unmarshal([]byte(responses[1]), &second)
		if first["error"].(map[string]any)["code"] != second["error"].(map[string]any)["code"] {
			t.Fatalf("credential errors differ: %s / %s", responses[0], responses[1])
		}
	}
}

func TestDecodeJSONRejectsUnknownAndOversizedBody(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t, false)
	defer cleanup()
	handler := server.Handler()
	unknown := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple", "unexpected": "field",
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, unknown)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field status = %d", recorder.Code)
	}

	oversized := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", io.LimitReader(strings.NewReader(strings.Repeat("x", int(DefaultMaxBodyBytes)+1)), int64(DefaultMaxBodyBytes)+1))
	oversized.Header.Set("Content-Type", "application/json")
	oversized.Header.Set("Origin", testOrigin)
	tooLarge := httptest.NewRecorder()
	handler.ServeHTTP(tooLarge, oversized)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d", tooLarge.Code)
	}
}

func TestAuthUnavailableDoesNotLeakDatabaseDetails(t *testing.T) {
	service := auth.NewService(nil, auth.Config{})
	server, err := NewServer(Config{Auth: service, Origin: origin.Policy{AllowedOrigin: testOrigin}})
	if err != nil {
		t.Fatal(err)
	}
	request := makeJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "database") {
		t.Fatalf("unavailable auth response = %d %s", recorder.Code, recorder.Body.String())
	}
}
