package platformdb

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrAuthInvariant      = errors.New("platformdb: authentication state invariant failed")
	ErrSetupComplete      = errors.New("platformdb: setup is already complete")
	ErrSetupTokenMissing  = errors.New("platformdb: setup token is not available")
	ErrSetupTokenInvalid  = errors.New("platformdb: invalid setup token")
	ErrSetupTokenExpired  = errors.New("platformdb: setup token expired")
	ErrSetupTokenConsumed = errors.New("platformdb: setup token already consumed")
	ErrAdminExists        = errors.New("platformdb: administrator already exists")
	ErrAdminNotFound      = errors.New("platformdb: administrator not found")
	ErrSessionNotFound    = errors.New("platformdb: session not found")
	ErrSessionExpired     = errors.New("platformdb: session expired")
	ErrSessionRevoked     = errors.New("platformdb: session revoked")
	ErrAdminDisabled      = errors.New("platformdb: administrator disabled")
)

// AuthRepository is the only persistence boundary used by the P1B auth
// service. It deliberately exposes no *sql.DB so authentication cannot create
// a second SQLite owner or execute unreviewed SQL from an HTTP handler.
type AuthRepository struct {
	store *Store
}

func NewAuthRepository(store *Store) *AuthRepository {
	if store == nil {
		return nil
	}
	return &AuthRepository{store: store}
}

type AuthState string

const (
	AuthSetupRequired AuthState = "setup_required"
	AuthReady         AuthState = "ready"
)

type AdministratorRecord struct {
	ID                string
	Username          string
	PasswordHash      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	PasswordChangedAt time.Time
	Disabled          bool
}

type SessionRecord struct {
	SessionIDHash string
	AdminID       string
	Username      string
	CreatedAt     time.Time
	LastSeenAt    time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	CSRFSecret    string
	UserAgentHash string
}

func (r *AuthRepository) requireStore() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrDatabaseClosed
	}
	return nil
}

func (r *AuthRepository) State(ctx context.Context) (AuthState, error) {
	if err := r.requireStore(); err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var completed int
	if err := r.store.db.QueryRowContext(ctx, "SELECT completed FROM setup_state WHERE singleton = 1").Scan(&completed); err != nil {
		return "", fmt.Errorf("platformdb: read setup state: %w", err)
	}
	var adminCount int
	if err := r.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM administrators").Scan(&adminCount); err != nil {
		return "", fmt.Errorf("platformdb: count administrators: %w", err)
	}
	switch {
	case completed == 0 && adminCount == 0:
		return AuthSetupRequired, nil
	case completed == 1 && adminCount == 1:
		return AuthReady, nil
	default:
		return "", fmt.Errorf("%w: completed=%d administrator_count=%d", ErrAuthInvariant, completed, adminCount)
	}
}

