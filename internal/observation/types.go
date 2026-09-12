// Package observation contains the passive Phase 2A fact recorder. It is a
// fail-open auxiliary boundary: it never owns Legacy subscriptions, filters,
// rendering, delivery, or retry decisions.
package observation

import (
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

const (
	ObservationSchemaVersion = 1
	DefaultQueueSize         = 2048
	DefaultRetention         = 90 * 24 * time.Hour
)

// EventInput is an allowlisted projection of a stable Legacy event. Callers
// must populate it from public event fields; the recorder never marshals the
// source response or a concrete connector object.
type EventInput struct {
	Platform         string
	SourceKind       string
	SourceExternalID string
	UpstreamEventID  string
	EventType        string
	ObservedAt       time.Time
	SourceEventAt    time.Time
	PublicText       string
	PublicURL        string
	PublicMediaURLs  []string
	PublicAuthorID   string
	PublicAuthorName string
}

// PublicSnapshotProvider is an optional, source-owned allowlist seam. A
// Legacy source may implement these methods on its stable event type without
// exposing its raw API response to the observation package.
type PublicSnapshotProvider interface {
	ObservationPublicText() string
	ObservationPublicURL() string
	ObservationPublicMediaURLs() []string
	ObservationPublicAuthorID() string
	ObservationPublicAuthorName() string
	ObservationUpstreamEventID() string
	ObservationSourceEventAt() time.Time
}

type RouteInput struct {
	RouteOrdinal          int
	DestinationKind       string
	DestinationExternalID string
	Outcome               string
	ReasonCode            string
	ObservedAt            time.Time
}

type DeliveryInput struct {
	ConnectorKind         string
	DestinationExternalID string
	Status                string
	ResultCode            string
	ObservedAt            time.Time
}

// Trace is a short-lived in-memory correlation handle. Invalid traces are
// deliberately zero values and cause later hooks to become no-ops.
type Trace struct {
	eventObservationID string
	valid              bool
	// eventSnapshot is the same allowlisted public projection that is queued
	// for persistence. Keeping it on the correlation handle lets an
	// authoritative pre-send hook make a decision without racing the
	// recorder's asynchronous worker; it never contains a raw source payload.
	eventSnapshot platformdb.ObservedEventRecord
}

func (t Trace) Valid() bool { return t.valid && t.eventObservationID != "" }

func (t Trace) EventObservationID() string {
	if !t.Valid() {
		return ""
	}
	return t.eventObservationID
}

// EventSnapshot returns the allowlisted public event projection associated
// with this trace. The value is copied so callers cannot mutate recorder
// state. An invalid trace returns false.
func (t Trace) EventSnapshot() (platformdb.ObservedEventRecord, bool) {
	if !t.Valid() || t.eventSnapshot.ID == "" {
		return platformdb.ObservedEventRecord{}, false
	}
	return t.eventSnapshot, true
}

type RouteTrace struct {
	eventObservationID string
	routeObservationID string
	destinationID      string
	valid              bool
	eventSnapshot      platformdb.ObservedEventRecord
}

func (t RouteTrace) Valid() bool {
	return t.valid && t.eventObservationID != "" && t.routeObservationID != ""
}

func (t RouteTrace) EventObservationID() string {
	if !t.Valid() {
		return ""
	}
	return t.eventObservationID
}

func (t RouteTrace) RouteObservationID() string {
	if !t.Valid() {
		return ""
	}
	return t.routeObservationID
}

func (t RouteTrace) DestinationExternalID() string {
	if !t.Valid() {
		return ""
	}
	return t.destinationID
}

// EventSnapshot returns the public event projection carried through the
// route correlation handle. This is intentionally a value copy and contains
// no connector, credential, or process-owned objects.
func (t RouteTrace) EventSnapshot() (platformdb.ObservedEventRecord, bool) {
	if !t.Valid() || t.eventSnapshot.ID == "" {
		return platformdb.ObservedEventRecord{}, false
	}
	return t.eventSnapshot, true
}
