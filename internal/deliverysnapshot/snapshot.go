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
	LogicalTarget LogicalTarget   `json:"logical_target"`
	Message       MessageSnapshot `json:"message_snapshot"`
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
	if strings.TrimSpace(p.LogicalTarget.TargetID) == "" ||
		strings.TrimSpace(p.LogicalTarget.TargetType) == "" ||
		strings.TrimSpace(p.LogicalTarget.ExternalID) == "" {
		return ErrMissingTarget
	}
	if p.Message.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("deliverysnapshot: unsupported message schema version %d", p.Message.SchemaVersion)
	}
	if len(p.Message.Segments) == 0 && strings.TrimSpace(p.Message.TextFallback) == "" {
		return ErrMissingMessage
	}
	for _, segment := range p.Message.Segments {
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
	return p, nil
}