// EnsureSetupToken creates or replaces the single bootstrap token row only
// while setup is incomplete. It returns true when the supplied token was
// persisted and therefore may be emitted through the dedicated bootstrap
// output boundary. An active token is never silently rotated on restart.
func (r *AuthRepository) EnsureSetupToken(ctx context.Context, tokenHash string, createdAt, expiresAt time.Time) (bool, error) {
	if err := r.requireStore(); err != nil {
		return false, err
	}
	if strings.TrimSpace(tokenHash) == "" || expiresAt.IsZero() || createdAt.IsZero() {
		return false, ErrSetupTokenMissing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("platformdb: begin setup token: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var completed int
	if err := tx.QueryRowContext(ctx, "SELECT completed FROM setup_state WHERE singleton = 1").Scan(&completed); err != nil {
		return false, fmt.Errorf("platformdb: read setup state: %w", err)
	}
	var adminCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM administrators").Scan(&adminCount); err != nil {
		return false, fmt.Errorf("platformdb: count administrators: %w", err)
	}
	if completed == 1 || adminCount > 0 {
		if completed == 0 && adminCount > 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE setup_state SET completed = 1, completed_at = COALESCE(completed_at, ?) WHERE singleton = 1", createdAt.UTC().Unix()); err != nil {
				return false, fmt.Errorf("platformdb: repair setup state: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("platformdb: commit setup state: %w", err)
		}
		committed = true
		return false, nil
	}

	var existingHash string
	var existingExpires int64
	var consumedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `
SELECT token_hash, expires_at, consumed_at
FROM setup_tokens
WHERE singleton = 1`,
	).Scan(&existingHash, &existingExpires, &consumedAt)
	switch {
	case err == nil && !consumedAt.Valid && existingExpires > createdAt.UTC().Unix():
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("platformdb: commit existing setup token: %w", err)
		}
		committed = true
		return false, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("platformdb: read setup token: %w", err)
	}

	if err == nil {
		if _, err := tx.ExecContext(ctx, `
UPDATE setup_tokens
SET token_hash = ?, created_at = ?, expires_at = ?, consumed_at = NULL
WHERE singleton = 1`, tokenHash, createdAt.UTC().Unix(), expiresAt.UTC().Unix()); err != nil {
			return false, fmt.Errorf("platformdb: rotate setup token: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO setup_tokens (singleton, token_hash, created_at, expires_at)
VALUES (1, ?, ?, ?)`, tokenHash, createdAt.UTC().Unix(), expiresAt.UTC().Unix()); err != nil {
			return false, fmt.Errorf("platformdb: create setup token: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("platformdb: commit setup token: %w", err)
	}
	committed = true
	return true, nil
}

// CreateAdministrator consumes the setup token and creates the only admin in
// one transaction. The hash is compared in constant time after reading the
// single token row; no plaintext setup token is persisted.
func (r *AuthRepository) CreateAdministrator(ctx context.Context, tokenHash, adminID, username, passwordHash string, now time.Time) (AdministratorRecord, error) {
	if err := r.requireStore(); err != nil {
		return AdministratorRecord{}, err
	}
	if strings.TrimSpace(tokenHash) == "" || strings.TrimSpace(adminID) == "" || strings.TrimSpace(username) == "" || strings.TrimSpace(passwordHash) == "" {
		return AdministratorRecord{}, ErrSetupTokenInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: begin administrator setup: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var completed int
	if err := tx.QueryRowContext(ctx, "SELECT completed FROM setup_state WHERE singleton = 1").Scan(&completed); err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: read setup state: %w", err)
	}
	if completed == 1 {
		return AdministratorRecord{}, ErrSetupComplete
	}
	var adminCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM administrators").Scan(&adminCount); err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: count administrators: %w", err)
	}
	if adminCount != 0 {
		return AdministratorRecord{}, ErrAdminExists
	}

	var storedHash string
	var expiresAt int64
	var consumedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
SELECT token_hash, expires_at, consumed_at
FROM setup_tokens
WHERE singleton = 1`).Scan(&storedHash, &expiresAt, &consumedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdministratorRecord{}, ErrSetupTokenMissing
		}
		return AdministratorRecord{}, fmt.Errorf("platformdb: read setup token: %w", err)
	}
	if consumedAt.Valid {
		return AdministratorRecord{}, ErrSetupTokenConsumed
	}
	if expiresAt <= now.UTC().Unix() {
		return AdministratorRecord{}, ErrSetupTokenExpired
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(tokenHash)) != 1 {
		return AdministratorRecord{}, ErrSetupTokenInvalid
	}

	stamp := now.UTC().Unix()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO administrators
(id, singleton, username, password_hash, created_at, updated_at, password_changed_at, disabled)
VALUES (?, 1, ?, ?, ?, ?, ?, 0)`, adminID, username, passwordHash, stamp, stamp, stamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return AdministratorRecord{}, ErrAdminExists
		}
		return AdministratorRecord{}, fmt.Errorf("platformdb: create administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE setup_tokens SET consumed_at = ? WHERE singleton = 1 AND consumed_at IS NULL", stamp); err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: consume setup token: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE setup_state SET completed = 1, completed_at = ? WHERE singleton = 1 AND completed = 0", stamp); err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: complete setup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: commit administrator setup: %w", err)
	}
	committed = true
	return AdministratorRecord{
		ID:                adminID,
		Username:          username,
		PasswordHash:      passwordHash,
		CreatedAt:         now.UTC(),
		UpdatedAt:         now.UTC(),
		PasswordChangedAt: now.UTC(),
	}, nil
}

func (r *AuthRepository) AdministratorByUsername(ctx context.Context, username string) (AdministratorRecord, error) {
	if err := r.requireStore(); err != nil {
		return AdministratorRecord{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return r.administrator(ctx, "WHERE username = ?", username)
}

func (r *AuthRepository) AdministratorByID(ctx context.Context, id string) (AdministratorRecord, error) {
	if err := r.requireStore(); err != nil {
		return AdministratorRecord{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return r.administrator(ctx, "WHERE id = ?", id)
}

func (r *AuthRepository) administrator(ctx context.Context, predicate, value string) (AdministratorRecord, error) {
	var record AdministratorRecord
	var createdAt, updatedAt, passwordChangedAt int64
	var disabled int
	err := r.store.db.QueryRowContext(ctx, `
SELECT id, username, password_hash, created_at, updated_at, password_changed_at, disabled
FROM administrators `+predicate, value).Scan(
		&record.ID, &record.Username, &record.PasswordHash,
		&createdAt, &updatedAt, &passwordChangedAt, &disabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AdministratorRecord{}, ErrAdminNotFound
	}
	if err != nil {
		return AdministratorRecord{}, fmt.Errorf("platformdb: read administrator: %w", err)
	}
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	record.PasswordChangedAt = time.Unix(passwordChangedAt, 0).UTC()
	record.Disabled = disabled != 0
	return record, nil
}

func (r *AuthRepository) CreateSession(ctx context.Context, record SessionRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(record.SessionIDHash) == "" || strings.TrimSpace(record.AdminID) == "" || strings.TrimSpace(record.CSRFSecret) == "" || record.ExpiresAt.IsZero() {
		return ErrSessionNotFound
	}
	_, err := r.store.db.ExecContext(ctx, `
INSERT INTO sessions
(session_id_hash, admin_id, created_at, last_seen_at, expires_at, csrf_secret, user_agent_hash)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.SessionIDHash, record.AdminID, record.CreatedAt.UTC().Unix(), record.LastSeenAt.UTC().Unix(), record.ExpiresAt.UTC().Unix(), record.CSRFSecret, nullableString(record.UserAgentHash))
	if err != nil {
		return fmt.Errorf("platformdb: create session: %w", err)
	}
	return nil
}

