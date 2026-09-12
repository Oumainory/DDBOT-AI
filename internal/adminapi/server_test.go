package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/auth"
	"github.com/Oumainory/DDBOT-AI/internal/buildinfo"
	"github.com/Oumainory/DDBOT-AI/internal/origin"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/secretstore"
	"github.com/Oumainory/DDBOT-AI/internal/session"
	"github.com/Oumainory/DDBOT-AI/lsp/subscription"
)

const apiTestOrigin = "https://admin.example.test"

func newPlatformAPITestServer(t *testing.T) (*Server, auth.BootstrapResult, func()) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "platform.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }})
	secretService := secretstore.New(ctx, platformdb.NewSecretRepository(store), secretstore.Config{
		MasterKeyFile: filepath.Join(t.TempDir(), "master.key"), Now: func() time.Time { return now },
	})
	probe := platformdb.NewProbeWithAuthAndSecret(store, nil, secretService)
	server, err := NewServer(Config{
		Auth: authService, Probe: &probe, Origin: origin.Policy{AllowedOrigin: apiTestOrigin},
		Cookie:           session.CookiePolicy{SameSite: http.SameSiteLaxMode},
		DomainRepository: platformdb.NewDomainRepository(store), LegacySubscriptions: subscription.NewService(),
		Build: buildinfo.Info{ProductName: "DDBOT-AI", Version: "test-version", Commit: "0123456789abcdef0123456789abcdef01234567", BuildTime: "test-time", SourceRepository: buildinfo.SourceRepository, License: buildinfo.License, LicenseName: buildinfo.LicenseName},
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	bootstrap, err := authService.Bootstrap(ctx)
	if err != nil || !bootstrap.Created {
		_ = store.Close()
		t.Fatalf("Bootstrap = %#v, %v", bootstrap, err)
	}
	return server, bootstrap, func() { _ = store.Close() }
}

func apiJSONRequest(method, target string, body any) *http.Request {
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", apiTestOrigin)
	r.RemoteAddr = "127.0.0.1:1234"
	return r
}

func TestPlatformAPIOverviewAboutAndLogoutFlow(t *testing.T) {
	server, bootstrap, cleanup := newPlatformAPITestServer(t)
	defer cleanup()
	handler := server.Handler()

	unauthOverview := httptest.NewRecorder()
	handler.ServeHTTP(unauthOverview, httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/platform/overview", nil))
	if unauthOverview.Code != http.StatusUnauthorized || !strings.Contains(unauthOverview.Body.String(), "unauthorized") {
		t.Fatalf("unauthenticated overview = %d %s", unauthOverview.Code, unauthOverview.Body.String())
	}
	unauthAbout := httptest.NewRecorder()
	handler.ServeHTTP(unauthAbout, httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/about", nil))
	if unauthAbout.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated about = %d %s", unauthAbout.Code, unauthAbout.Body.String())
	}

	setup := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token, "username": "admin", "password": "correct horse battery staple",
	})
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusCreated || strings.Contains(setupResponse.Body.String(), bootstrap.Token) {
		t.Fatalf("setup = %d %s", setupResponse.Code, setupResponse.Body.String())
	}

	login := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	})
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginEnvelope struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginEnvelope); err != nil || loginEnvelope.Data.CSRF == "" {
		t.Fatalf("login envelope = %s, err=%v", loginResponse.Body.String(), err)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("login cookies = %#v", cookies)
	}

	overview := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/platform/overview", nil)
	overview.AddCookie(cookies[0])
	overviewResponse := httptest.NewRecorder()
	handler.ServeHTTP(overviewResponse, overview)
	if overviewResponse.Code != http.StatusOK || !strings.Contains(overviewResponse.Body.String(), "DDBOT-AI") || strings.Contains(overviewResponse.Body.String(), "master.key") {
		t.Fatalf("overview = %d %s", overviewResponse.Code, overviewResponse.Body.String())
	}

	about := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/about", nil)
	about.AddCookie(cookies[0])
	aboutResponse := httptest.NewRecorder()
	handler.ServeHTTP(aboutResponse, about)
	if aboutResponse.Code != http.StatusOK || !strings.Contains(aboutResponse.Body.String(), "AGPL-3.0") || !strings.Contains(aboutResponse.Body.String(), "0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("about = %d %s", aboutResponse.Code, aboutResponse.Body.String())
	}

	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/not-found", nil))
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Header().Get("Content-Type"), "application/json") || strings.Contains(unknown.Body.String(), "<html") {
		t.Fatalf("unknown API = %d %s", unknown.Code, unknown.Body.String())
	}

	logoutMissing := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/logout", nil)
	logoutMissing.Header.Set("Origin", apiTestOrigin)
	logoutMissing.AddCookie(cookies[0])
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, logoutMissing)
	if missingResponse.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf = %d %s", missingResponse.Code, missingResponse.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/logout", nil)
	logout.Header.Set("Origin", apiTestOrigin)
	logout.Header.Set("X-CSRF-Token", loginEnvelope.Data.CSRF)
	logout.AddCookie(cookies[0])
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", logoutResponse.Code, logoutResponse.Body.String())
	}
}

