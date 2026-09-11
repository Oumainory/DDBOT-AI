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
	ID           string   `json:"id"`
	Platform     Platform `json:"platform"`
	ExternalID   string   `json:"external_id"`
	Name         string   `json:"name,omitempty"`
	URL          string   `json:"url,omitempty"`
	Handle       string   `json:"handle,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	CanonicalURL string   `json:"canonical_url,omitempty"`
	Status       string   `json:"status,omitempty"`
	MetadataJSON string   `json:"metadata_json,omitempty"`
	CreatedAt    int64    `json:"created_at,omitempty"`
	UpdatedAt    int64    `json:"updated_at,omitempty"`
}

type Target struct {
	ID             string     `json:"id"`
	ConnectorID    string     `json:"connector_id"`
	TargetType     TargetType `json:"target_type"`
	ExternalID     string     `json:"external_id"`
	Name           string     `json:"name,omitempty"`
	LegacyRouteKey string     `json:"legacy_route_key,omitempty"`
	DisplayName    string     `json:"display_name,omitempty"`
	MetadataJSON   string     `json:"metadata_json,omitempty"`
	Status         string     `json:"status,omitempty"`
	CreatedAt      int64      `json:"created_at,omitempty"`
	UpdatedAt      int64      `json:"updated_at,omitempty"`
}

const (
	SourceActive      = "active"
	SourceUnresolved  = "unresolved"
	SourceUnavailable = "unavailable"

	ConnectorOneBot   = "onebot"
	ConnectorSatori   = "satori"
	ConnectorTelegram = "telegram"
	ConnectorMain     = "main"
	ConnectorExtra    = "extra"

	ConnectorActive      = "active"
	ConnectorUnavailable = "unavailable"
	ConnectorDisabled    = "disabled"
	ConnectorAmbiguous   = "ambiguous"

	TargetResolved    = "resolved"
	TargetAmbiguous   = "ambiguous"
	TargetUnresolved  = "unresolved"
	TargetUnavailable = "unavailable"

	ProjectionActive     = "active"
	ProjectionStale      = "stale"
	ProjectionDrift      = "drift"
	ProjectionDegraded   = "degraded"
	ProjectionUnresolved = "unresolved"
)

var (
	ErrInvalidDomain        = errors.New("domain: invalid domain value")
	ErrSourceInUse          = errors.New("domain: source_in_use")
	ErrTargetInUse          = errors.New("domain: target_in_use")
	ErrMigrationRequired    = errors.New("domain: migration_required")
	ErrTopologyInvalid      = errors.New("domain: invalid connector topology")
	ErrAmbiguousTarget      = errors.New("domain: ambiguous target")
	ErrDiscoveryUnavailable = errors.New("domain: discovery unavailable")
)

// Connector describes a publisher integration. CredentialID is only a
// reference into the Secret Store; it is never a plaintext credential.
type Connector struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Enabled      bool   `json:"enabled"`
	Status       string `json:"status"`
	Endpoint     string `json:"endpoint,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
	ConfigJSON   string `json:"config_json,omitempty"`
	MetadataJSON string `json:"metadata_json,omitempty"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
}

type SubscriptionProjection struct {
	ID                        string `json:"id"`
	SourceID                  string `json:"source_id"`
	TargetID                  string `json:"target_id"`
	LegacyKey                 string `json:"legacy_key"`
	Enabled                   bool   `json:"enabled"`
	LegacyOptionsSnapshotJSON string `json:"legacy_options_snapshot_json,omitempty"`
	ProjectionStatus          string `json:"projection_status"`
	ProjectedAt               int64  `json:"projected_at"`
}

// LegacySubscription is a lossless, serializable snapshot of a Legacy BuntDB
// subscription used solely to rebuild the SQLite projection. It is not a
// replacement for Legacy's source of truth.
type LegacySubscription struct {
	Platform          string `json:"platform"`
	ExternalID        string `json:"external_id"`
	DisplayName       string `json:"display_name,omitempty"`
	CanonicalURL      string `json:"canonical_url,omitempty"`
	SubscriptionType  string `json:"subscription_type"`
	TargetType        string `json:"target_type"`
	TargetExternalID  string `json:"target_external_id"`
	TargetDisplayName string `json:"target_display_name,omitempty"`
	Enabled           bool   `json:"enabled"`
	LegacyKey         string `json:"legacy_key"`
	OptionsJSON       string `json:"options_json,omitempty"`
}

func NormalizePlatform(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ValidateSource(source Source) error {
	if source.ID != "" && !IsUUIDv7(source.ID) {
		return fmt.Errorf("%w: source identity", ErrInvalidDomain)
	}
	if NormalizePlatform(string(source.Platform)) == "" || strings.TrimSpace(source.ExternalID) == "" {
		return fmt.Errorf("%w: source identity", ErrInvalidDomain)
	}
	if source.Status == "" {
		return nil
	}
	switch source.Status {
	case SourceActive, SourceUnresolved, SourceUnavailable:
	default:
		return fmt.Errorf("%w: source status", ErrInvalidDomain)
	}
	return nil
}

func ValidateTarget(target Target) error {
	if target.ID != "" && !IsUUIDv7(target.ID) {
		return fmt.Errorf("%w: target identity", ErrInvalidDomain)
	}
	if target.ConnectorID == "" || strings.TrimSpace(target.ExternalID) == "" {
		return fmt.Errorf("%w: target identity", ErrInvalidDomain)
	}
	if target.TargetType != TargetGroup && target.TargetType != TargetChannel {
		return fmt.Errorf("%w: target type", ErrInvalidDomain)
	}
	if target.Status == "" {
		return nil
	}
	switch target.Status {
	case TargetResolved, TargetAmbiguous, TargetUnresolved, TargetUnavailable:
	default:
		return fmt.Errorf("%w: target status", ErrInvalidDomain)
	}
	return nil
}

func ValidateConnector(connector Connector) error {
	if connector.ID != "" && !IsUUIDv7(connector.ID) {
		return fmt.Errorf("%w: connector identity", ErrInvalidDomain)
	}
	if strings.TrimSpace(connector.Name) == "" {
		return fmt.Errorf("%w: connector identity", ErrInvalidDomain)
	}
	switch connector.Kind {
	case ConnectorOneBot, ConnectorSatori, ConnectorTelegram:
	default:
		return fmt.Errorf("%w: connector kind", ErrInvalidDomain)
	}
	if connector.Role != ConnectorMain && connector.Role != ConnectorExtra {
		return fmt.Errorf("%w: connector role", ErrInvalidDomain)
	}
	return nil
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
