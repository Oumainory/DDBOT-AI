// Package adminauth exposes only the P1B bootstrap/auth endpoints. It is
// intentionally separate from the legacy /api/v1 admin server and contains no
// subscription, connector, AI, or Dashboard business API.
package adminauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/auth"
	"github.com/Oumainory/DDBOT-AI/internal/csrf"
	"github.com/Oumainory/DDBOT-AI/internal/origin"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/session"
)

const DefaultMaxBodyBytes int64 = 64 * 1024

type Config struct {
	Auth          *auth.Service
	Probe         *platformdb.Probe
	Origin        origin.Policy
	RequireOrigin bool
	Cookie        session.CookiePolicy
	MaxBodyBytes  int64
	Now           func() time.Time
}

type Server struct {
	auth         *auth.Service
	probe        *platformdb.Probe
	origin       origin.Policy
	cookie       session.CookiePolicy
	maxBodyBytes int64
	now          func() time.Time
	mux          *http.ServeMux
}

type setupRequest struct {
	SetupToken string `json:"setup_token"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type responseEnvelope struct {
	Data      any       `json:"data,omitempty"`
	Meta      any       `json:"meta,omitempty"`
	Error     *apiError `json:"error,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewServer(config Config) (*Server, error) {
	if config.Auth == nil {
		return nil, auth.ErrUnavailable
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RequireOrigin {
		config.Origin.AllowMissing = false
	} else {
		// The contract validates an Origin when a browser sends one; clients
		// without an Origin are not treated as cross-site by assumption.
		config.Origin.AllowMissing = true
	}
	return &Server{
		auth:         config.Auth,
		probe:        config.Probe,
		origin:       config.Origin,
		cookie:       config.Cookie,
		maxBodyBytes: config.MaxBodyBytes,
		now:          config.Now,
		mux:          http.NewServeMux(),
	}, nil
}

func (s *Server) Handler() http.Handler {
	if s == nil {
		return http.NotFoundHandler()
	}
	// Build a fresh mux for each call so embedders may request independent
	// handlers without duplicate registration panics.
	s.mux = http.NewServeMux()
	if s.probe != nil {
		s.mux.Handle("/healthz", s.probe.Healthz())
		s.mux.Handle("/readyz", s.probe.Readyz())
	}
	s.mux.HandleFunc("/api/v2/setup", s.handleSetup)
	s.mux.HandleFunc("/api/v2/setup/status", s.handleSetupStatus)
	s.mux.HandleFunc("/api/v2/auth/login", s.handleLogin)
	s.mux.HandleFunc("/api/v2/auth/logout", s.handleLogout)
	s.mux.HandleFunc("/api/v2/auth/session", s.handleSession)
	return s.mux
}

func (s *Server) Bootstrap(ctx context.Context) (auth.BootstrapResult, error) {
	if s == nil || s.auth == nil {
		return auth.BootstrapResult{}, auth.ErrUnavailable
	}
	return s.auth.Bootstrap(ctx)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := s.validateOrigin(r, true); err != nil {
		s.writeError(w, http.StatusForbidden, "origin_rejected", "request origin rejected")
		return
	}
	var request setupRequest
	if !s.decodeJSON(w, r, &request) {
		return
	}
	result, err := s.auth.Setup(r.Context(), request.SetupToken, request.Username, request.Password)
	if err != nil {
		s.writeAuthError(w, err, true)
		return
	}
	s.writeJSON(w, http.StatusCreated, responseEnvelope{Data: map[string]any{
		"setup_complete": true,
		"username":       result.Admin.Username,
	}})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, http.MethodGet)
		return
	}
	state, err := s.auth.State(r.Context())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, responseEnvelope{Data: map[string]any{
		"state": string(state),
	}})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := s.validateOrigin(r, false); err != nil {
		s.writeError(w, http.StatusForbidden, "origin_rejected", "request origin rejected")
		return
	}
	var request loginRequest
	if !s.decodeJSON(w, r, &request) {
		return
	}
	result, err := s.auth.Login(r.Context(), request.Username, request.Password, remoteAddress(r), r.UserAgent())
	if err != nil {
		s.writeAuthError(w, err, false)
		return
	}
	http.SetCookie(w, session.NewCookie(result.SessionToken, s.now(), result.ExpiresAt, s.cookie))
	s.writeJSON(w, http.StatusOK, responseEnvelope{Data: map[string]any{
		"authenticated": true,
		"username":      result.Admin.Username,
		"csrf_token":    result.CSRFToken,
		"expires_at":    result.ExpiresAt.UTC().Format(time.RFC3339),
	}})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, http.MethodGet)
		return
	}
	rawToken, err := session.ParseCookie(r, s.cookie)
	if err != nil {
		s.writeJSON(w, http.StatusOK, responseEnvelope{Data: map[string]any{"authenticated": false}})
		return
	}
	record, err := s.auth.LookupSession(r.Context(), rawToken)
	if err != nil {
		if errors.Is(err, auth.ErrUnavailable) {
			s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, responseEnvelope{Data: map[string]any{"authenticated": false}})
		return
	}
	s.writeJSON(w, http.StatusOK, responseEnvelope{Data: map[string]any{
		"authenticated": true,
		"username":      record.Username,
		"csrf_token":    record.CSRFSecret,
		"expires_at":    record.ExpiresAt.UTC().Format(time.RFC3339),
	}})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := s.validateOrigin(r, true); err != nil {
		s.writeError(w, http.StatusForbidden, "origin_rejected", "request origin rejected")
		return
	}
	rawToken, err := session.ParseCookie(r, s.cookie)
	if err != nil {
		http.SetCookie(w, session.ClearCookie(s.now(), s.cookie))
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	record, err := s.auth.LookupSession(r.Context(), rawToken)
	if err != nil {
		http.SetCookie(w, session.ClearCookie(s.now(), s.cookie))
		if errors.Is(err, auth.ErrUnavailable) {
			s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
		} else {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		}
		return
	}
	if !csrf.Validate(record.CSRFSecret, r.Header.Get("X-CSRF-Token")) {
		s.writeError(w, http.StatusForbidden, "csrf_rejected", "csrf validation failed")
		return
	}
	if err := s.auth.Logout(r.Context(), rawToken); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
		return
	}
	http.SetCookie(w, session.ClearCookie(s.now(), s.cookie))
	s.writeJSON(w, http.StatusOK, responseEnvelope{Data: map[string]any{"authenticated": false}})
}

