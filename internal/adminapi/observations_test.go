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

	"github.com/cnxysoft/DDBOT-WSa/internal/auth"
	"github.com/cnxysoft/DDBOT-WSa/internal/buildinfo"
	"github.com/cnxysoft/DDBOT-WSa/internal/observation"
	"github.com/cnxysoft/DDBOT-WSa/internal/origin"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/session"
)

type observationAPIFixture struct {
	server    *Server
	store     *platformdb.Store
	repo      *platformdb.ObservationRepository
	recorder  *observation.Recorder
	cookie    *http.Cookie
	csrfToken string
	handler   http.Handler
}

func newObservationAPIFixture(t *testing.T) observationAPIFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "observation-api.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	repo := platformdb.NewObservationRepository(store)
	recorder := observation.NewRecorder(repo, observation.Config{PruneInitialDelay: time.Hour})
	authService := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{Now: func() time.Time { return now }})
	probe := platformdb.NewProbe(store, nil)
	server, err := NewServer(Config{
		Auth: authService, Probe: &probe, Origin: origin.Policy{AllowedOrigin: apiTestOrigin},
		Cookie:                session.CookiePolicy{SameSite: http.SameSiteLaxMode},
		Build:                 buildinfo.Info{ProductName: "DDBOT-AI", Version: "test", Commit: "0123456789abcdef0123456789abcdef01234567", License: buildinfo.License, LicenseName: buildinfo.LicenseName},
		ObservationRepository: repo, ObservationRecorder: recorder, Now: func() time.Time { return now },
	})
	if err != nil {
		recorder.Close(context.Background())
		store.Close()
		t.Fatal(err)
	}
	bootstrap, err := authService.Bootstrap(ctx)
	if err != nil || !bootstrap.Created {
		recorder.Close(context.Background())
		store.Close()
		t.Fatalf("bootstrap = %#v, %v", bootstrap, err)
	}
	handler := server.Handler()
	setup := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/setup", map[string]string{"setup_token": bootstrap.Token, "username": "admin", "password": "correct horse battery staple"})
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusCreated {
		recorder.Close(context.Background())
		store.Close()
		t.Fatalf("setup = %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	login := apiJSONRequest(http.MethodPost, "http://admin.example.test/api/v2/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"})
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		recorder.Close(context.Background())
		store.Close()
		t.Fatalf("login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	var envelope struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &envelope); err != nil || envelope.Data.CSRF == "" {
		recorder.Close(context.Background())
		store.Close()
		t.Fatalf("login envelope = %s, err=%v", loginResponse.Body.String(), err)
	}
	return observationAPIFixture{server: server, store: store, repo: repo, recorder: recorder, cookie: loginResponse.Result().Cookies()[0], csrfToken: envelope.Data.CSRF, handler: handler}
}

func (f observationAPIFixture) close() {
	_ = f.recorder.Close(context.Background())
	_ = f.store.Close()
}

func observationAPIRequest(method, target string, cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(nil))
	request.AddCookie(cookie)
	return request
}

