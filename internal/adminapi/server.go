// Package adminapi owns the DDBOT-AI platform API boundary. Legacy /api/v1
// handlers remain in admin; this package only adds the authoritative /api/v2
// shell and its reusable authenticated read/mutation boundaries.
package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/adminauth"
	"github.com/Oumainory/DDBOT-AI/internal/auth"
	"github.com/Oumainory/DDBOT-AI/internal/buildinfo"
	"github.com/Oumainory/DDBOT-AI/internal/csrf"
	"github.com/Oumainory/DDBOT-AI/internal/discovery"
	"github.com/Oumainory/DDBOT-AI/internal/enforce"
	"github.com/Oumainory/DDBOT-AI/internal/evaluation"
	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
	"github.com/Oumainory/DDBOT-AI/internal/mediacache"
	"github.com/Oumainory/DDBOT-AI/internal/migration"
	"github.com/Oumainory/DDBOT-AI/internal/observation"
	"github.com/Oumainory/DDBOT-AI/internal/origin"
	"github.com/Oumainory/DDBOT-AI/internal/pairing"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
	"github.com/Oumainory/DDBOT-AI/internal/replay"
	"github.com/Oumainory/DDBOT-AI/internal/secretstore"
	"github.com/Oumainory/DDBOT-AI/internal/session"
	"github.com/Oumainory/DDBOT-AI/internal/shadow"
	"github.com/Oumainory/DDBOT-AI/lsp/subscription"
	"go.uber.org/atomic"
)

type Config struct {
	Auth                  *auth.Service
	Probe                 *platformdb.Probe
	Origin                origin.Policy
	RequireOrigin         bool
	Cookie                session.CookiePolicy
	LegacyOnline          *atomic.Bool
	Build                 buildinfo.Info
	ObservationRepository *platformdb.ObservationRepository
	ObservationRecorder   *observation.Recorder
	DomainRepository      *platformdb.DomainRepository
	LegacySubscriptions   *subscription.Service
	Idempotency           *idempotency.MemoryStore
	MigrationRepository   *platformdb.MigrationRepository
	MigrationCoordinator  *migration.Coordinator
	PairingService        *pairing.Service
	PairingVerifier       pairing.Verifier
	BilibiliResolver      discovery.BilibiliResolver
	TwitterResolver       discovery.TwitterResolver
	Now                   func() time.Time
	AIRepository          *platformdb.AIRepository
	SecretStore           *secretstore.Service
	AIProvider            *provider.Swappable
	ShadowRuntime         *shadow.Runtime
	EvaluationRunner      *evaluation.Runner
	Phase5Repository      *platformdb.Phase5Repository
	EnforceRuntime        *enforce.Runtime
	ReplayService         *replay.Service
	MediaCache            *mediacache.Cache
}

type Server struct {
	auth                  *auth.Service
	probe                 *platformdb.Probe
	origin                origin.Policy
	cookie                session.CookiePolicy
	legacy                *atomic.Bool
	build                 buildinfo.Info
	now                   func() time.Time
	authHandler           http.Handler
	observationRepository *platformdb.ObservationRepository
	observationRecorder   *observation.Recorder
	domainRepository      *platformdb.DomainRepository
	legacySubscriptions   *subscription.Service
	idempotency           *idempotency.MemoryStore
	migrationRepository   *platformdb.MigrationRepository
	migrationCoordinator  *migration.Coordinator
	pairingService        *pairing.Service
	pairingVerifier       pairing.Verifier
	bilibiliResolver      discovery.BilibiliResolver
	twitterResolver       discovery.TwitterResolver
	aiRepository          *platformdb.AIRepository
	secretStore           *secretstore.Service
	aiProvider            *provider.Swappable
	shadowRuntime         *shadow.Runtime
	evaluationRunner      *evaluation.Runner
	phase5Repository      *platformdb.Phase5Repository
	enforceRuntime        *enforce.Runtime
	replayService         *replay.Service
	mediaCache            *mediacache.Cache
	testMu                sync.Mutex
	testLast              map[string]time.Time
	aiTestLast            map[string]time.Time
}

type Principal struct {
	AdminID   string
	Username  string
	ExpiresAt time.Time
	csrfToken string
}

type contextKey struct{}

