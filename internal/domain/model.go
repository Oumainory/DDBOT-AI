// Package domain contains the platform-neutral DDBOT-AI contracts shared by
// adapters, policy evaluation, persistence, and the Dashboard API.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Platform string

const (
	PlatformBilibili Platform = "bilibili"
	PlatformTwitter  Platform = "twitter"
	PlatformWeibo    Platform = "weibo"
	PlatformYouTube  Platform = "youtube"
)

type EventType string

const (
	EventDynamic    EventType = "dynamic"
	EventSubmission EventType = "submission"
	EventTweet      EventType = "tweet"
	EventLive       EventType = "live"
	EventGeneric    EventType = "generic"
)

type TargetType string

const (
	TargetGroup   TargetType = "group"
	TargetChannel TargetType = "channel"
)

type Category string

const (
	CategoryAnnouncement   Category = "announcement"
	CategoryUpdate         Category = "update"
	CategoryMaintenance    Category = "maintenance"
	CategoryIncident       Category = "incident"
	CategoryEvent          Category = "event"
	CategoryPromotion      Category = "promotion"
	CategoryGiveaway       Category = "giveaway"
	CategoryCommunity      Category = "community"
	CategoryRepost         Category = "repost"
	CategoryContentPublish Category = "content_publish"
	CategoryPersonalUpdate Category = "personal_update"
	CategorySchedule       Category = "schedule"
	CategoryPolicyChange   Category = "policy_change"
	CategoryOther          Category = "other"
	CategoryUnknown        Category = "unknown"
)

type Importance string

const (
	ImportanceLow      Importance = "low"
	ImportanceMedium   Importance = "medium"
	ImportanceHigh     Importance = "high"
	ImportanceCritical Importance = "critical"
)

const (
	TagGameVersion    = "game_version"
	TagVersionPreview = "version_preview"
	TagNewCharacter   = "new_character"
	TagNewContent     = "new_content"
	TagGameEvent      = "game_event"
	TagGacha          = "gacha"
	TagMaintenance    = "maintenance"
	TagBug            = "bug"
	TagCompensation   = "compensation"
	TagServiceOutage  = "service_outage"
	TagShutdown       = "shutdown"
	TagDelay          = "delay"
	TagLivestream     = "livestream"
	TagCollaboration  = "collaboration"
	TagMerchandise    = "merchandise"
	TagSecurity       = "security"
	TagBreakingChange = "breaking_change"
)

const (
	FlagOriginal               = "is_original"
	FlagRepost                 = "is_repost"
	FlagQuote                  = "is_quote"
	FlagNewInformation         = "has_new_information"
	FlagTimeSensitive          = "time_sensitive"
	FlagServiceImpact          = "service_impact"
	FlagCommercial             = "commercial"
	FlagRepeated               = "repeated"
	FlagTruncated              = "truncated"
	FlagInsufficientContext    = "insufficient_context"
	FlagPromptInjectionSuspect = "prompt_injection_suspected"
)

type MediaReference struct {
	Kind       string `json:"kind"`
	URL        string `json:"url,omitempty"`
	PreviewURL string `json:"preview_url,omitempty"`
	AltText    string `json:"alt_text,omitempty"`
}

type Source struct {
	ID         string   `json:"id"`
	Platform   Platform `json:"platform"`
	ExternalID string   `json:"external_id"`
	Name       string   `json:"name,omitempty"`
	URL        string   `json:"url,omitempty"`
}

type Target struct {
	ID             string     `json:"id"`
	ConnectorID    string     `json:"connector_id"`
	TargetType     TargetType `json:"target_type"`
	ExternalID     string     `json:"external_id"`
	Name           string     `json:"name,omitempty"`
	LegacyRouteKey string     `json:"legacy_route_key,omitempty"`
}

// IdentityKey is the storage uniqueness key. Target type is intentionally
// part of the key so a connector can expose, for example, a group and a
// channel with the same external identifier without collision.
func (t Target) IdentityKey() string {
	return strings.Join([]string{t.ConnectorID, string(t.TargetType), t.ExternalID}, "\x00")
}

func (t Target) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.ConnectorID) == "" ||
		strings.TrimSpace(string(t.TargetType)) == "" || strings.TrimSpace(t.ExternalID) == "" {
		return errors.New("domain: target id, connector, type, and external id are required")
	}
	return nil
}

type NormalizedEvent struct {
	ID                string           `json:"id"`
	Platform          Platform         `json:"platform"`
	SourceID          string           `json:"source_id"`
	ExternalID        string           `json:"external_id"`
	EventType         EventType        `json:"event_type"`
	Title             string           `json:"title,omitempty"`
	Body              string           `json:"body,omitempty"`
	RelatedBody       string           `json:"related_body,omitempty"`
	URL               string           `json:"url,omitempty"`
	Media             []MediaReference `json:"media,omitempty"`
	ReplayPayload     json.RawMessage  `json:"replay_payload"`
	NormalizerVersion string           `json:"normalizer_version"`
	CreatedAt         time.Time        `json:"created_at"`
}

func (e NormalizedEvent) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("domain: event id is required")
	}
	if e.Platform == "" || e.EventType == "" {
		return errors.New("domain: event platform and event type are required")
	}
	if strings.TrimSpace(e.SourceID) == "" || strings.TrimSpace(e.ExternalID) == "" {
		return errors.New("domain: source id and external id are required")
	}
	if len(e.ReplayPayload) == 0 || !json.Valid(e.ReplayPayload) {
		return errors.New("domain: replay payload must be valid JSON")
	}
	if e.CreatedAt.IsZero() {
		return errors.New("domain: event created_at is required")
	}
	return nil
}

type SemanticResult struct {
	Category   Category   `json:"category"`
	Importance Importance `json:"importance"`
	Tags       []string   `json:"tags,omitempty"`
	Flags      []string   `json:"flags,omitempty"`
	Confidence float64    `json:"confidence"`
	Uncertain  bool       `json:"uncertain"`
	Reason     string     `json:"reason,omitempty"`
}

func (r SemanticResult) Validate() error {
	if r.Category == "" || r.Importance == "" {
		return errors.New("domain: semantic category and importance are required")
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return fmt.Errorf("domain: confidence %v is outside [0,1]", r.Confidence)
	}
	if len([]rune(r.Reason)) > 160 {
		return errors.New("domain: semantic reason exceeds 160 characters")
	}
	return nil
}

type DeliveryStatus string

const (
	DeliveryPlanned          DeliveryStatus = "planned"
	DeliverySending          DeliveryStatus = "sending"
	DeliveryMigrationHeld    DeliveryStatus = "migration_held"
	DeliveryQueued           DeliveryStatus = "queued"
	DeliverySent             DeliveryStatus = "sent"
	DeliveryPartial          DeliveryStatus = "partial"
	DeliveryNotSent          DeliveryStatus = "not_sent"
	DeliveryUnknown          DeliveryStatus = "unknown"
	DeliveryRejected         DeliveryStatus = "rejected"
	DeliveryExpired          DeliveryStatus = "expired"
	DeliveryAbandonedRestart DeliveryStatus = "abandoned_restart"
	DeliverySkippedEmpty     DeliveryStatus = "skipped_empty"
)

// AutoRetryAllowed deliberately excludes unknown and migration_held. Unknown
// means the remote side may have accepted the message, while migration_held
// belongs exclusively to the Migration Coordinator.
func (s DeliveryStatus) AutoRetryAllowed() bool {
	return s == DeliveryNotSent || s == DeliveryQueued
}
