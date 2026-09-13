package platformdb

// This file provides the production idempotency backend.  It deliberately
// lives beside the platform Store so the Admin API and Replay service share
// the same single SQLite owner and cannot diverge after a process restart.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
)

var ErrIdempotencyUnavailable = errors.New("platformdb: idempotency unavailable")

// DurableIdempotencyStore persists the same contract as idempotency.MemoryStore
// in the platform database.  The enclosing Store is the sole SQLite owner;
// this type never opens a second connection or starts a worker.
type DurableIdempotencyStore struct {
	store     *Store
	retention time.Duration
}

func NewIdempotencyStore(store *Store, retention ...time.Duration) *DurableIdempotencyStore {
	value := idempotency.DefaultRetention
	if len(retention) > 0 && retention[0] > 0 {
		value = retention[0]
	}
	return &DurableIdempotencyStore{store: store, retention: value}
}

func (s *DurableIdempotencyStore) require() error {
	if s == nil || s.store == nil || s.store.db == nil {
		return ErrIdempotencyUnavailable
	}
	return nil
}

func idempotencyContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizeIdempotencyInputs(principal, key, command string) (string, string, string, error) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return "", "", "", idempotency.ErrInvalidPrincipal
	}
	key, err := idempotency.NormalizeKey(key)
	if err != nil {
		return "", "", "", err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", "", "", idempotency.ErrInvalidCommand
	}
	return principal, key, command, nil
}

func (s *DurableIdempotencyStore) Begin(principal, key string, fingerprint idempotency.Fingerprint, now time.Time) (idempotency.Record, idempotency.Outcome, error) {
	return s.BeginCommand(principal, key, idempotency.UnknownCommandType, fingerprint, now)
}

