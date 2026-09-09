// Package session contains server-side session token and cookie primitives.
// Persistence belongs to platformdb; this package never stores session state
// and never starts a refresh goroutine.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultCookieName = "ddbot_ai_session"

var ErrInvalidToken = errors.New("session: invalid token")

type CookiePolicy struct {
	Name     string
	Path     string
	Secure   bool
	SameSite http.SameSite
}

func (p CookiePolicy) normalized() CookiePolicy {
	if strings.TrimSpace(p.Name) == "" {
		p.Name = DefaultCookieName
	}
	if p.Path == "" {
		p.Path = "/"
	}
	if p.SameSite == http.SameSiteDefaultMode {
		p.SameSite = http.SameSiteLaxMode
	}
	return p
}

// GenerateToken returns a 256-bit URL-safe token and its SHA-256 lookup hash.
func GenerateToken(reader io.Reader) (raw string, hash string, err error) {
	if reader == nil {
		reader = rand.Reader
	}
	data := make([]byte, 32)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(data)
	return raw, HashToken(raw), nil
}

func HashToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func HashMetadata(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func ParseCookie(r *http.Request, policy CookiePolicy) (string, error) {
	if r == nil {
		return "", ErrInvalidToken
	}
	cookie, err := r.Cookie(policy.normalized().Name)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", ErrInvalidToken
	}
	return cookie.Value, nil
}

func NewCookie(raw string, now, expires time.Time, policy CookiePolicy) *http.Cookie {
	policy = policy.normalized()
	maxAge := int(expires.Sub(now).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     policy.Name,
		Value:    raw,
		Path:     policy.Path,
		HttpOnly: true,
		Secure:   policy.Secure,
		SameSite: policy.SameSite,
		Expires:  expires.UTC(),
		MaxAge:   maxAge,
	}
}

func ClearCookie(now time.Time, policy CookiePolicy) *http.Cookie {
	policy = policy.normalized()
	return &http.Cookie{
		Name:     policy.Name,
		Value:    "",
		Path:     policy.Path,
		HttpOnly: true,
		Secure:   policy.Secure,
		SameSite: policy.SameSite,
		Expires:  now.Add(-time.Hour).UTC(),
		MaxAge:   -1,
	}
}
