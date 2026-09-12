// Package replay defines the process-independent public snapshot used by
// manual replay.  It intentionally contains data, not behavior: no renderer,
// Messenger, HTTP client, connector or source response can be serialized.
package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

const SchemaVersion = 1

var (
	ErrInvalidSnapshot = errors.New("replay: invalid snapshot")
	ErrSnapshotExpired = errors.New("replay: snapshot expired")
)

type TargetIdentity struct {
	TargetID       string `json:"target_id"`
	TargetType     string `json:"target_type"`
	ExternalID     string `json:"external_id"`
	ConnectorID    string `json:"connector_id,omitempty"`
	LegacyRouteKey string `json:"legacy_route_key,omitempty"`
}

type Snapshot struct {
	SchemaVersion      int                     `json:"schema_version"`
	RouteDecisionID    string                  `json:"route_decision_id"`
	EventID            string                  `json:"event_id"`
	ObservedEventID    string                  `json:"observed_event_id,omitempty"`
	SourceID           string                  `json:"source_id"`
	Target             TargetIdentity          `json:"target"`
	SubscriptionID     string                  `json:"subscription_id,omitempty"`
	EventType          domain.EventType        `json:"event_type"`
	Title              string                  `json:"title,omitempty"`
	Text               string                  `json:"text,omitempty"`
	RelatedText        string                  `json:"related_text,omitempty"`
	PublicURL          string                  `json:"public_url,omitempty"`
	PublicURLs         []string                `json:"public_urls,omitempty"`
	Media              []domain.MediaReference `json:"media,omitempty"`
	SourceEventAt      *time.Time              `json:"source_event_at,omitempty"`
	NormalizedMetadata map[string]string       `json:"normalized_metadata,omitempty"`
	TemplateInput      json.RawMessage         `json:"template_input"`
	ClassificationRef  string                  `json:"classification_ref,omitempty"`
	CreatedAt          time.Time               `json:"created_at"`
	ExpiresAt          time.Time               `json:"expires_at"`
}

func (s Snapshot) Validate(now time.Time) error {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	if s.SchemaVersion != SchemaVersion || strings.TrimSpace(s.RouteDecisionID) == "" || strings.TrimSpace(s.EventID) == "" || strings.TrimSpace(s.SourceID) == "" || strings.TrimSpace(s.Target.TargetID) == "" || strings.TrimSpace(s.Target.TargetType) == "" || strings.TrimSpace(s.Target.ExternalID) == "" || s.EventType == "" || len(s.TemplateInput) == 0 || !json.Valid(s.TemplateInput) || s.CreatedAt.IsZero() {
		return ErrInvalidSnapshot
	}
	if !s.ExpiresAt.IsZero() && !time.Time(now).IsZero() && !s.ExpiresAt.After(now.UTC()) {
		return ErrSnapshotExpired
	}
	return nil
}

func (s Snapshot) Marshal() ([]byte, error) {
	if err := s.Validate(time.Time{}); err != nil && !errors.Is(err, ErrSnapshotExpired) {
		return nil, err
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	s.PublicURLs = cloneStrings(s.PublicURLs)
	s.Media = append([]domain.MediaReference(nil), s.Media...)
	return json.Marshal(s)
}

func Unmarshal(data []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, err
	}
	if err := s.Validate(time.Time{}); err != nil && !errors.Is(err, ErrSnapshotExpired) {
		return Snapshot{}, err
	}
	return s, nil
}

func FromNormalizedEvent(event domain.NormalizedEvent, routeDecisionID string, target TargetIdentity, subscriptionID, classificationRef string, now time.Time) (Snapshot, error) {
	if event.ID == "" {
		event.ID = event.NormalizedEventID
	}
	if routeDecisionID == "" || target.TargetID == "" {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = event.ObservedAt
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now.UTC()
	}
	input := struct {
		EventID            string                  `json:"event_id"`
		SourceID           string                  `json:"source_id"`
		EventType          domain.EventType        `json:"event_type"`
		Title              string                  `json:"title,omitempty"`
		Text               string                  `json:"text,omitempty"`
		RelatedText        string                  `json:"related_text,omitempty"`
		URL                string                  `json:"url,omitempty"`
		PublicURLs         []string                `json:"public_urls,omitempty"`
		Media              []domain.MediaReference `json:"media,omitempty"`
		AuthorID           string                  `json:"author_id,omitempty"`
		AuthorName         string                  `json:"author_name,omitempty"`
		SourceDisplayName  string                  `json:"source_display_name,omitempty"`
		Truncated          bool                    `json:"truncated,omitempty"`
		NormalizationFlags []string                `json:"normalization_flags,omitempty"`
	}{event.ID, event.SourceID, event.EventType, event.Title, event.Body, event.RelatedBody, event.URL, event.PublicURLs, event.Media, event.AuthorID, event.AuthorName, event.SourceDisplayName, event.Truncated, event.NormalizationFlags}
	raw, err := json.Marshal(input)
	if err != nil {
		return Snapshot{}, err
	}
	var sourceAt *time.Time
	if event.SourceEventAt != nil {
		value := event.SourceEventAt.UTC()
		sourceAt = &value
	}
	created := event.CreatedAt.UTC()
	return Snapshot{SchemaVersion: SchemaVersion, RouteDecisionID: routeDecisionID, EventID: event.ID, ObservedEventID: event.ObservedEventID, SourceID: event.SourceID, Target: target, SubscriptionID: subscriptionID, EventType: event.EventType, Title: event.Title, Text: event.Body, RelatedText: event.RelatedBody, PublicURL: event.URL, PublicURLs: cloneStrings(event.PublicURLs), Media: append([]domain.MediaReference(nil), event.Media...), SourceEventAt: sourceAt, NormalizedMetadata: map[string]string{"normalizer_version": event.NormalizerVersion, "preprocessor_version": event.PreprocessorVersion, "source_display_name": event.SourceDisplayName}, TemplateInput: raw, ClassificationRef: classificationRef, CreatedAt: created, ExpiresAt: created.Add(90 * 24 * time.Hour)}, nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func (s Snapshot) Digest() string {
	raw, err := s.Marshal()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// MediaFallback encodes the fixed replay order. Empty values are skipped; the
// caller performs the actual cache lookup or HTTP fetch under its own bounds.
func MediaFallback(cached, remote, textLink string) []string {
	result := make([]string, 0, 3)
	for _, value := range []string{cached, remote, textLink} {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}
