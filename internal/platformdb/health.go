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

// DependencyChecker is the deliberately small boundary used by optional
// platform components such as Secret Store. Implementations return only a
// stable status/code pair; raw errors and sensitive configuration stay out of
// health/readiness responses.
type DependencyChecker interface {
	Check(context.Context) (CheckStatus, string)
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
	store     *Store
	initErr   error
	auth      *AuthRepository
	secret    DependencyChecker
	migration *MigrationRepository
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

// NewProbeWithAuthAndSecret extends the P1B probe without changing the
// existing constructor or its SQLite/Auth semantics.
func NewProbeWithAuthAndSecret(store *Store, initErr error, secret DependencyChecker) Probe {
	probe := NewProbeWithAuth(store, initErr)
	probe.secret = secret
	return probe
}

// SetMigrationRepository wires the optional connector-migration health view
// without making migrations a prerequisite for the Legacy core. It is an
// explicit owner action; no package-level database handle is introduced.
func (p *Probe) SetMigrationRepository(repository *MigrationRepository) {
	if p == nil {
		return
	}
	p.migration = repository
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
		p.addSecretHealth(ctx, &report)
		p.addMigrationHealth(ctx, &report)
		return report
	}
	if err := p.store.Ping(ctx); err != nil {
		report.Status = StatusDegraded
		report.Checks["sqlite"] = Check{Status: CheckDegraded, Code: "sqlite_unavailable"}
		p.addSecretHealth(ctx, &report)
		p.addMigrationHealth(ctx, &report)
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
	p.addSecretHealth(ctx, &report)
	p.addMigrationHealth(ctx, &report)
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
		report = notReady(report, "sqlite", "sqlite_initialization_failed")
		p.addSecretReadiness(ctx, &report)
		p.addMigrationReadiness(ctx, &report)
		return report
	}
	if err := p.store.Ping(ctx); err != nil {
		report = notReady(report, "sqlite", "sqlite_unavailable")
		p.addSecretReadiness(ctx, &report)
		p.addMigrationReadiness(ctx, &report)
		return report
	}
	report.Checks["sqlite"] = Check{Status: CheckOK, Code: "sqlite_alive"}
	version, err := p.store.SchemaVersion(ctx)
	if err != nil {
		report = notReady(report, "schema", "schema_unavailable")
		p.addSecretReadiness(ctx, &report)
		p.addMigrationReadiness(ctx, &report)
		return report
	}
	definitions, err := migrationDefinitions()
	if err != nil || len(definitions) == 0 {
		report = notReady(report, "schema", "schema_definition_unavailable")
		p.addSecretReadiness(ctx, &report)
		p.addMigrationReadiness(ctx, &report)
		return report
	}
	expected := definitions[len(definitions)-1].version
	if version != expected {
		report = notReady(report, "schema", "schema_version_mismatch")
		p.addSecretReadiness(ctx, &report)
		p.addMigrationReadiness(ctx, &report)
		return report
	}
	report.Checks["schema"] = Check{Status: CheckOK, Code: "schema_current"}
	if p.auth != nil {
		state, err := p.auth.State(ctx)
		if err != nil {
			report = notReady(report, "auth", "auth_unavailable")
			p.addSecretReadiness(ctx, &report)
			p.addMigrationReadiness(ctx, &report)
			return report
		}
		if state == AuthSetupRequired {
			report.Checks["auth"] = Check{Status: CheckOK, Code: "setup_required"}
		} else {
			report.Checks["auth"] = Check{Status: CheckOK, Code: "auth_ready"}
		}
	}
	p.addSecretReadiness(ctx, &report)
	p.addMigrationReadiness(ctx, &report)
	return report
}

func (p Probe) addMigrationHealth(ctx context.Context, report *Report) {
	if p.migration == nil {
		return
	}
	values, err := p.migration.UnfinishedMigrations(ctx)
	if err != nil {
		report.Checks["migration"] = Check{Status: CheckDegraded, Code: "migration_status_unavailable"}
		report.Status = StatusDegraded
		return
	}
	for _, value := range values {
		if value.State == MigrationRecovery {
			report.Checks["migration"] = Check{Status: CheckDegraded, Code: "migration_recovery_required"}
			report.Status = StatusDegraded
			return
		}
	}
	if len(values) > 0 {
		report.Checks["migration"] = Check{Status: CheckOK, Code: "migration_active"}
	} else {
		report.Checks["migration"] = Check{Status: CheckOK, Code: "migration_idle"}
	}
}

func (p Probe) addMigrationReadiness(ctx context.Context, report *Report) {
	if p.migration == nil {
		return
	}
	values, err := p.migration.UnfinishedMigrations(ctx)
	if err != nil {
		// Migration recovery is target-scoped maintenance. Keep core readiness
		// intact while exposing the degraded condition to operators.
		report.Checks["migration"] = Check{Status: CheckDegraded, Code: "migration_status_unavailable"}
		return
	}
	for _, value := range values {
		if value.State == MigrationRecovery {
			report.Checks["migration"] = Check{Status: CheckDegraded, Code: "migration_recovery_required"}
			return
		}
	}
	report.Checks["migration"] = Check{Status: CheckOK, Code: "migration_ready"}
}

func (p Probe) addSecretHealth(ctx context.Context, report *Report) {
	if p.secret == nil {
		return
	}
	status, code := p.secret.Check(ctx)
	report.Checks["secret_store"] = Check{Status: status, Code: code}
	if status != CheckOK {
		report.Status = StatusDegraded
	}
}

func (p Probe) addSecretReadiness(ctx context.Context, report *Report) {
	if p.secret == nil {
		return
	}
	status, code := p.secret.Check(ctx)
	if status != CheckOK {
		*report = notReady(*report, "secret_store", code)
		return
	}
	report.Checks["secret_store"] = Check{Status: CheckOK, Code: code}
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