// BeginCommand atomically deletes expired rows, checks the full semantic
// fingerprint and command type, and claims a new key as in_progress.  A
// completed matching row is returned for exact replay; an in-progress row is
// never retried automatically after a restart.
func (s *DurableIdempotencyStore) BeginCommand(principal, key, command string, fingerprint idempotency.Fingerprint, now time.Time) (idempotency.Record, idempotency.Outcome, error) {
	if err := s.require(); err != nil {
		return idempotency.Record{}, idempotency.OutcomeReserved, err
	}
	principal, key, command, err := normalizeIdempotencyInputs(principal, key, command)
	if err != nil {
		return idempotency.Record{}, idempotency.OutcomeReserved, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	ctx := idempotencyContext(nil)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return idempotency.Record{}, idempotency.OutcomeReserved, fmt.Errorf("%w: begin claim: %v", ErrIdempotencyUnavailable, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DELETE FROM idempotency_records WHERE rowid IN (SELECT rowid FROM idempotency_records WHERE expires_at <= ? ORDER BY expires_at,principal,idempotency_key LIMIT 256)", now.Unix()); err != nil {
		return idempotency.Record{}, idempotency.OutcomeReserved, fmt.Errorf("%w: prune claim: %v", ErrIdempotencyUnavailable, err)
	}
	var record idempotency.Record
	var method, path, query, bodyHash, headersJSON, execution string
	var body []byte
	var created, expires int64
	var completed sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT principal,idempotency_key,method,normalized_path,canonical_query,body_sha256,command_type,execution_status,status_code,response_headers_json,response_body,created_at,completed_at,expires_at
FROM idempotency_records WHERE principal=? AND idempotency_key=?`, principal, key).Scan(&record.Principal, &record.Key, &method, &path, &query, &bodyHash, &record.CommandType, &execution, &record.StatusCode, &headersJSON, &body, &created, &completed, &expires)
	if err == nil {
		record.Fingerprint = idempotency.Fingerprint{Method: method, NormalizedPath: path, CanonicalQuery: query, BodySHA256: bodyHash}
		record.ExecutionStatus = idempotency.ExecutionStatus(execution)
		if record.ExecutionStatus == "" {
			if record.StatusCode == 0 {
				record.ExecutionStatus = idempotency.ExecutionInProgress
			} else {
				record.ExecutionStatus = idempotency.ExecutionCompleted
			}
		}
		record.Headers = decodeHeaders(headersJSON)
		record.Body = append([]byte(nil), body...)
		record.CreatedAt, record.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
		if completed.Valid {
			value := time.Unix(completed.Int64, 0).UTC()
			record.CompletedAt = &value
		}
		if !record.Fingerprint.Equal(fingerprint) || record.CommandType != command {
			return idempotency.Record{}, idempotency.OutcomeConflict, idempotency.ErrConflict
		}
		if record.ExecutionStatus != idempotency.ExecutionCompleted {
			return record, idempotency.OutcomeInProgress, idempotency.ErrInProgress
		}
		if err := tx.Commit(); err != nil {
			return idempotency.Record{}, idempotency.OutcomeReserved, fmt.Errorf("%w: replay commit: %v", ErrIdempotencyUnavailable, err)
		}
		committed = true
		return record, idempotency.OutcomeReplay, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return idempotency.Record{}, idempotency.OutcomeReserved, fmt.Errorf("%w: read claim: %v", ErrIdempotencyUnavailable, err)
	}
	expiresAt := now.Add(s.retention).Unix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records (principal,idempotency_key,method,normalized_path,canonical_query,body_sha256,command_type,execution_status,status_code,response_headers_json,response_body,created_at,completed_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, principal, key, fingerprint.Method, fingerprint.NormalizedPath, fingerprint.CanonicalQuery, fingerprint.BodySHA256, command, string(idempotency.ExecutionInProgress), 0, "{}", []byte{}, now.Unix(), nil, expiresAt); err != nil {
		return idempotency.Record{}, idempotency.OutcomeReserved, fmt.Errorf("%w: insert claim: %v", ErrIdempotencyUnavailable, err)
	}
	if err := tx.Commit(); err != nil {
		return idempotency.Record{}, idempotency.OutcomeReserved, fmt.Errorf("%w: claim commit: %v", ErrIdempotencyUnavailable, err)
	}
	committed = true
	return idempotency.Record{Principal: principal, Key: key, Fingerprint: fingerprint, CommandType: command, ExecutionStatus: idempotency.ExecutionInProgress, CreatedAt: now, ExpiresAt: time.Unix(expiresAt, 0).UTC()}, idempotency.OutcomeReserved, nil
}

func (s *DurableIdempotencyStore) Complete(principal, key string, fingerprint idempotency.Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (idempotency.Record, error) {
	return s.CompleteCommand(principal, key, "", fingerprint, statusCode, headers, body, now)
}

// CompleteCommand stores only the sanitized response and preserves the
// original expiry from the first claim. It is safe to call repeatedly after a
// successful completion and rejects a conflicting fingerprint/command.
func (s *DurableIdempotencyStore) CompleteCommand(principal, key, command string, fingerprint idempotency.Fingerprint, statusCode int, headers map[string]string, body []byte, now time.Time) (idempotency.Record, error) {
	if err := s.require(); err != nil {
		return idempotency.Record{}, err
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return idempotency.Record{}, idempotency.ErrInvalidPrincipal
	}
	key, err := idempotency.NormalizeKey(key)
	if err != nil {
		return idempotency.Record{}, err
	}
	if command != "" {
		command = strings.TrimSpace(command)
		if command == "" {
			return idempotency.Record{}, idempotency.ErrInvalidCommand
		}
	}
	if statusCode < 100 || statusCode > 599 {
		return idempotency.Record{}, errors.New("idempotency: invalid response status")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	ctx := idempotencyContext(nil)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return idempotency.Record{}, fmt.Errorf("%w: begin complete: %v", ErrIdempotencyUnavailable, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DELETE FROM idempotency_records WHERE rowid IN (SELECT rowid FROM idempotency_records WHERE expires_at <= ? ORDER BY expires_at,principal,idempotency_key LIMIT 256)", now.Unix()); err != nil {
		return idempotency.Record{}, fmt.Errorf("%w: prune complete: %v", ErrIdempotencyUnavailable, err)
	}
	var record idempotency.Record
	var method, path, query, bodyHash, execution, headersJSON string
	var storedBody []byte
	var created, expires int64
	var completed sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT principal,idempotency_key,method,normalized_path,canonical_query,body_sha256,command_type,execution_status,status_code,response_headers_json,response_body,created_at,completed_at,expires_at
FROM idempotency_records WHERE principal=? AND idempotency_key=?`, principal, key).Scan(&record.Principal, &record.Key, &method, &path, &query, &bodyHash, &record.CommandType, &execution, &record.StatusCode, &headersJSON, &storedBody, &created, &completed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return idempotency.Record{}, idempotency.ErrNotReserved
	}
	if err != nil {
		return idempotency.Record{}, fmt.Errorf("%w: read complete: %v", ErrIdempotencyUnavailable, err)
	}
	record.Fingerprint = idempotency.Fingerprint{Method: method, NormalizedPath: path, CanonicalQuery: query, BodySHA256: bodyHash}
	record.ExecutionStatus = idempotency.ExecutionStatus(execution)
	if record.ExecutionStatus == "" {
		if record.StatusCode == 0 {
			record.ExecutionStatus = idempotency.ExecutionInProgress
		} else {
			record.ExecutionStatus = idempotency.ExecutionCompleted
		}
	}
	record.Headers = decodeHeaders(headersJSON)
	record.Body = append([]byte(nil), storedBody...)
	record.CreatedAt, record.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	if completed.Valid {
		value := time.Unix(completed.Int64, 0).UTC()
		record.CompletedAt = &value
	}
	if !record.Fingerprint.Equal(fingerprint) {
		return idempotency.Record{}, idempotency.ErrConflict
	}
	if command != "" && record.CommandType != command {
		return idempotency.Record{}, idempotency.ErrConflict
	}
	if record.ExecutionStatus == idempotency.ExecutionCompleted {
		if err := tx.Commit(); err != nil {
			return idempotency.Record{}, fmt.Errorf("%w: completed replay commit: %v", ErrIdempotencyUnavailable, err)
		}
		committed = true
		return record, nil
	}
	safeHeaders := idempotency.SanitizeHeaders(headers)
	headerBytes, err := json.Marshal(safeHeaders)
	if err != nil {
		return idempotency.Record{}, fmt.Errorf("%w: encode response headers: %v", ErrIdempotencyUnavailable, err)
	}
	completedAt := now.Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET status_code=?,response_headers_json=?,response_body=?,execution_status=?,completed_at=? WHERE principal=? AND idempotency_key=?`, statusCode, string(headerBytes), append([]byte(nil), body...), string(idempotency.ExecutionCompleted), completedAt, principal, key); err != nil {
		return idempotency.Record{}, fmt.Errorf("%w: update complete: %v", ErrIdempotencyUnavailable, err)
	}
	if err := tx.Commit(); err != nil {
		return idempotency.Record{}, fmt.Errorf("%w: complete commit: %v", ErrIdempotencyUnavailable, err)
	}
	committed = true
	record.StatusCode, record.Headers, record.Body = statusCode, safeHeaders, append([]byte(nil), body...)
	record.ExecutionStatus = idempotency.ExecutionCompleted
	record.CompletedAt = func() *time.Time { value := now; return &value }()
	return record, nil
}

func (s *DurableIdempotencyStore) Lookup(principal, key string, now time.Time) (idempotency.Record, bool, error) {
	if err := s.require(); err != nil {
		return idempotency.Record{}, false, err
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return idempotency.Record{}, false, idempotency.ErrInvalidPrincipal
	}
	key, err := idempotency.NormalizeKey(key)
	if err != nil {
		return idempotency.Record{}, false, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	ctx := idempotencyContext(nil)
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM idempotency_records WHERE rowid IN (SELECT rowid FROM idempotency_records WHERE expires_at <= ? ORDER BY expires_at,principal,idempotency_key LIMIT 256)", now.UTC().Unix()); err != nil {
		return idempotency.Record{}, false, fmt.Errorf("%w: prune lookup: %v", ErrIdempotencyUnavailable, err)
	}
	return s.lookup(ctx, principal, key)
}

func (s *DurableIdempotencyStore) lookup(ctx context.Context, principal, key string) (idempotency.Record, bool, error) {
	var record idempotency.Record
	var method, path, query, bodyHash, execution, headersJSON string
	var body []byte
	var created, expires int64
	var completed sql.NullInt64
	err := s.store.db.QueryRowContext(ctx, `SELECT principal,idempotency_key,method,normalized_path,canonical_query,body_sha256,command_type,execution_status,status_code,response_headers_json,response_body,created_at,completed_at,expires_at
FROM idempotency_records WHERE principal=? AND idempotency_key=?`, principal, key).Scan(&record.Principal, &record.Key, &method, &path, &query, &bodyHash, &record.CommandType, &execution, &record.StatusCode, &headersJSON, &body, &created, &completed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return idempotency.Record{}, false, nil
	}
	if err != nil {
		return idempotency.Record{}, false, fmt.Errorf("%w: lookup: %v", ErrIdempotencyUnavailable, err)
	}
	record.Fingerprint = idempotency.Fingerprint{Method: method, NormalizedPath: path, CanonicalQuery: query, BodySHA256: bodyHash}
	record.ExecutionStatus = idempotency.ExecutionStatus(execution)
	if record.ExecutionStatus == "" {
		if record.StatusCode == 0 {
			record.ExecutionStatus = idempotency.ExecutionInProgress
		} else {
			record.ExecutionStatus = idempotency.ExecutionCompleted
		}
	}
	record.Headers = decodeHeaders(headersJSON)
	record.Body = append([]byte(nil), body...)
	record.CreatedAt, record.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	if completed.Valid {
		value := time.Unix(completed.Int64, 0).UTC()
		record.CompletedAt = &value
	}
	return record, true, nil
}

func (s *DurableIdempotencyStore) Prune(now time.Time) int {
	if s == nil || s.store == nil || s.store.db == nil {
		return 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	result, err := s.store.db.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE rowid IN (SELECT rowid FROM idempotency_records WHERE expires_at <= ? ORDER BY expires_at,principal,idempotency_key LIMIT 256)", now.UTC().Unix())
	if err != nil {
		return 0
	}
	n, _ := result.RowsAffected()
	return int(n)
}

func decodeHeaders(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil
	}
	return idempotency.SanitizeHeaders(headers)
}

var _ idempotency.Store = (*DurableIdempotencyStore)(nil)