type apiEnvelope struct {
	Data      any       `json:"data,omitempty"`
	Meta      any       `json:"meta,omitempty"`
	Error     *apiError `json:"error,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ComponentStatus struct {
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

type ProductStatus struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Overview struct {
	Product  ProductStatus `json:"product"`
	Platform struct {
		LegacyCore  ComponentStatus `json:"legacy_core"`
		AdminAPI    ComponentStatus `json:"admin_api"`
		SQLite      ComponentStatus `json:"sqlite"`
		SecretStore ComponentStatus `json:"secret_store"`
		Auth        ComponentStatus `json:"auth"`
		AI          ComponentStatus `json:"ai"`
	} `json:"platform"`
}

type About struct {
	ProductName      string `json:"product_name"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	BuildTime        string `json:"build_time"`
	SourceRepository string `json:"source_repository"`
	CommitURL        string `json:"commit_url,omitempty"`
	License          string `json:"license"`
	LicenseName      string `json:"license_name"`
}

func NewServer(config Config) (*Server, error) {
	if config.Auth == nil {
		return nil, auth.ErrUnavailable
	}
	if config.Build.ProductName == "" {
		config.Build = buildinfo.Current()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.LegacySubscriptions == nil {
		config.LegacySubscriptions = subscription.NewService()
	}
	if config.Idempotency == nil {
		config.Idempotency = idempotency.NewMemoryStore(idempotency.DefaultRetention)
	}
	if config.BilibiliResolver == nil {
		config.BilibiliResolver = discovery.StaticBilibiliResolver{}
	}
	if config.TwitterResolver == nil {
		config.TwitterResolver = discovery.StaticTwitterResolver{}
	}
	if config.MigrationRepository == nil && config.DomainRepository != nil {
		config.MigrationRepository = config.DomainRepository.MigrationRepository()
	}
	if config.MigrationCoordinator == nil && config.MigrationRepository != nil && config.DomainRepository != nil {
		config.MigrationCoordinator = migration.NewCoordinator(migration.Config{Repository: config.MigrationRepository, Domain: config.DomainRepository, Now: config.Now})
	}
	if config.PairingService == nil && config.MigrationRepository != nil && config.DomainRepository != nil {
		config.PairingService = pairing.New(pairing.Config{Repository: config.MigrationRepository, Domain: config.DomainRepository, Now: config.Now})
	}
	if config.MigrationRepository != nil && config.LegacySubscriptions != nil {
		config.LegacySubscriptions.SetMutationGate(func(ctx context.Context, groupCode int64) error {
			frozen, err := config.MigrationRepository.FrozenLegacyGroup(ctx, groupCode)
			if err != nil {
				return nil
			} // platform degradation remains fail-open
			if frozen {
				return platformdb.ErrMigrationInProgress
			}
			return nil
		})
	}
	if config.RequireOrigin {
		config.Origin.AllowMissing = false
	} else {
		config.Origin.AllowMissing = true
	}
	authServer, err := adminauth.NewServer(adminauth.Config{
		Auth:          config.Auth,
		Probe:         config.Probe,
		Origin:        config.Origin,
		RequireOrigin: config.RequireOrigin,
		Cookie:        config.Cookie,
	})
	if err != nil {
		return nil, err
	}
	return &Server{
		auth:                  config.Auth,
		probe:                 config.Probe,
		origin:                config.Origin,
		cookie:                config.Cookie,
		legacy:                config.LegacyOnline,
		build:                 config.Build,
		now:                   config.Now,
		observationRepository: config.ObservationRepository,
		observationRecorder:   config.ObservationRecorder,
		domainRepository:      config.DomainRepository,
		legacySubscriptions:   config.LegacySubscriptions,
		idempotency:           config.Idempotency,
		migrationRepository:   config.MigrationRepository,
		migrationCoordinator:  config.MigrationCoordinator,
		pairingService:        config.PairingService,
		pairingVerifier:       config.PairingVerifier,
		bilibiliResolver:      config.BilibiliResolver,
		twitterResolver:       config.TwitterResolver,
		aiRepository:          config.AIRepository,
		secretStore:           config.SecretStore,
		aiProvider:            config.AIProvider,
		shadowRuntime:         config.ShadowRuntime,
		evaluationRunner:      config.EvaluationRunner,
		phase5Repository:      config.Phase5Repository,
		enforceRuntime:        config.EnforceRuntime,
		replayService:         config.ReplayService,
		mediaCache:            config.MediaCache,
		testLast:              make(map[string]time.Time),
		aiTestLast:            make(map[string]time.Time),
		authHandler:           authServer.Handler(),
	}, nil
}

// Handler routes only /api/v2. Unknown API paths receive the JSON envelope and
// never fall through to the SPA index.
func (s *Server) Handler() http.Handler {
	if s == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2") {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/v2/setup", "/api/v2/setup/status", "/api/v2/auth/login", "/api/v2/auth/session", "/api/v2/auth/logout":
			s.authHandler.ServeHTTP(w, r)
		case "/api/v2/platform/overview":
			s.RequireAuth(http.HandlerFunc(s.handleOverview)).ServeHTTP(w, r)
		case "/api/v2/about":
			s.RequireAuth(http.HandlerFunc(s.handleAbout)).ServeHTTP(w, r)
		case "/api/v2/observations/events":
			s.RequireAuth(http.HandlerFunc(s.handleObservationEvents)).ServeHTTP(w, r)
		case "/api/v2/observations/summary":
			s.RequireAuth(http.HandlerFunc(s.handleObservationSummary)).ServeHTTP(w, r)
		case "/api/v2/sources":
			s.handleSources(w, r)
		case "/api/v2/targets":
			s.handleTargets(w, r)
		case "/api/v2/connectors":
			s.handleConnectors(w, r)
		case "/api/v2/connector-migrations":
			s.handleMigrations(w, r)
		case "/api/v2/subscriptions":
			s.handleSubscriptions(w, r)
		case "/api/v2/subscriptions/rebuild-projection":
			s.handleRebuildProjection(w, r)
		case "/api/v2/discovery/bilibili/search":
			s.handleBilibiliSearch(w, r)
		case "/api/v2/discovery/bilibili/resolve":
			s.handleBilibiliResolve(w, r)
		case "/api/v2/discovery/twitter/resolve":
			s.handleTwitterResolve(w, r)
		case "/api/v2/ai/provider":
			s.handleAIProvider(w, r)
		case "/api/v2/ai/provider/test":
			s.handleAIProviderTest(w, r)
		case "/api/v2/ai/releases":
			s.handleAIReleases(w, r)
		case "/api/v2/ai/profiles":
			s.handleAIProfiles(w, r)
		case "/api/v2/ai/policy/global":
			s.handleAIPolicy(w, r, "global", "")
		case "/api/v2/ai/shadow/decisions":
			s.handleAIDecisions(w, r)
		case "/api/v2/ai/shadow/summary":
			s.handleAIShadowSummary(w, r)
		case "/api/v2/ai/evaluation/cases":
			s.handleAIEvaluationCases(w, r)
		case "/api/v2/ai/evaluation/runs":
			s.handleAIEvaluationRuns(w, r)
		case "/api/v2/ai/enforce-readiness":
			s.handleAIEnforceReadiness(w, r)
		default:
			if s.handlePhase5Subresource(w, r) {
				return
			}
			if s.handleMigrationSubresource(w, r) {
				return
			}
			if s.handlePairingSubresource(w, r) {
				return
			}
			if s.handleDomainSubresource(w, r) {
				return
			}
			if s.handleAISubresource(w, r) {
				return
			}
			parts := observationPathParts(r.URL.Path)
			if len(parts) == 2 && parts[1] == "routes" {
				s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					s.handleObservationRoutes(w, r, parts[0])
				})).ServeHTTP(w, r)
				return
			}
			if len(parts) == 2 && parts[1] == "deliveries" {
				s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					s.handleObservationDeliveries(w, r, parts[0])
				})).ServeHTTP(w, r)
				return
			}
			if len(parts) == 1 {
				s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					s.handleObservationEventDetail(w, r, parts[0])
				})).ServeHTTP(w, r)
				return
			}
			s.writeError(w, http.StatusNotFound, "not_found", "resource not found")
		}
	})
}

