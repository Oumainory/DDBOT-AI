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
	ErrNotReserved      = errors.New("idempotency: request is not reserved")
)

type Record struct {
	Principal   string            `json:"principal"`
	Key         string            `json:"key"`
	Fingerprint Fingerprint       `json:"fingerprint"`
	StatusCode  int               `json:"status_code"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        []byte            `json:"body,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

func (r Record) Completed() bool { return r.StatusCode != 0 }

type Outcome uint8

const (
	OutcomeReserved Outcome = iota
	OutcomeReplay
	OutcomeConflict
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

// Begin atomically reserves a key. The same key and request fingerprint
// returns the original record for replay. A different method/path/body hash
// never reuses the original result and returns ErrConflict (HTTP 409).
func (s *MemoryStore) Begin(principal, key string, fingerprint Fingerprint, now time.Time) (Record, Outcome, error) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return Record{}, OutcomeReserved, ErrInvalidPrincipal
	}
	key, err := NormalizeKey(key)
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
		if !existing.Fingerprint.Equal(fingerprint) {
			return Record{}, OutcomeConflict, ErrConflict
		}
		return cloneRecord(existing), OutcomeReplay, nil
	}

	record := Record{
		Principal:   principal,
		Key:         key,
		Fingerprint: fingerprint,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.retention),
	}
	s.records[lookupKey] = record
	return cloneRecord(record), OutcomeReserved, nil
}

// Complete stores the exact response that a replay must receive. It repeats
// the fingerprint check so a caller cannot accidentally complete a conflicting
// request under an existing key.
func (s *MemoryStore) Complete(principal, key string, fingerprint Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (Record, error) {
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
	if record.Completed() {
		return cloneRecord(record), nil
	}
	if statusCode < 100 || statusCode > 599 {
		return Record{}, errors.New("idempotency: invalid response status")
	}
	record.StatusCode = statusCode
	record.Headers = cloneHeaders(headers)
	record.Body = append([]byte(nil), body...)
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
	return record
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
