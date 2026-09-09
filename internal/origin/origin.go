// Package origin validates browser Origin headers for state-changing
// requests. Forwarded headers are ignored unless a caller explicitly opts in.
package origin

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

var (
	ErrMissing   = errors.New("origin: missing origin")
	ErrRejected  = errors.New("origin: rejected origin")
	ErrMalformed = errors.New("origin: malformed origin")
)

type Policy struct {
	AllowedOrigin  string
	TrustForwarded bool
	AllowMissing   bool
}

func (p Policy) Validate(r *http.Request) error {
	if r == nil {
		return ErrRejected
	}
	raw := strings.TrimSpace(r.Header.Get("Origin"))
	if raw == "" {
		if p.AllowMissing {
			return nil
		}
		return ErrMissing
	}
	if raw == "null" {
		return ErrRejected
	}
	origin, err := normalize(raw)
	if err != nil {
		return err
	}
	allowed := strings.TrimSpace(p.AllowedOrigin)
	if allowed == "" {
		allowed = requestOrigin(r, p.TrustForwarded)
	}
	if allowed == "" {
		return ErrRejected
	}
	allowed, err = normalize(allowed)
	if err != nil {
		return ErrRejected
	}
	if origin != allowed {
		return ErrRejected
	}
	return nil
}

func normalize(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrMalformed
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func requestOrigin(r *http.Request, trustForwarded bool) string {
	scheme := "http"
	host := r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if trustForwarded {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
			scheme = forwarded
		}
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" {
			host = forwarded
		}
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