// RequireAuth is reusable by future read-only Domain APIs. The context
// principal contains no raw session token; the token is hashed inside auth.
func (s *Server) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.auth == nil {
			s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
			return
		}
		rawToken, err := session.ParseCookie(r, s.cookie)
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		record, err := s.auth.LookupSession(r.Context(), rawToken)
		if err != nil {
			if errors.Is(err, auth.ErrUnavailable) {
				s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
			} else {
				s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			}
			return
		}
		principal := Principal{AdminID: record.AdminID, Username: record.Username, ExpiresAt: record.ExpiresAt, csrfToken: record.CSRFSecret}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, principal)))
	})
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok
}

// RequireMutation combines the common authenticated mutation boundary. It is
// intentionally not used for setup/login, whose independent P1B contracts do
// not require a session or CSRF token.
func (s *Server) RequireMutation(next http.Handler) http.Handler {
	return s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		policy := s.origin
		policy.AllowMissing = false
		if err := policy.Validate(r); err != nil {
			s.writeError(w, http.StatusForbidden, "origin_rejected", "request origin rejected")
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || !csrf.Validate(principal.csrfToken, r.Header.Get("X-CSRF-Token")) {
			s.writeError(w, http.StatusForbidden, "csrf_rejected", "csrf validation failed")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	info := s.build
	result := Overview{Product: ProductStatus{Name: info.ProductName, Version: info.Version, Commit: info.Commit}}
	if s.legacy == nil {
		result.Platform.LegacyCore = ComponentStatus{Status: "unknown", Code: "legacy_status_unknown"}
	} else if s.legacy.Load() {
		result.Platform.LegacyCore = ComponentStatus{Status: "available", Code: "legacy_online"}
	} else {
		result.Platform.LegacyCore = ComponentStatus{Status: "degraded", Code: "legacy_offline"}
	}
	result.Platform.AdminAPI = ComponentStatus{Status: "available", Code: "admin_api_ready"}
	if s.probe == nil {
		result.Platform.SQLite = ComponentStatus{Status: "unknown", Code: "sqlite_status_unknown"}
		result.Platform.SecretStore = ComponentStatus{Status: "unknown", Code: "secret_store_status_unknown"}
		result.Platform.Auth = ComponentStatus{Status: "unknown", Code: "auth_status_unknown"}
	} else {
		report := s.probe.Health(r.Context())
		result.Platform.SQLite = component(report.Checks["sqlite"])
		result.Platform.SecretStore = component(report.Checks["secret_store"])
		result.Platform.Auth = component(report.Checks["auth"])
	}
	result.Platform.AI = s.aiComponentStatus(r.Context())
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: result})
}

