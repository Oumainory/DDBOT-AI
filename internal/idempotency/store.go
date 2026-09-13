package idempotency

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"time"
)

const DefaultRetention = 7 * 24 * time.Hour

var (
	ErrInvalidPrincipal = errors.New("idempotency: invalid principal")
	ErrConflict         = errors.New("idempotency_conflict")
	ErrInProgress       = errors.New("idempotency_in_progress")
	ErrNotReserved      = errors.New("idempotency: request is not reserved")
	ErrInvalidCommand   = errors.New("idempotency: invalid command type")
)

type ExecutionStatus string

const (
	ExecutionInProgress ExecutionStatus = "in_progress"
	ExecutionCompleted  ExecutionStatus = "completed"
	UnknownCommandType                  = "unknown"
)

type Record struct {
	Principal       string            `json:"principal"`
	Key             string            `json:"key"`
	Fingerprint     Fingerprint       `json:"fingerprint"`
	CommandType     string            `json:"command_type"`
	ExecutionStatus ExecutionStatus   `json:"execution_status"`
	StatusCode      int               `json:"status_code"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            []byte            `json:"body,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
	ExpiresAt       time.Time         `json:"expires_at"`
}

// Store is the small persistence boundary shared by the Admin API and Replay
// service. MemoryStore remains useful for isolated tests; production wires a
// SQLite implementation that satisfies the same atomic claim/complete
// contract.
type Store interface {
	Begin(principal, key string, fingerprint Fingerprint, now time.Time) (Record, Outcome, error)
	BeginCommand(principal, key, commandType string, fingerprint Fingerprint, now time.Time) (Record, Outcome, error)
	Complete(principal, key string, fingerprint Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (Record, error)
	CompleteCommand(principal, key, commandType string, fingerprint Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (Record, error)
	Lookup(principal, key string, now time.Time) (Record, bool, error)
	Prune(now time.Time) int
}

func (r Record) Completed() bool {
	if r.ExecutionStatus != "" {
		return r.ExecutionStatus == ExecutionCompleted
	}
	return r.StatusCode != 0
}

type Outcome uint8

const (
	OutcomeReserved Outcome = iota
	OutcomeReplay
	OutcomeConflict
	OutcomeInProgress
)

// MemoryStore is a reference implementation of the idempotency contract.
// Production persistence will use the same fields in SQLite; keeping this
// implementation small makes the conflict and retention rules testable before
// the API/database phase lands.
type MemoryStore struct {
	mu        sync.Mutex
	retention time.Duration
	records   map[string]Record
}

func NewMemoryStore(retention time.Duration) *MemoryStore {
	if retention <= 0 {
		retention = DefaultRetention
	}
	return &MemoryStore{retention: retention, records: make(map[string]Record)}
}

func recordKey(principal, key string) string { return principal + "\x00" + key }

// Begin atomically reserves a key. A matching completed request returns the
// original record for replay; a matching in-progress request is explicit and
// returns ErrInProgress. A different method/path/query/body hash never reuses
// the original result and returns ErrConflict (HTTP 409).
func (s *MemoryStore) Begin(principal, key string, fingerprint Fingerprint, now time.Time) (Record, Outcome, error) {
	return s.BeginCommand(principal, key, UnknownCommandType, fingerprint, now)
}

// BeginCommand atomically claims a command key. A matching completed record
// is replayable; a matching in-progress record is explicit and must not be
// executed twice. Command type is part of the semantic identity alongside the
// request fingerprint.
func (s *MemoryStore) BeginCommand(principal, key, commandType string, fingerprint Fingerprint, now time.Time) (Record, Outcome, error) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return Record{}, OutcomeReserved, ErrInvalidPrincipal
	}
	commandType, err := normalizeCommandType(commandType)
	if err != nil {
		return Record{}, OutcomeReserved, err
	}
	key, err = NormalizeKey(key)
	if err != nil {
		return Record{}, OutcomeReserved, err
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked(now)

	lookupKey := recordKey(principal, key)
	if existing, ok := s.records[lookupKey]; ok {
		if !existing.Fingerprint.Equal(fingerprint) || existing.CommandType != commandType {
			return Record{}, OutcomeConflict, ErrConflict
		}
		if !existing.Completed() {
			return cloneRecord(existing), OutcomeInProgress, ErrInProgress
		}
		return cloneRecord(existing), OutcomeReplay, nil
	}

	record := Record{
		Principal:       principal,
		Key:             key,
		Fingerprint:     fingerprint,
		CommandType:     commandType,
		ExecutionStatus: ExecutionInProgress,
		CreatedAt:       now,
		ExpiresAt:       now.Add(s.retention),
	}
	s.records[lookupKey] = record
	return cloneRecord(record), OutcomeReserved, nil
}

// Complete stores the exact response that a replay must receive. It repeats
// the fingerprint check so a caller cannot accidentally complete a conflicting
// request under an existing key.
func (s *MemoryStore) Complete(principal, key string, fingerprint Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (Record, error) {
	return s.complete(principal, key, "", fingerprint, statusCode, headers, body, now)
}

// CompleteCommand finalizes a claimed command and stores the exact sanitized
// response that a replay must receive. ExpiresAt remains tied to the first
// accepted claim and is never extended by completion or replay.
func (s *MemoryStore) CompleteCommand(principal, key, commandType string, fingerprint Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (Record, error) {
	return s.complete(principal, key, commandType, fingerprint, statusCode, headers, body, now)
}

func (s *MemoryStore) complete(principal, key, commandType string, fingerprint Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (Record, error) {
	principal = strings.TrimSpace(principal)
	key, err := NormalizeKey(key)
	if principal == "" {
		return Record{}, ErrInvalidPrincipal
	}
	if err != nil {
		return Record{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked(now)

	lookupKey := recordKey(principal, key)
	record, ok := s.records[lookupKey]
	if !ok {
		return Record{}, ErrNotReserved
	}
	if !record.Fingerprint.Equal(fingerprint) {
		return Record{}, ErrConflict
	}
	if commandType != "" {
		commandType, err = normalizeCommandType(commandType)
		if err != nil {
			return Record{}, err
		}
		if record.CommandType != commandType {
			return Record{}, ErrConflict
		}
	}
	if record.Completed() {
		return cloneRecord(record), nil
	}
	if statusCode < 100 || statusCode > 599 {
		return Record{}, errors.New("idempotency: invalid response status")
	}
	record.StatusCode = statusCode
	record.Headers = SanitizeHeaders(headers)
	record.Body = append([]byte(nil), body...)
	record.ExecutionStatus = ExecutionCompleted
	completedAt := now
	record.CompletedAt = &completedAt
	s.records[lookupKey] = record
	return cloneRecord(record), nil
}

func (s *MemoryStore) Lookup(principal, key string, now time.Time) (Record, bool, error) {
	principal = strings.TrimSpace(principal)
	key, err := NormalizeKey(key)
	if principal == "" {
		return Record{}, false, ErrInvalidPrincipal
	}
	if err != nil {
		return Record{}, false, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked(now)
	record, ok := s.records[recordKey(principal, key)]
	return cloneRecord(record), ok, nil
}

func (s *MemoryStore) Prune(now time.Time) int {
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeExpiredLocked(now)
}

func (s *MemoryStore) removeExpiredLocked(now time.Time) int {
	removed := 0
	for key, record := range s.records {
		if !now.Before(record.ExpiresAt) {
			delete(s.records, key)
			removed++
		}
	}
	return removed
}

func cloneRecord(record Record) Record {
	record.Headers = cloneHeaders(record.Headers)
	record.Body = bytes.Clone(record.Body)
	if record.CompletedAt != nil {
		completedAt := *record.CompletedAt
		record.CompletedAt = &completedAt
	}
	return record
}

func normalizeCommandType(commandType string) (string, error) {
	commandType = strings.TrimSpace(commandType)
	if commandType == "" {
		return "", ErrInvalidCommand
	}
	return commandType, nil
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}

// SanitizeHeaders returns the deliberately small response-header allowlist
// that may be replayed from durable idempotency records. Authentication and
// session material (for example Set-Cookie and Authorization) is never
// persisted or replayed.
func SanitizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	allowed := map[string]string{
		"content-type":           "Content-Type",
		"location":               "Location",
		"cache-control":          "Cache-Control",
		"x-content-type-options": "X-Content-Type-Options",
		"referrer-policy":        "Referrer-Policy",
	}
	result := make(map[string]string)
	for key, value := range headers {
		if canonical, ok := allowed[strings.ToLower(strings.TrimSpace(key))]; ok {
			result[canonical] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