func TestRequireMutationNeedsOriginAndCSRF(t *testing.T) {
	server, bootstrap, cleanup := newPlatformAPITestServer(t)
	defer cleanup()
	handler := server.Handler()
	setup := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{"setup_token": bootstrap.Token, "username": "admin", "password": "correct horse battery staple"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, setup)
	login := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"})
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, login)
	var envelope struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(loginRecorder.Body.Bytes(), &envelope)
	cookie := loginRecorder.Result().Cookies()[0]
	mutation := server.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	missingOrigin := httptest.NewRequest(http.MethodPost, "/internal-test", nil)
	missingOrigin.AddCookie(cookie)
	missingOrigin.Header.Set("X-CSRF-Token", envelope.Data.CSRF)
	missingResponse := httptest.NewRecorder()
	mutation.ServeHTTP(missingResponse, missingOrigin)
	if missingResponse.Code != http.StatusForbidden {
		t.Fatalf("mutation without origin = %d", missingResponse.Code)
	}
	valid := httptest.NewRequest(http.MethodPost, "/internal-test", nil)
	valid.AddCookie(cookie)
	valid.Header.Set("Origin", apiTestOrigin)
	valid.Header.Set("X-CSRF-Token", envelope.Data.CSRF)
	validResponse := httptest.NewRecorder()
	mutation.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("valid mutation = %d %s", validResponse.Code, validResponse.Body.String())
	}
}

func TestDomainSourceTargetCommandsUseLegacySafeBoundaries(t *testing.T) {
	server, bootstrap, cleanup := newPlatformAPITestServer(t)
	defer cleanup()
	handler := server.Handler()
	setup := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token, "username": "admin", "password": "correct horse battery staple",
	})
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	login := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	})
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginEnvelope struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginEnvelope); err != nil {
		t.Fatal(err)
	}
	cookie := loginResponse.Result().Cookies()[0]
	connector, err := server.domainRepository.EnsureMainConnector(context.Background(), "onebot")
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, body any, key string) *httptest.ResponseRecorder {
		req := apiJSONRequest(http.MethodPost, "http://admin.example.test"+path, body)
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", loginEnvelope.Data.CSRF)
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	sourceBody := map[string]any{"platform": "bilibili", "external_id": "401742377", "display_name": "原神"}
	sourceResponse := post("/api/v2/sources", sourceBody, "source-create-key-1")
	if sourceResponse.Code != http.StatusCreated || !strings.Contains(sourceResponse.Body.String(), "401742377") {
		t.Fatalf("create source = %d %s", sourceResponse.Code, sourceResponse.Body.String())
	}
	replay := post("/api/v2/sources", sourceBody, "source-create-key-1")
	if replay.Code != http.StatusCreated {
		t.Fatalf("source replay = %d %s", replay.Code, replay.Body.String())
	}
	conflict := post("/api/v2/sources", map[string]any{"platform": "bilibili", "external_id": "999"}, "source-create-key-1")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("source idempotency conflict = %d %s", conflict.Code, conflict.Body.String())
	}
	targetResponse := post("/api/v2/targets", map[string]any{"connector_id": connector.ID, "target_type": "group", "external_id": "123456"}, "target-create-key-1")
	if targetResponse.Code != http.StatusCreated {
		t.Fatalf("create target = %d %s", targetResponse.Code, targetResponse.Body.String())
	}
	privateResponse := post("/api/v2/targets", map[string]any{"connector_id": connector.ID, "target_type": "private", "external_id": "9"}, "target-create-key-2")
	if privateResponse.Code != http.StatusBadRequest || !strings.Contains(privateResponse.Body.String(), "invalid_argument") {
		t.Fatalf("private target = %d %s", privateResponse.Code, privateResponse.Body.String())
	}
}
