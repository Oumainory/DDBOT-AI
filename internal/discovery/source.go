// Package discovery contains input normalization and narrow source discovery
// contracts. It never performs a generic fetch of a user supplied URL.
package discovery

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

var (
	ErrInvalidProfile    = errors.New("discovery: invalid profile")
	ErrSearchUnavailable = errors.New("discovery: search unavailable")
)

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

func BilibiliUID(value string) (string, string, error) {
	value = strings.TrimSpace(strings.Trim(value, `"`))
	if digitsOnly.MatchString(value) {
		return value, "https://space.bilibili.com/" + value, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", ErrInvalidProfile
	}
	host := strings.ToLower(u.Hostname())
	if host != "bilibili.com" && host != "www.bilibili.com" && host != "space.bilibili.com" {
		return "", "", ErrInvalidProfile
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "space.bilibili.com" {
		parts = parts[1:]
	}
	if len(parts) == 2 && parts[0] == "space" {
		parts = parts[1:]
	}
	if len(parts) != 1 || !digitsOnly.MatchString(parts[0]) {
		return "", "", ErrInvalidProfile
	}
	return parts[0], "https://space.bilibili.com/" + parts[0], nil
}

func TwitterHandle(value string) (string, string, error) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	if value == "" {
		return "", "", ErrInvalidProfile
	}
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", "", ErrInvalidProfile
		}
		host := strings.ToLower(u.Hostname())
		if host != "x.com" && host != "www.x.com" && host != "twitter.com" && host != "www.twitter.com" {
			return "", "", ErrInvalidProfile
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 1 || parts[0] == "" {
			return "", "", ErrInvalidProfile
		}
		value = parts[0]
	}
	if value == "" || strings.ContainsAny(value, " /?#@:/") || len(value) > 64 {
		return "", "", ErrInvalidProfile
	}
	return value, "https://x.com/" + value, nil
}

type BilibiliCandidate struct {
	UID         string `json:"uid"`
	Name        string `json:"name"`
	Avatar      string `json:"avatar,omitempty"`
	ProfileURL  string `json:"profile_url"`
	Description string `json:"description,omitempty"`
}

type BilibiliResolver interface {
	Search(ctx context.Context, query string) ([]BilibiliCandidate, error)
	Resolve(ctx context.Context, value string) (BilibiliCandidate, error)
}

type TwitterResolver interface {
	Resolve(ctx context.Context, value string) (domain.Source, error)
}

// StaticBilibiliResolver provides safe exact resolution without making a
// generic request. Search is intentionally an injectable capability because
// the production Bilibili client owns credentials/rate limiting.
type StaticBilibiliResolver struct{}

func (StaticBilibiliResolver) Search(_ context.Context, query string) ([]BilibiliCandidate, error) {
	uid, profile, err := BilibiliUID(query)
	if err == nil {
		return []BilibiliCandidate{{UID: uid, ProfileURL: profile}}, nil
	}
	return nil, ErrSearchUnavailable
}

func (StaticBilibiliResolver) Resolve(_ context.Context, value string) (BilibiliCandidate, error) {
	uid, profile, err := BilibiliUID(value)
	if err != nil {
		return BilibiliCandidate{}, err
	}
	return BilibiliCandidate{UID: uid, ProfileURL: profile}, nil
}

type StaticTwitterResolver struct{}

func (StaticTwitterResolver) Resolve(_ context.Context, value string) (domain.Source, error) {
	handle, profile, err := TwitterHandle(value)
	if err != nil {
		return domain.Source{}, err
	}
	return domain.Source{Platform: domain.Platform("twitter"), ExternalID: handle, Handle: handle, CanonicalURL: profile, Status: domain.SourceActive, MetadataJSON: `{"identity_kind":"handle"}`}, nil
}
