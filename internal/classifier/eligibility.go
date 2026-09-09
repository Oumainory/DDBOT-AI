package classifier

import (
	"errors"
	"strings"
)

var (
	ErrInvalidShadowSample  = errors.New("classifier: invalid shadow sample")
	ErrDuplicateShadowEvent = errors.New("classifier: duplicate event for classifier release")
	ErrReleaseMismatch      = errors.New("classifier: shadow sample belongs to another release")
)

type ShadowSample struct {
	ClassifierReleaseID string `json:"classifier_release_id"`
	EventID             string `json:"event_id"`
	SuggestedDrop       bool   `json:"suggested_drop"`
	Reviewed            bool   `json:"reviewed"`
}

type ShadowStats struct {
	ClassifierReleaseID   string `json:"classifier_release_id"`
	ShadowEvents          int    `json:"shadow_events"`
	ReviewedSuggestedDrop int    `json:"reviewed_suggested_drop"`
}

type EligibilityRequirement struct {
	MinShadowEvents          int
	MinReviewedSuggestedDrop int
}

func (r EligibilityRequirement) Eligible(stats ShadowStats) bool {
	return stats.ShadowEvents >= r.MinShadowEvents &&
		stats.ReviewedSuggestedDrop >= r.MinReviewedSuggestedDrop
}

// ShadowLedger deliberately keys samples by release and event. A prompt or
// normalizer change creates a new release and therefore starts a new real-data
// sample count; regression fixtures can be replayed separately.
type ShadowLedger struct {
	releaseID string
	samples   map[string]ShadowSample
}

func NewShadowLedger(releaseID string) (*ShadowLedger, error) {
	if strings.TrimSpace(releaseID) == "" {
		return nil, ErrInvalidShadowSample
	}
	return &ShadowLedger{releaseID: releaseID, samples: make(map[string]ShadowSample)}, nil
}

func (l *ShadowLedger) Add(sample ShadowSample) error {
	if l == nil || strings.TrimSpace(l.releaseID) == "" ||
		strings.TrimSpace(sample.ClassifierReleaseID) == "" || strings.TrimSpace(sample.EventID) == "" {
		return ErrInvalidShadowSample
	}
	if sample.ClassifierReleaseID != l.releaseID {
		return ErrReleaseMismatch
	}
	if _, exists := l.samples[sample.EventID]; exists {
		return ErrDuplicateShadowEvent
	}
	l.samples[sample.EventID] = sample
	return nil
}

func (l *ShadowLedger) Stats() ShadowStats {
	stats := ShadowStats{}
	if l == nil {
		return stats
	}
	stats.ClassifierReleaseID = l.releaseID
	stats.ShadowEvents = len(l.samples)
	for _, sample := range l.samples {
		if sample.SuggestedDrop && sample.Reviewed {
			stats.ReviewedSuggestedDrop++
		}
	}
	return stats
}
