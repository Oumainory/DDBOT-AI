package platformdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeReportsReadyDatabaseAndNonSensitiveHTTPPayload(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "ddbot.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	probe := NewProbe(store, nil)
	ready := probe.Ready(ctx)
	if ready.Status != StatusReady || ready.HTTPStatus() != http.StatusOK {
		t.Fatalf("ready = %#v", ready)
	}
	if ready.Checks["schema"].Code != "schema_current" {
		t.Fatalf("schema check = %#v", ready.Checks["schema"])
	}

	recorder := httptest.NewRecorder()
	probe.Readyz().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("readyz status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("readyz content type = %q", got)
	}
	var response Report
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != StatusReady {
		t.Fatalf("readyz response = %#v", response)
	}
	if body := recorder.Body.String(); body == "" || containsAny(body, store.path, "sqlite_initialization_failed") {
		t.Fatalf("readyz leaked implementation detail: %s", body)
	}
}

func TestProbeSeparatesHealthFromReadinessWhenStoreFails(t *testing.T) {
	probe := NewProbe(nil, ErrDatabaseClosed)
	ctx := context.Background()

	health := probe.Health(ctx)
	if health.Status != StatusDegraded || health.HTTPStatus() != http.StatusOK {
		t.Fatalf("health = %#v, want degraded but live", health)
	}
	if health.Checks["process"].Status != CheckOK {
		t.Fatalf("process check = %#v", health.Checks["process"])
	}
	ready := probe.Ready(ctx)
	if ready.Status != StatusNotReady || ready.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("ready = %#v, want not ready", ready)
	}

	recorder := httptest.NewRecorder()
	probe.Healthz().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	probe.Readyz().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d", recorder.Code)
	}
}

func TestProbeWithAuthTreatsSetupRequiredAsReady(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "auth-health.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	probe := NewProbeWithAuth(store, nil)
	health := probe.Health(ctx)
	if health.Status != StatusHealthy || health.Checks["auth"].Code != "setup_required" {
		t.Fatalf("health with setup required = %#v", health)
	}
	ready := probe.Ready(ctx)
	if ready.Status != StatusReady || ready.HTTPStatus() != http.StatusOK || ready.Checks["auth"].Code != "setup_required" {
		t.Fatalf("ready with setup required = %#v", ready)
	}
}

func TestProbeRejectsUnsupportedMethods(t *testing.T) {
	probe := NewProbe(nil, ErrDatabaseClosed)
	recorder := httptest.NewRecorder()
	probe.Healthz().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d", recorder.Code)
	}
	if recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow = %q", recorder.Header().Get("Allow"))
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