func seedObservationFacts(t *testing.T, repo *platformdb.ObservationRepository) string {
	t.Helper()
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	eventID := "evt-api-1"
	if err := repo.InsertObservedEvent(ctx, platformdb.ObservedEventRecord{ID: eventID, SchemaVersion: 1, Platform: "bilibili", SourceKind: "account", SourceExternalID: "401742377", UpstreamEventID: "dynamic-1", EventType: "dynamic", ObservedAt: at, ContentFingerprint: strings.Repeat("c", 64), PublicSnapshotJSON: `{"platform":"bilibili","event_type":"dynamic","text":"public preview","author_name":"official","url":"https://example.invalid/public","secret":"test-secret-do-not-leak-123","ciphertext":"opaque-ciphertext","cookie":"session-cookie"}`, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertRouteObservation(ctx, platformdb.RouteObservationRecord{ID: "route-api-1", EventID: eventID, RouteOrdinal: 0, DestinationKind: "qq_group", DestinationExternalID: "123456", Outcome: "filtered", ReasonCode: "legacy_filter", ObservedAt: at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertDeliveryObservation(ctx, platformdb.DeliveryObservationRecord{ID: "delivery-api-1", EventID: eventID, RouteObservationID: "route-api-1", ConnectorKind: "onebot", DestinationExternalID: "123456", Status: "unknown", ResultCode: "result_unknown", ObservedAt: at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	return eventID
}

func seedFilteredObservation(t *testing.T, repo *platformdb.ObservationRepository) string {
	t.Helper()
	at := time.Unix(1_700_000_001, 0).UTC()
	eventID := "evt-api-filtered"
	if err := repo.InsertObservedEvent(context.Background(), platformdb.ObservedEventRecord{ID: eventID, SchemaVersion: 1, Platform: "bilibili", SourceKind: "account", SourceExternalID: "filtered-source", EventType: "dynamic", ObservedAt: at, ContentFingerprint: strings.Repeat("d", 64), PublicSnapshotJSON: `{"platform":"bilibili","event_type":"dynamic","text":"filtered preview"}`, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertRouteObservation(context.Background(), platformdb.RouteObservationRecord{ID: "route-api-filtered", EventID: eventID, RouteOrdinal: 0, DestinationKind: "qq_group", DestinationExternalID: "123456", Outcome: "filtered", ReasonCode: "legacy_filter", ObservedAt: at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	return eventID
}

func TestObservationAPIReadFlowAndStableErrors(t *testing.T) {
	fixture := newObservationAPIFixture(t)
	defer fixture.close()
	eventID := seedObservationFacts(t, fixture.repo)
	filteredEventID := seedFilteredObservation(t, fixture.repo)

	unauth := httptest.NewRecorder()
	fixture.handler.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/v2/observations/events", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d %s", unauth.Code, unauth.Body.String())
	}

	list := httptest.NewRecorder()
	fixture.handler.ServeHTTP(list, observationAPIRequest(http.MethodGet, "/api/v2/observations/events?limit=1&platform=bilibili&source_external_id=401742377", fixture.cookie))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "test-secret-do-not-leak-123") || strings.Contains(list.Body.String(), "opaque-ciphertext") || !strings.Contains(list.Body.String(), "public preview") {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	var listEnvelope struct {
		Data observationPageDTO `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listEnvelope); err != nil || len(listEnvelope.Data.Items) != 1 || listEnvelope.Data.Items[0].RouteCount != 1 || listEnvelope.Data.Items[0].FinalDeliveryStatus != "unknown" {
		t.Fatalf("list payload = %#v, err=%v", listEnvelope, err)
	}

	detail := httptest.NewRecorder()
	fixture.handler.ServeHTTP(detail, observationAPIRequest(http.MethodGet, "/api/v2/observations/events/"+eventID, fixture.cookie))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "public preview") || strings.Contains(detail.Body.String(), "test-secret-do-not-leak-123") || strings.Contains(detail.Body.String(), "ciphertext") || strings.Contains(detail.Body.String(), "session-cookie") {
		t.Fatalf("detail = %d %s", detail.Code, detail.Body.String())
	}
	routes := httptest.NewRecorder()
	fixture.handler.ServeHTTP(routes, observationAPIRequest(http.MethodGet, "/api/v2/observations/events/"+eventID+"/routes", fixture.cookie))
	if routes.Code != http.StatusOK || !strings.Contains(routes.Body.String(), "legacy_filter") {
		t.Fatalf("routes = %d %s", routes.Code, routes.Body.String())
	}
	deliveries := httptest.NewRecorder()
	fixture.handler.ServeHTTP(deliveries, observationAPIRequest(http.MethodGet, "/api/v2/observations/events/"+eventID+"/deliveries", fixture.cookie))
	if deliveries.Code != http.StatusOK || !strings.Contains(deliveries.Body.String(), "unknown") {
		t.Fatalf("deliveries = %d %s", deliveries.Code, deliveries.Body.String())
	}
	filteredDetail := httptest.NewRecorder()
	fixture.handler.ServeHTTP(filteredDetail, observationAPIRequest(http.MethodGet, "/api/v2/observations/events/"+filteredEventID, fixture.cookie))
	if filteredDetail.Code != http.StatusOK || !strings.Contains(filteredDetail.Body.String(), "filtered") || !strings.Contains(filteredDetail.Body.String(), `"deliveries":[]`) {
		t.Fatalf("filtered detail = %d %s", filteredDetail.Code, filteredDetail.Body.String())
	}
	summary := httptest.NewRecorder()
	fixture.handler.ServeHTTP(summary, observationAPIRequest(http.MethodGet, "/api/v2/observations/summary", fixture.cookie))
	if summary.Code != http.StatusOK || !strings.Contains(summary.Body.String(), `"retention_days":90`) || !strings.Contains(summary.Body.String(), `"status":"available"`) {
		t.Fatalf("summary = %d %s", summary.Code, summary.Body.String())
	}

	badCursor := httptest.NewRecorder()
	fixture.handler.ServeHTTP(badCursor, observationAPIRequest(http.MethodGet, "/api/v2/observations/events?cursor=not-a-cursor", fixture.cookie))
	if badCursor.Code != http.StatusBadRequest || !strings.Contains(badCursor.Body.String(), "invalid_argument") {
		t.Fatalf("bad cursor = %d %s", badCursor.Code, badCursor.Body.String())
	}
	badLimit := httptest.NewRecorder()
	fixture.handler.ServeHTTP(badLimit, observationAPIRequest(http.MethodGet, "/api/v2/observations/events?limit=201", fixture.cookie))
	if badLimit.Code != http.StatusBadRequest || !strings.Contains(badLimit.Body.String(), "invalid_argument") {
		t.Fatalf("bad limit = %d %s", badLimit.Code, badLimit.Body.String())
	}
	badTimeRange := httptest.NewRecorder()
	fixture.handler.ServeHTTP(badTimeRange, observationAPIRequest(http.MethodGet, "/api/v2/observations/events?from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z", fixture.cookie))
	if badTimeRange.Code != http.StatusBadRequest || !strings.Contains(badTimeRange.Body.String(), "invalid_argument") {
		t.Fatalf("bad time range = %d %s", badTimeRange.Code, badTimeRange.Body.String())
	}
	missing := httptest.NewRecorder()
	fixture.handler.ServeHTTP(missing, observationAPIRequest(http.MethodGet, "/api/v2/observations/events/missing", fixture.cookie))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "observation_not_found") {
		t.Fatalf("missing detail = %d %s", missing.Code, missing.Body.String())
	}
}

func TestObservationAPIUnavailableIsSanitized(t *testing.T) {
	fixture := newObservationAPIFixture(t)
	defer fixture.close()
	fixture.server.observationRepository = nil
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, observationAPIRequest(http.MethodGet, "/api/v2/observations/events", fixture.cookie))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "observation_unavailable") || strings.Contains(response.Body.String(), "sqlite") || strings.Contains(response.Body.String(), "observation-api.sqlite") {
		t.Fatalf("unavailable response = %d %s", response.Code, response.Body.String())
	}
}