func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json") {
		s.writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "content type must be application/json")
		return false
	}
	body, readErr := io.ReadAll(io.LimitReader(r.Body, s.maxBodyBytes+1))
	if readErr != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	if int64(len(body)) > s.maxBodyBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	return true
}

func (s *Server) validateOrigin(r *http.Request, required bool) error {
	policy := s.origin
	if required {
		policy.AllowMissing = false
	}
	return policy.Validate(r)
}

func (s *Server) writeAuthError(w http.ResponseWriter, err error, setup bool) {
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		s.writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is invalid")
	case errors.Is(err, auth.ErrUnavailable), errors.Is(err, platformdb.ErrDatabaseClosed):
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable")
	case setup && (errors.Is(err, platformdb.ErrSetupComplete) || errors.Is(err, platformdb.ErrAdminExists)):
		s.writeError(w, http.StatusConflict, "setup_complete", "setup is already complete")
	case setup && (errors.Is(err, platformdb.ErrSetupTokenInvalid) || errors.Is(err, platformdb.ErrSetupTokenExpired) || errors.Is(err, platformdb.ErrSetupTokenConsumed) || errors.Is(err, platformdb.ErrSetupTokenMissing)):
		s.writeError(w, http.StatusUnauthorized, "invalid_setup_token", "setup token is invalid")
	case setup && (errors.Is(err, auth.ErrPasswordPolicy) || errors.Is(err, auth.ErrUsernamePolicy)):
		s.writeError(w, http.StatusBadRequest, "invalid_setup_input", "setup input is invalid")
	default:
		s.writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
	}
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, responseEnvelope{Error: &apiError{Code: code, Message: message}, RequestID: requestID()})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value responseEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestID() string {
	data := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "request-unknown"
	}
	return hex.EncodeToString(data)
}

func remoteAddress(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	if remote == "" {
		return "unknown"
	}
	return remote
}