func (r *AuthRepository) LookupSession(ctx context.Context, sessionIDHash string, now time.Time) (SessionRecord, error) {
	if err := r.requireStore(); err != nil {
		return SessionRecord{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	var record SessionRecord
	var createdAt, lastSeenAt, expiresAt int64
	var revokedAt sql.NullInt64
	var userAgentHash sql.NullString
	var disabled int
	err := r.store.db.QueryRowContext(ctx, `
SELECT s.session_id_hash, s.admin_id, a.username, s.created_at, s.last_seen_at,
       s.expires_at, s.revoked_at, s.csrf_secret, s.user_agent_hash, a.disabled
FROM sessions s
JOIN administrators a ON a.id = s.admin_id
WHERE s.session_id_hash = ?`, sessionIDHash).Scan(
		&record.SessionIDHash, &record.AdminID, &record.Username,
		&createdAt, &lastSeenAt, &expiresAt, &revokedAt, &record.CSRFSecret,
		&userAgentHash, &disabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRecord{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionRecord{}, fmt.Errorf("platformdb: read session: %w", err)
	}
	if revokedAt.Valid {
		return SessionRecord{}, ErrSessionRevoked
	}
	if expiresAt <= now.UTC().Unix() {
		return SessionRecord{}, ErrSessionExpired
	}
	if disabled != 0 {
		return SessionRecord{}, ErrAdminDisabled
	}
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.LastSeenAt = time.Unix(lastSeenAt, 0).UTC()
	record.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	if userAgentHash.Valid {
		record.UserAgentHash = userAgentHash.String
	}
	if _, err := r.store.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", now.UTC().Unix(), sessionIDHash, now.UTC().Unix()); err != nil {
		return SessionRecord{}, fmt.Errorf("platformdb: update session activity: %w", err)
	}
	record.LastSeenAt = now.UTC()
	return record, nil
}

func (r *AuthRepository) RevokeSession(ctx context.Context, sessionIDHash string, now time.Time) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	_, err := r.store.db.ExecContext(ctx, "UPDATE sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE session_id_hash = ?", now.UTC().Unix(), sessionIDHash)
	if err != nil {
		return fmt.Errorf("platformdb: revoke session: %w", err)
	}
	return nil
}

func (r *AuthRepository) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	if err := r.requireStore(); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	result, err := r.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ? OR revoked_at IS NOT NULL", now.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("platformdb: prune sessions: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("platformdb: count pruned sessions: %w", err)
	}
	return count, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
