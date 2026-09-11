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
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/adminauth"
	"github.com/cnxysoft/DDBOT-WSa/internal/auth"
	"github.com/cnxysoft/DDBOT-WSa/internal/buildinfo"
	"github.com/cnxysoft/DDBOT-WSa/internal/csrf"
	"github.com/cnxysoft/DDBOT-WSa/internal/observation"
	"github.com/cnxysoft/DDBOT-WSa/internal/origin"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/session"
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
	Now                   func() time.Time
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
		default:
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
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: result})
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
