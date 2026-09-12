package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/auth"
	"github.com/cnxysoft/DDBOT-WSa/internal/buildinfo"
	"github.com/cnxysoft/DDBOT-WSa/internal/evaluation"
	"github.com/cnxysoft/DDBOT-WSa/internal/origin"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/policy"
	"github.com/cnxysoft/DDBOT-WSa/internal/provider"
	"github.com/cnxysoft/DDBOT-WSa/internal/secretstore"
	"github.com/cnxysoft/DDBOT-WSa/internal/session"
	"github.com/cnxysoft/DDBOT-WSa/internal/shadow"
	"github.com/cnxysoft/DDBOT-WSa/lsp/subscription"
	"go.uber.org/atomic"
)

func TestAIProviderResponseDoesNotExposeCredentialAndEnforceIsLocked(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "platform.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secretService := secretstore.New(ctx, platformdb.NewSecretRepository(store), secretstore.Config{MasterKeyFile: filepath.Join(t.TempDir(), "master.key"), Now: func() time.Time { return now }})
	authService := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }})
	bootstrap, err := authService.Bootstrap(ctx)
	if err != nil || !bootstrap.Created {
		t.Fatalf("bootstrap = %#v, %v", bootstrap, err)
	}
	aiRepository := platformdb.NewAIRepository(store)
	fake := &provider.FakeOpenAICompatibleProvider{}
	aiProvider := provider.NewSwappable(fake)
	shadowRuntime := shadow.New(shadow.Config{Repository: aiRepository, Provider: aiProvider, Now: func() time.Time { return now }})
	defer shadowRuntime.Close(context.Background())
	server, err := NewServer(Config{Auth: authService, Probe: func() *platformdb.Probe {
		probe := platformdb.NewProbeWithAuthAndSecret(store, nil, secretService)
		return &probe
	}(), Origin: origin.Policy{AllowedOrigin: apiTestOrigin}, Cookie: session.CookiePolicy{SameSite: http.SameSiteLaxMode}, LegacyOnline: atomic.NewBool(true), Build: buildinfo.Info{ProductName: "DDBOT-AI", Version: "test", Commit: "0123456789abcdef0123456789abcdef01234567", SourceRepository: buildinfo.SourceRepository, License: buildinfo.License, LicenseName: buildinfo.LicenseName}, LegacySubscriptions: subscription.NewService(), Idempotency: nil, AIRepository: aiRepository, SecretStore: secretService, AIProvider: aiProvider, ShadowRuntime: shadowRuntime, EvaluationRunner: evaluation.New(aiRepository, aiProvider, func() time.Time { return now })})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	setup := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{"setup_token": bootstrap.Token, "username": "admin", "password": "correct horse battery staple"})
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	login := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"})
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
	getProvider := httptest.NewRequest(http.MethodGet, "http://admin.example.test/api/v2/ai/provider", nil)
	getProvider.AddCookie(cookie)
	providerResponse := httptest.NewRecorder()
	handler.ServeHTTP(providerResponse, getProvider)
	if providerResponse.Code != http.StatusOK || strings.Contains(providerResponse.Body.String(), "api_key") || strings.Contains(providerResponse.Body.String(), bootstrap.Token) {
		t.Fatalf("provider response = %d %s", providerResponse.Code, providerResponse.Body.String())
	}
	policyRequest := apiJSONRequest(http.MethodPatch, "http://admin.example.test/api/v2/ai/policy/global", map[string]any{"mode": policy.ModeEnforce})
	policyRequest.AddCookie(cookie)
	policyRequest.Header.Set("X-CSRF-Token", loginEnvelope.Data.CSRF)
	policyRequest.Header.Set("Idempotency-Key", "enforce-lock-test")
	policyResponse := httptest.NewRecorder()
	handler.ServeHTTP(policyResponse, policyRequest)
	if policyResponse.Code != http.StatusConflict || !strings.Contains(policyResponse.Body.String(), "enforce_not_available") {
		t.Fatalf("enforce response = %d %s", policyResponse.Code, policyResponse.Body.String())
	}
}
