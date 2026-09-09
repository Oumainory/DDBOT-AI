package platformdb

import (
	"context"
	"encoding/json"
	"net/http"
)

type Status string

const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusReady    Status = "ready"
	StatusNotReady Status = "not_ready"
)

type CheckStatus string

const (
	CheckOK       CheckStatus = "ok"
	CheckDegraded CheckStatus = "degraded"
	CheckNotReady CheckStatus = "not_ready"
)

// Check intentionally exposes stable, non-sensitive codes rather than raw
// database errors. Detailed errors belong in process logs, not health APIs.
type Check struct {
	Status CheckStatus `json:"status"`
	Code   string      `json:"code,omitempty"`
}

type Report struct {
	Status Status           `json:"status"`
	Checks map[string]Check `json:"checks"`
}

// HTTPStatus maps liveness to 200 even when a non-critical platform
// dependency is degraded, while readiness returns 503 until dependencies are
// usable.
func (r Report) HTTPStatus() int {
	if r.Status == StatusNotReady {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// Probe turns an optional platform Store into health/readiness reports. The
// initErr is intentionally retained separately: callers can continue Legacy
// startup after Open fails while still reporting a degraded platform.
type Probe struct {
	store   *Store
	initErr error
	auth    *AuthRepository
}

func NewProbe(store *Store, initErr error) Probe {
	return Probe{store: store, initErr: initErr}
}

// NewProbeWithAuth adds the P1B authentication state to readiness without
// changing the liveness contract. Setup-required is a valid product state;
// only an unavailable or inconsistent auth repository makes readiness fail.
func NewProbeWithAuth(store *Store, initErr error) Probe {
	probe := NewProbe(store, initErr)
	probe.auth = NewAuthRepository(store)
	return probe
}

func (p Probe) Health(ctx context.Context) Report {
	report := Report{
		Status: StatusHealthy,
		Checks: map[string]Check{
			"process": {Status: CheckOK, Code: "process_alive"},
		},
	}
	if p.store == nil || p.initErr != nil {
		report.Status = StatusDegraded
		report.Checks["sqlite"] = Check{Status: CheckDegraded, Code: "sqlite_initialization_failed"}
		return report
	}
	if err := p.store.Ping(ctx); err != nil {
		report.Status = StatusDegraded
		report.Checks["sqlite"] = Check{Status: CheckDegraded, Code: "sqlite_unavailable"}
		return report
	}
	report.Checks["sqlite"] = Check{Status: CheckOK, Code: "sqlite_alive"}
	if p.auth != nil {
		state, err := p.auth.State(ctx)
		if err != nil {
			report.Status = StatusDegraded
			report.Checks["auth"] = Check{Status: CheckDegraded, Code: "auth_unavailable"}
		} else if state == AuthSetupRequired {
			report.Checks["auth"] = Check{Status: CheckOK, Code: "setup_required"}
		} else {
			report.Checks["auth"] = Check{Status: CheckOK, Code: "auth_ready"}
		}
	}
	return report
}

func (p Probe) Ready(ctx context.Context) Report {
	report := Report{
		Status: StatusReady,
		Checks: map[string]Check{
			"process": {Status: CheckOK, Code: "process_alive"},
		},
	}
	if p.store == nil || p.initErr != nil {
		return notReady(report, "sqlite", "sqlite_initialization_failed")
	}
	if err := p.store.Ping(ctx); err != nil {
		return notReady(report, "sqlite", "sqlite_unavailable")
	}
	report.Checks["sqlite"] = Check{Status: CheckOK, Code: "sqlite_alive"}
	version, err := p.store.SchemaVersion(ctx)
	if err != nil {
		return notReady(report, "schema", "schema_unavailable")
	}
	definitions, err := migrationDefinitions()
	if err != nil || len(definitions) == 0 {
		return notReady(report, "schema", "schema_definition_unavailable")
	}
	expected := definitions[len(definitions)-1].version
	if version != expected {
		return notReady(report, "schema", "schema_version_mismatch")
	}
	report.Checks["schema"] = Check{Status: CheckOK, Code: "schema_current"}
	if p.auth != nil {
		state, err := p.auth.State(ctx)
		if err != nil {
			return notReady(report, "auth", "auth_unavailable")
		}
		if state == AuthSetupRequired {
			report.Checks["auth"] = Check{Status: CheckOK, Code: "setup_required"}
		} else {
			report.Checks["auth"] = Check{Status: CheckOK, Code: "auth_ready"}
		}
	}
	return report
}

func notReady(report Report, name, code string) Report {
	report.Status = StatusNotReady
	report.Checks[name] = Check{Status: CheckNotReady, Code: code}
	return report
}

func (p Probe) Healthz() http.Handler {
	return p.handler(false)
}

func (p Probe) Readyz() http.Handler {
	return p.handler(true)
}

func (p Probe) handler(ready bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var report Report
		if ready {
			report = p.Ready(r.Context())
		} else {
			report = p.Health(r.Context())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(report.HTTPStatus())
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(report)
	})
}
