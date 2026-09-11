// Package deliverysnapshot defines the process-independent payload used by
// Connector Migration. Values in this package are deliberately JSON-shaped:
// a restart must not need a Notify, Messenger, template object, goroutine, or
// other process-local value to release a held Delivery.
package deliverysnapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const CurrentSchemaVersion = 1

var (
	ErrMissingDelivery   = errors.New("deliverysnapshot: missing delivery_id")
	ErrMissingEvent      = errors.New("deliverysnapshot: missing event_id")
	ErrMissingMigration  = errors.New("deliverysnapshot: missing migration_id")
	ErrMissingRoute      = errors.New("deliverysnapshot: missing route decision")
	ErrMissingRouteSnap  = errors.New("deliverysnapshot: missing route snapshot")
	ErrMissingTarget     = errors.New("deliverysnapshot: missing logical target")
	ErrMissingMessage    = errors.New("deliverysnapshot: missing message snapshot")
	ErrMissingSegmentTyp = errors.New("deliverysnapshot: message segment has no type")
	ErrNonDurableMedia   = errors.New("deliverysnapshot: local media must be durable")
)

type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

type MediaReference struct {
	LocalPath string `json:"local_path,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
	Fallback  string `json:"fallback,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Durable   bool   `json:"durable,omitempty"`
}

type MessageSnapshot struct {
	SchemaVersion int              `json:"schema_version"`
	Segments      []Segment        `json:"segments"`
	Media         []MediaReference `json:"media,omitempty"`
	TextFallback  string           `json:"text_fallback,omitempty"`
	TemplateName  string           `json:"template_name,omitempty"`
	TemplateHash  string           `json:"template_hash,omitempty"`
}

type LogicalTarget struct {
	TargetID       string `json:"target_id"`
	TargetType     string `json:"target_type"`
	ExternalID     string `json:"external_id"`
	LegacyRouteKey string `json:"legacy_route_key,omitempty"`
}

type Payload struct {
	SchemaVersion int             `json:"schema_version"`
	DeliveryID    string          `json:"delivery_id"`
	MigrationID   string          `json:"migration_id"`
	EventID       string          `json:"event_id"`
	RouteID       string          `json:"route_decision_id"`
	RouteSnapshot json.RawMessage `json:"route_snapshot"`
	// LogicalTarget is retained for decoding the original v1 payload shape.
	// New snapshots also expose the explicit logical_target_snapshot field;
	// Marshal and Unmarshal keep both representations equivalent and never
	// retain a process-local target object.
	LogicalTarget         LogicalTarget   `json:"logical_target"`
	LogicalTargetSnapshot LogicalTarget   `json:"logical_target_snapshot,omitempty"`
	Message               MessageSnapshot `json:"message_snapshot"`
	// The following fields make the v1 contract explicit for consumers that
	// do not want to inspect MessageSnapshot. They are connector-neutral and
	// remain optional for backwards-compatible rows created by Phase 0.
	OrderedMessageSegments []Segment         `json:"ordered_message_segments,omitempty"`
	TemplateIdentity       string            `json:"template_identity,omitempty"`
	TemplateDigest         string            `json:"template_digest,omitempty"`
	CreatedAt              int64             `json:"created_at,omitempty"`
	HeldAt                 int64             `json:"held_at,omitempty"`
	Metadata               map[string]string `json:"metadata,omitempty"`
}

func (p Payload) Validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("deliverysnapshot: unsupported schema version %d", p.SchemaVersion)
	}
	if strings.TrimSpace(p.DeliveryID) == "" {
		return ErrMissingDelivery
	}
	if strings.TrimSpace(p.EventID) == "" {
		return ErrMissingEvent
	}
	if strings.TrimSpace(p.MigrationID) == "" {
		return ErrMissingMigration
	}
	if strings.TrimSpace(p.RouteID) == "" {
		return ErrMissingRoute
	}
	if len(p.RouteSnapshot) == 0 || !json.Valid(p.RouteSnapshot) {
		return ErrMissingRouteSnap
	}
	target := p.LogicalTarget
	if strings.TrimSpace(target.TargetID) == "" {
		target = p.LogicalTargetSnapshot
	}
	if strings.TrimSpace(target.TargetID) == "" ||
		strings.TrimSpace(target.TargetType) == "" ||
		strings.TrimSpace(target.ExternalID) == "" {
		return ErrMissingTarget
	}
	if p.Message.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("deliverysnapshot: unsupported message schema version %d", p.Message.SchemaVersion)
	}
	segments := p.Message.Segments
	if len(segments) == 0 {
		segments = p.OrderedMessageSegments
	}
	if len(segments) == 0 && strings.TrimSpace(p.Message.TextFallback) == "" {
		return ErrMissingMessage
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment.Type) == "" {
			return ErrMissingSegmentTyp
		}
	}
	for _, media := range p.Message.Media {
		if strings.TrimSpace(media.LocalPath) != "" && !media.Durable {
			return ErrNonDurableMedia
		}
	}
	return nil
}

func (p Payload) Marshal() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.LogicalTarget.TargetID) == "" {
		p.LogicalTarget = p.LogicalTargetSnapshot
	}
	if strings.TrimSpace(p.LogicalTargetSnapshot.TargetID) == "" {
		p.LogicalTargetSnapshot = p.LogicalTarget
	}
	if len(p.OrderedMessageSegments) == 0 {
		p.OrderedMessageSegments = append([]Segment(nil), p.Message.Segments...)
	}
	if p.TemplateIdentity == "" {
		p.TemplateIdentity = p.Message.TemplateName
	}
	if p.TemplateDigest == "" {
		p.TemplateDigest = p.Message.TemplateHash
	}
	return json.Marshal(p)
}

func Unmarshal(data []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return Payload{}, err
	}
	if err := p.Validate(); err != nil {
		return Payload{}, err
	}
	if strings.TrimSpace(p.LogicalTarget.TargetID) == "" {
		p.LogicalTarget = p.LogicalTargetSnapshot
	}
	if strings.TrimSpace(p.LogicalTargetSnapshot.TargetID) == "" {
		p.LogicalTargetSnapshot = p.LogicalTarget
	}
	if len(p.Message.Segments) == 0 && len(p.OrderedMessageSegments) > 0 {
		p.Message.Segments = append([]Segment(nil), p.OrderedMessageSegments...)
	}
	if p.Message.TemplateName == "" {
		p.Message.TemplateName = p.TemplateIdentity
	}
	if p.Message.TemplateHash == "" {
		p.Message.TemplateHash = p.TemplateDigest
	}
	return p, nil
}
