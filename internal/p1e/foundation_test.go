package p1e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/adminapi"
	"github.com/Oumainory/DDBOT-AI/internal/auth"
	"github.com/Oumainory/DDBOT-AI/internal/buildinfo"
	"github.com/Oumainory/DDBOT-AI/internal/origin"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/secretstore"
	"github.com/Oumainory/DDBOT-AI/internal/session"
	"go.uber.org/atomic"
	_ "modernc.org/sqlite"
)

const acceptanceOrigin = "https://admin.example.test"

func TestPhase1FreshRestartAndRecoveryFlow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	root := t.TempDir()
	dbPath := filepath.Join(root, "ddbot-ai.sqlite")
	keyPath := filepath.Join(root, "secrets", "master.key")

	store, err := platformdb.Open(ctx, platformdb.Config{Path: dbPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	secret := secretstore.New(ctx, platformdb.NewSecretRepository(store), secretstore.Config{MasterKeyFile: keyPath, Now: func() time.Time { return now }})
	if secret.State() != secretstore.StateReady {
		t.Fatalf("fresh secret store state = %s, init error = %v", secret.State(), secret.InitError())
	}
	authService := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }})
	bootstrap, err := authService.Bootstrap(ctx)
	if err != nil || !bootstrap.Created || bootstrap.Token == "" {
		t.Fatalf("fresh bootstrap = %#v, %v", bootstrap, err)
	}
	server := newAcceptanceServer(t, authService, store, secret)
	handler := server.Handler()

	setupResponse := serveJSON(t, handler, http.MethodPost, "/api/v2/setup", map[string]string{
		"setup_token": bootstrap.Token,
		"username":    "admin",
		"password":    "correct horse battery staple",
	})
	if setupResponse.Code != http.StatusCreated || strings.Contains(setupResponse.Body.String(), bootstrap.Token) {
		t.Fatalf("fresh setup = %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	loginResponse := serveJSON(t, handler, http.MethodPost, "/api/v2/auth/login", map[string]string{
		"username": "admin",
		"password": "correct horse battery staple",
	})
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("fresh login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	sessionCookie := loginResponse.Result().Cookies()[0]
	overview := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/platform/overview", nil)
	overview.AddCookie(sessionCookie)
	overviewResponse := httptest.NewRecorder()
	handler.ServeHTTP(overviewResponse, overview)
	if overviewResponse.Code != http.StatusOK || !strings.Contains(overviewResponse.Body.String(), "DDBOT-AI") {
		t.Fatalf("fresh overview = %d %s", overviewResponse.Code, overviewResponse.Body.String())
	}
	remoteReset := serveJSON(t, handler, http.MethodPost, "/api/v2/admin/reset-password", map[string]string{"password": "must-not-be-an-endpoint"})
	if remoteReset.Code != http.StatusNotFound || strings.Contains(remoteReset.Body.String(), "password reset") {
		t.Fatalf("remote reset endpoint = %d %s", remoteReset.Code, remoteReset.Body.String())
	}
	about := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/about", nil)
	about.AddCookie(sessionCookie)
	aboutResponse := httptest.NewRecorder()
	handler.ServeHTTP(aboutResponse, about)
	if aboutResponse.Code != http.StatusOK || !strings.Contains(aboutResponse.Body.String(), `"version":"acceptance"`) || !strings.Contains(aboutResponse.Body.String(), `"build_time":"acceptance-build"`) || !strings.Contains(aboutResponse.Body.String(), "https://github.com/Oumainory/DDBOT-AI/commit/0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("fresh about = %d %s", aboutResponse.Code, aboutResponse.Body.String())
	}

	if _, err := secret.CreateCredential(ctx, secretstore.CredentialInput{ID: "cred-acceptance", Type: "generic", Label: "Acceptance", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := secret.SetSecret(ctx, "cred-acceptance", []byte("acceptance-secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart the same instance with the same durable database and key. The
	// existing session, sentinel, ciphertext, and admin state must remain valid.
	store, err = platformdb.Open(ctx, platformdb.Config{Path: dbPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	secret = secretstore.New(ctx, platformdb.NewSecretRepository(store), secretstore.Config{MasterKeyFile: keyPath, Now: func() time.Time { return now }})
	if secret.State() != secretstore.StateReady {
		t.Fatalf("restart secret store state = %s, init error = %v", secret.State(), secret.InitError())
	}
	resolved, err := secret.ResolveSecret(ctx, "cred-acceptance")
	if err != nil || string(resolved) != "acceptance-secret" {
		t.Fatalf("restart secret = %q, %v", resolved, err)
	}
	authService = auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }})
	restartedServer := newAcceptanceServer(t, authService, store, secret)
	restartedOverview := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/platform/overview", nil)
	restartedOverview.AddCookie(sessionCookie)
	restartedResponse := httptest.NewRecorder()
	restartedServer.Handler().ServeHTTP(restartedResponse, restartedOverview)
	if restartedResponse.Code != http.StatusOK || !strings.Contains(restartedResponse.Body.String(), `"secret_store":{"status":"available"`) {
		t.Fatalf("restart overview = %d %s", restartedResponse.Code, restartedResponse.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Corrupt only the metadata/envelope relationship. Startup must preserve
	// metadata access but enter recovery and make readiness fail closed.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE credentials SET configured = 0 WHERE id = ?", "cred-acceptance"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = platformdb.Open(ctx, platformdb.Config{Path: dbPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret = secretstore.New(ctx, platformdb.NewSecretRepository(store), secretstore.Config{MasterKeyFile: keyPath, Now: func() time.Time { return now }})
	if secret.State() != secretstore.StateRecovery {
		t.Fatalf("recovery secret store state = %s, init error = %v", secret.State(), secret.InitError())
	}
	if _, err := secret.Metadata(ctx, "cred-acceptance"); err != nil {
		t.Fatalf("metadata during recovery = %v", err)
	}
	if _, err := secret.ResolveSecret(ctx, "cred-acceptance"); !errors.Is(err, secretstore.ErrSecretStoreRecovery) {
		t.Fatalf("ResolveSecret during recovery = %v", err)
	}
	if err := secret.SetSecret(ctx, "cred-acceptance", []byte("must-not-write")); !errors.Is(err, secretstore.ErrSecretStoreRecovery) {
		t.Fatalf("SetSecret during recovery = %v", err)
	}
	probe := platformdb.NewProbeWithAuthAndSecret(store, nil, secret)
	health := httptest.NewRecorder()
	probe.Healthz().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"degraded"`) || !strings.Contains(health.Body.String(), `"code":"secret_store_recovery"`) {
		t.Fatalf("recovery health = %d %s", health.Code, health.Body.String())
	}
	ready := httptest.NewRecorder()
	probe.Readyz().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"status":"not_ready"`) || !strings.Contains(ready.Body.String(), `"code":"secret_store_recovery"`) {
		t.Fatalf("recovery readiness = %d %s", ready.Code, ready.Body.String())
	}
	recoveryServer := newAcceptanceServer(t, auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }}), store, secret)
	recoveryOverview := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/platform/overview", nil)
	recoveryOverview.AddCookie(sessionCookie)
	recoveryResponse := httptest.NewRecorder()
	recoveryServer.Handler().ServeHTTP(recoveryResponse, recoveryOverview)
	if recoveryResponse.Code != http.StatusOK || !strings.Contains(recoveryResponse.Body.String(), `"status":"recovery"`) || strings.Contains(recoveryResponse.Body.String(), "acceptance-secret") {
		t.Fatalf("recovery overview = %d %s", recoveryResponse.Code, recoveryResponse.Body.String())
	}
}

func newAcceptanceServer(t *testing.T, authService *auth.Service, store *platformdb.Store, secret *secretstore.Service) *adminapi.Server {
	t.Helper()
	probe := platformdb.NewProbeWithAuthAndSecret(store, nil, secret)
	server, err := adminapi.NewServer(adminapi.Config{
		Auth: authService, Probe: &probe, Origin: origin.Policy{AllowedOrigin: acceptanceOrigin},
		Cookie: session.CookiePolicy{SameSite: http.SameSiteLaxMode}, LegacyOnline: atomic.NewBool(true),
		Build: buildinfo.Info{ProductName: "DDBOT-AI", Version: "acceptance", Commit: "0123456789abcdef0123456789abcdef01234567", BuildTime: "acceptance-build", License: buildinfo.License, LicenseName: buildinfo.LicenseName, SourceRepository: buildinfo.SourceRepository},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func serveJSON(t *testing.T, handler http.Handler, method, path string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "http://admin.example.test"+path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", acceptanceOrigin)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
