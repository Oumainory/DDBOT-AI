// Package normalizer converts the allowlisted public observation snapshot into
// the bounded, deterministic input accepted by the AI Shadow subsystem. It
// intentionally has no network or credential access.
package normalizer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

const (
	SchemaVersion       = 1
	BilibiliVersion     = "bilibili-v1"
	TwitterVersion      = "twitter-v1"
	UnsupportedVersion  = "unsupported-v1"
	PreprocessorVersion = "text-v1"
	MaxInputRunes       = 12000
	MaxTitleRunes       = 512
	MaxRelatedRunes     = 6000
	MaxURLs             = 16
	MaxMedia            = 32
)

var (
	ErrUnsupportedPlatform = errors.New("normalizer: unsupported platform")
	ErrInvalidSnapshot     = errors.New("normalizer: invalid public snapshot")
)

// Input is the only source shape accepted by a normalizer. It is deliberately
// equivalent to the Phase 2 allowlisted public snapshot and has no raw API,
// headers, cookies, credentials, or OneBot payload fields.
type Input struct {
	NormalizedEventID string
	ObservedEventID   string
	Platform          string
	SourceID          string
	SourceDisplayName string
	ExternalID        string
	EventType         string
	AuthorID          string
	AuthorName        string
	Title             string
	Text              string
	RelatedText       string
	URL               string
	PublicURLs        []string
	MediaURLs         []string
	SourceEventAt     *time.Time
	ObservedAt        time.Time
}

// publicSnapshot mirrors observation's fixed allowlist. Unknown JSON fields
// are ignored so a future observation writer cannot accidentally become an AI
// input channel.
type publicSnapshot struct {
	Platform         string   `json:"platform"`
	SourceKind       string   `json:"source_kind"`
	SourceExternalID string   `json:"source_external_id"`
	UpstreamEventID  string   `json:"upstream_event_id"`
	EventType        string   `json:"event_type"`
	SourceEventAt    *int64   `json:"source_event_at"`
	Text             string   `json:"text"`
	URL              string   `json:"url"`
	MediaURLs        []string `json:"media_urls"`
	AuthorID         string   `json:"author_id"`
	AuthorName       string   `json:"author_name"`
}

func NormalizeObserved(record platformdb.ObservedEventRecord) (domain.NormalizedEvent, error) {
	var snapshot publicSnapshot
	decoder := json.NewDecoder(strings.NewReader(record.PublicSnapshotJSON))
	if err := decoder.Decode(&snapshot); err != nil {
		return domain.NormalizedEvent{}, ErrInvalidSnapshot
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return domain.NormalizedEvent{}, ErrInvalidSnapshot
	}
	input := Input{
		NormalizedEventID: stableID(record.ID, normalizerVersion(record.Platform)),
		ObservedEventID:   record.ID,
		Platform:          record.Platform,
		SourceID:          record.SourceExternalID,
		ExternalID:        record.SourceExternalID,
		EventType:         record.EventType,
		AuthorID:          snapshot.AuthorID,
		AuthorName:        snapshot.AuthorName,
		Text:              snapshot.Text,
		URL:               snapshot.URL,
		MediaURLs:         snapshot.MediaURLs,
		ObservedAt:        record.ObservedAt,
	}
	if snapshot.Platform != "" {
		input.Platform = snapshot.Platform
	}
	if snapshot.SourceKind != "" {
		input.SourceID = firstNonEmpty(snapshot.SourceExternalID, record.SourceExternalID)
	}
	if snapshot.SourceExternalID != "" {
		input.ExternalID = snapshot.SourceExternalID
	}
	if snapshot.EventType != "" {
		input.EventType = snapshot.EventType
	}
	// The observation record carries source identity separately from the
	// upstream event identity. Preserve that distinction in the normalized
	// contract; falling back to the observation id keeps older snapshots
	// deterministic without inventing an event id.
	if eventID := firstNonEmpty(snapshot.UpstreamEventID, record.UpstreamEventID, record.ID); eventID != "" {
		input.ExternalID = eventID
	}
	if snapshot.SourceEventAt != nil {
		value := time.Unix(*snapshot.SourceEventAt, 0).UTC()
		input.SourceEventAt = &value
	}
	return Normalize(input)
}

func NormalizeBilibili(input Input) (domain.NormalizedEvent, error) {
	input.Platform = "bilibili"
	return normalizeWithVersion(input, BilibiliVersion)
}

func NormalizeTwitter(input Input) (domain.NormalizedEvent, error) {
	input.Platform = "twitter"
	return normalizeWithVersion(input, TwitterVersion)
}

func Normalize(input Input) (domain.NormalizedEvent, error) {
	switch strings.ToLower(strings.TrimSpace(input.Platform)) {
	case "bilibili":
		return NormalizeBilibili(input)
	case "twitter", "x":
		return NormalizeTwitter(input)
	default:
		return domain.NormalizedEvent{}, ErrUnsupportedPlatform
	}
}