func (s *Server) aiComponentStatus(ctx context.Context) ComponentStatus {
	if s == nil || s.aiRepository == nil {
		return ComponentStatus{Status: "unavailable", Code: "ai_unavailable"}
	}
	if _, err := s.aiRepository.Provider(ctx); err != nil {
		if errors.Is(err, platformdb.ErrAIProviderNotFound) {
			return ComponentStatus{Status: "degraded", Code: "ai_provider_unconfigured"}
		}
		return ComponentStatus{Status: "degraded", Code: "ai_provider_unavailable"}
	}
	if s.shadowRuntime == nil {
		return ComponentStatus{Status: "degraded", Code: "ai_shadow_disabled"}
	}
	if s.aiProvider == nil || s.aiProvider.Get() == nil {
		return ComponentStatus{Status: "degraded", Code: "ai_provider_unavailable"}
	}
	return ComponentStatus{Status: "available", Code: "ai_shadow_ready"}
}

func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	info := s.build
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: About{
		ProductName: info.ProductName, Version: info.Version, Commit: info.Commit,
		BuildTime: info.BuildTime, SourceRepository: info.SourceRepository,
		CommitURL: info.CommitURL(), License: info.License, LicenseName: info.LicenseName,
	}})
}

func component(check platformdb.Check) ComponentStatus {
	if check.Code == "" {
		return ComponentStatus{Status: "unknown", Code: "status_unknown"}
	}
	status := "unknown"
	switch check.Status {
	case platformdb.CheckOK:
		status = "available"
	case platformdb.CheckDegraded:
		if check.Code == "secret_store_recovery" {
			status = "recovery"
		} else if check.Code == "secret_store_unavailable" || check.Code == "sqlite_initialization_failed" || check.Code == "sqlite_unavailable" || check.Code == "auth_unavailable" {
			status = "unavailable"
		} else {
			status = "degraded"
		}
	case platformdb.CheckNotReady:
		status = "unavailable"
	}
	return ComponentStatus{Status: status, Code: check.Code}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, apiEnvelope{Error: &apiError{Code: code, Message: message}, RequestID: requestID()})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value apiEnvelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "request-unknown"
	}
	return hex.EncodeToString(data)
}