func normalizeWithVersion(input Input, version string) (domain.NormalizedEvent, error) {
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.EventType = strings.TrimSpace(input.EventType)
	if input.SourceID == "" {
		input.SourceID = input.ExternalID
	}
	if input.ExternalID == "" {
		input.ExternalID = input.SourceID
	}
	if input.EventType == "" || input.SourceID == "" || input.ExternalID == "" {
		return domain.NormalizedEvent{}, ErrInvalidSnapshot
	}
	if input.ObservedAt.IsZero() {
		input.ObservedAt = time.Unix(0, 0).UTC()
	} else {
		input.ObservedAt = input.ObservedAt.UTC()
	}
	if input.NormalizedEventID == "" {
		seed := input.ObservedEventID + "|" + input.Platform + "|" + input.EventType + "|" + input.ExternalID
		input.NormalizedEventID = stableID(seed, version)
	}

	title, titleTruncated := bound(strings.TrimSpace(input.Title), MaxTitleRunes)
	text, textTruncated := bound(strings.TrimSpace(input.Text), MaxInputRunes)
	related, relatedTruncated := bound(strings.TrimSpace(input.RelatedText), MaxRelatedRunes)
	// The total prompt input is bounded as well as each component. Preserve the
	// title and primary text first, then trim related text deterministically.
	remaining := MaxInputRunes - utf8.RuneCountInString(title) - utf8.RuneCountInString(text)
	if remaining < 0 {
		text, _ = bound(text, MaxInputRunes-utf8.RuneCountInString(title))
		remaining = 0
	}
	if utf8.RuneCountInString(related) > remaining {
		related, _ = bound(related, remaining)
		relatedTruncated = true
	}
	truncated := titleTruncated || textTruncated || relatedTruncated

	urls := make([]string, 0, MaxURLs)
	for _, value := range append(append([]string{input.URL}, input.PublicURLs...), input.MediaURLs...) {
		value = publicURL(value)
		if value == "" || contains(urls, value) || len(urls) >= MaxURLs {
			continue
		}
		urls = append(urls, value)
	}
	media := make([]domain.MediaReference, 0, min(len(input.MediaURLs), MaxMedia))
	for _, value := range input.MediaURLs {
		if value = publicURL(value); value != "" {
			media = append(media, domain.MediaReference{Kind: "public", URL: value})
			if len(media) == MaxMedia {
				break
			}
		}
	}
	flags := make([]string, 0, 1)
	if truncated {
		flags = append(flags, domain.FlagTruncated)
	}
	eventType := domain.EventType(strings.ToLower(input.EventType))
	created := input.ObservedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	result := domain.NormalizedEvent{
		ID:                  input.NormalizedEventID,
		SchemaVersion:       SchemaVersion,
		NormalizedEventID:   input.NormalizedEventID,
		ObservedEventID:     input.ObservedEventID,
		Platform:            domain.Platform(input.Platform),
		SourceID:            input.SourceID,
		ExternalID:          input.ExternalID,
		SourceDisplayName:   input.SourceDisplayName,
		EventType:           eventType,
		AuthorID:            strings.TrimSpace(input.AuthorID),
		AuthorName:          strings.TrimSpace(input.AuthorName),
		Title:               title,
		Body:                text,
		RelatedBody:         related,
		URL:                 firstNonEmpty(urls...),
		PublicURLs:          urls,
		Media:               media,
		SourceEventAt:       input.SourceEventAt,
		ObservedAt:          input.ObservedAt,
		ReplayPayload:       nil,
		NormalizerVersion:   version,
		PreprocessorVersion: PreprocessorVersion,
		Truncated:           truncated,
		NormalizationFlags:  flags,
		CreatedAt:           created,
	}
	// ReplayPayload is a bounded, public-only representation used for audit and
	// evaluation. It is not sent to the provider as a raw source response.
	payload, _ := json.Marshal(struct {
		Platform string `json:"platform"`
		SourceID string `json:"source_id"`
		Event    string `json:"event_type"`
		Title    string `json:"title,omitempty"`
		Text     string `json:"text,omitempty"`
		URL      string `json:"url,omitempty"`
	}{input.Platform, input.SourceID, string(eventType), title, text, firstNonEmpty(urls...)})
	result.ReplayPayload = payload
	if err := result.Validate(); err != nil {
		return domain.NormalizedEvent{}, err
	}
	return result, nil
}

func normalizerVersion(platform string) string {
	if strings.EqualFold(platform, "twitter") || strings.EqualFold(platform, "x") {
		return TwitterVersion
	}
	if strings.EqualFold(platform, "bilibili") {
		return BilibiliVersion
	}
	return UnsupportedVersion
}

func stableID(seed, version string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(seed) + "|" + version))
	return "norm_" + hex.EncodeToString(sum[:16])
}

func bound(value string, max int) (string, bool) {
	if max <= 0 {
		if value == "" {
			return "", false
		}
		return "", true
	}
	if utf8.RuneCountInString(value) <= max {
		return value, false
	}
	return string([]rune(value)[:max]), true
}

func publicURL(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	// URLs are public metadata, but source APIs sometimes append bearer-like
	// query parameters or userinfo. Never carry those values into a model
	// prompt or durable normalized snapshot. Keep ordinary query parameters
	// because they can be part of a public canonical URL.
	if parsed.User != nil {
		return ""
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key") || strings.Contains(lower, "auth") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || lower == "sig" || strings.Contains(lower, "signature") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
