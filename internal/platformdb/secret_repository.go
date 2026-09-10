package platformdb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrSecretStoreStateMissing    = errors.New("platformdb: secret store state is missing")
	ErrSecretStoreStateExists     = errors.New("platformdb: secret store state already exists")
	ErrCredentialExists           = errors.New("platformdb: credential already exists")
	ErrCredentialNotFound         = errors.New("platformdb: credential not found")
	ErrCredentialRevisionConflict = errors.New("platformdb: credential secret revision conflict")
	ErrSecretNotConfigured        = errors.New("platformdb: credential secret is not configured")
)

// SecretStoreStateRecord contains only the encrypted sentinel and its
// non-sensitive timestamps. It never contains a Master Key or plaintext.
type SecretStoreStateRecord struct {
	EnvelopeVersion int
	SentinelNonce   []byte
	SentinelCipher  []byte
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CredentialMetadataRecord struct {
	ID         string
	Type       string
	Label      string
	Source     string
	Configured bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CredentialSecretRecord struct {
	EnvelopeVersion int
	SecretRevision  int64
	Nonce           []byte
	Ciphertext      []byte
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SecretRepository is the platform database boundary for P1C. Secret Store
// code receives records and encrypted envelopes through this type; it never
// receives the underlying *sql.DB and cannot bypass the single SQLite owner.
type SecretRepository struct {
	store *Store
}

func NewSecretRepository(store *Store) *SecretRepository {
	if store == nil {
		return nil
	}
	return &SecretRepository{store: store}
}

func (r *SecretRepository) requireStore() error {
	if r == nil || r.store == nil || r.store.db == nil {
		return ErrDatabaseClosed
	}
	return nil
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizedTimestamp(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

// HasStoredSecretData distinguishes a fresh Secret Store from one that has
// durable encrypted state. Metadata-only credentials do not count as stored
// secret data; an encrypted sentinel, credential secret, or configured
// credential marker does.
func (r *SecretRepository) HasStoredSecretData(ctx context.Context) (bool, error) {
	if err := r.requireStore(); err != nil {
		return false, err
	}
	ctx = normalizeContext(ctx)
	var exists int
	if err := r.store.db.QueryRowContext(ctx, `
SELECT CASE WHEN EXISTS (SELECT 1 FROM secret_store_state)
              OR EXISTS (SELECT 1 FROM credential_secrets)
              OR EXISTS (SELECT 1 FROM credentials WHERE configured = 1)
            THEN 1 ELSE 0 END`).Scan(&exists); err != nil {
		return false, fmt.Errorf("platformdb: inspect secret store data: %w", err)
	}
	return exists == 1, nil
}

func (r *SecretRepository) SecretStoreState(ctx context.Context) (SecretStoreStateRecord, error) {
	if err := r.requireStore(); err != nil {
		return SecretStoreStateRecord{}, err
	}
	ctx = normalizeContext(ctx)
	var record SecretStoreStateRecord
	var createdAt, updatedAt int64
	if err := r.store.db.QueryRowContext(ctx, `
SELECT envelope_version, sentinel_nonce, sentinel_ciphertext, created_at, updated_at
FROM secret_store_state
WHERE singleton = 1`).Scan(
		&record.EnvelopeVersion, &record.SentinelNonce, &record.SentinelCipher,
		&createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SecretStoreStateRecord{}, ErrSecretStoreStateMissing
		}
		return SecretStoreStateRecord{}, fmt.Errorf("platformdb: read secret store state: %w", err)
	}
	record.SentinelNonce = bytes.Clone(record.SentinelNonce)
	record.SentinelCipher = bytes.Clone(record.SentinelCipher)
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return record, nil
}

// CreateSecretStoreState persists the encrypted sentinel only after the
// caller has durably created or loaded the Master Key. It is insert-only.
func (r *SecretRepository) CreateSecretStoreState(ctx context.Context, record SecretStoreStateRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if record.EnvelopeVersion <= 0 || len(record.SentinelNonce) == 0 || len(record.SentinelCipher) == 0 {
		return ErrSecretStoreStateMissing
	}
	stamp := normalizedTimestamp(record.CreatedAt)
	updated := normalizedTimestamp(record.UpdatedAt)
	ctx = normalizeContext(ctx)
	_, err := r.store.db.ExecContext(ctx, `
INSERT INTO secret_store_state
(singleton, envelope_version, sentinel_nonce, sentinel_ciphertext, created_at, updated_at)
VALUES (1, ?, ?, ?, ?, ?)`,
		record.EnvelopeVersion, append([]byte(nil), record.SentinelNonce...), append([]byte(nil), record.SentinelCipher...), stamp.Unix(), updated.Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrSecretStoreStateExists
		}
		return fmt.Errorf("platformdb: create secret store state: %w", err)
	}
	return nil
}

func (r *SecretRepository) CreateCredential(ctx context.Context, record CredentialMetadataRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Type) == "" || strings.TrimSpace(record.Source) == "" {
		return ErrCredentialNotFound
	}
	created := normalizedTimestamp(record.CreatedAt)
	updated := normalizedTimestamp(record.UpdatedAt)
	ctx = normalizeContext(ctx)
	_, err := r.store.db.ExecContext(ctx, `
INSERT INTO credentials (id, type, label, source, configured, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?)`,
		record.ID, record.Type, record.Label, record.Source, created.Unix(), updated.Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrCredentialExists
		}
		return fmt.Errorf("platformdb: create credential: %w", err)
	}
	return nil
}

func (r *SecretRepository) Credential(ctx context.Context, id string) (CredentialMetadataRecord, error) {
	if err := r.requireStore(); err != nil {
		return CredentialMetadataRecord{}, err
	}
	if strings.TrimSpace(id) == "" {
		return CredentialMetadataRecord{}, ErrCredentialNotFound
	}
	ctx = normalizeContext(ctx)
	var record CredentialMetadataRecord
	var configured, createdAt, updatedAt int64
	err := r.store.db.QueryRowContext(ctx, `
SELECT id, type, label, source, configured, created_at, updated_at
FROM credentials WHERE id = ?`, id).Scan(
		&record.ID, &record.Type, &record.Label, &record.Source,
		&configured, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialMetadataRecord{}, ErrCredentialNotFound
	}
	if err != nil {
		return CredentialMetadataRecord{}, fmt.Errorf("platformdb: read credential: %w", err)
	}
	record.Configured = configured != 0
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return record, nil
}

func (r *SecretRepository) Credentials(ctx context.Context) ([]CredentialMetadataRecord, error) {
	if err := r.requireStore(); err != nil {
		return nil, err
	}
	ctx = normalizeContext(ctx)
	rows, err := r.store.db.QueryContext(ctx, `
SELECT id, type, label, source, configured, created_at, updated_at
FROM credentials ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("platformdb: list credentials: %w", err)
	}
	defer rows.Close()
	var records []CredentialMetadataRecord
	for rows.Next() {
		var record CredentialMetadataRecord
		var configured, createdAt, updatedAt int64
		if err := rows.Scan(&record.ID, &record.Type, &record.Label, &record.Source, &configured, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("platformdb: scan credential: %w", err)
		}
		record.Configured = configured != 0
		record.CreatedAt = time.Unix(createdAt, 0).UTC()
		record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformdb: list credentials: %w", err)
	}
	return records, nil
}

func (r *SecretRepository) CredentialSecret(ctx context.Context, id string) (CredentialSecretRecord, error) {
	if err := r.requireStore(); err != nil {
		return CredentialSecretRecord{}, err
	}
	if strings.TrimSpace(id) == "" {
		return CredentialSecretRecord{}, ErrCredentialNotFound
	}
	ctx = normalizeContext(ctx)
	var record CredentialSecretRecord
	var createdAt, updatedAt int64
	err := r.store.db.QueryRowContext(ctx, `
SELECT envelope_version, secret_revision, nonce, ciphertext, created_at, updated_at
FROM credential_secrets WHERE credential_id = ?`, id).Scan(
		&record.EnvelopeVersion, &record.SecretRevision, &record.Nonce,
		&record.Ciphertext, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if _, credentialErr := r.Credential(ctx, id); credentialErr != nil {
			return CredentialSecretRecord{}, credentialErr
		}
		return CredentialSecretRecord{}, ErrSecretNotConfigured
	}
	if err != nil {
		return CredentialSecretRecord{}, fmt.Errorf("platformdb: read credential secret: %w", err)
	}
	record.Nonce = bytes.Clone(record.Nonce)
	record.Ciphertext = bytes.Clone(record.Ciphertext)
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return record, nil
}

// PutCredentialSecret atomically replaces the encrypted envelope and marks the
// metadata configured. If any statement fails, the prior envelope remains.
func (r *SecretRepository) PutCredentialSecret(ctx context.Context, id string, record CredentialSecretRecord) error {
	if err := r.requireStore(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" || record.EnvelopeVersion <= 0 || record.SecretRevision <= 0 || len(record.Nonce) == 0 || len(record.Ciphertext) == 0 {
		return ErrSecretNotConfigured
	}
	stamp := normalizedTimestamp(record.UpdatedAt)
	created := normalizedTimestamp(record.CreatedAt)
	ctx = normalizeContext(ctx)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformdb: begin credential secret update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM credentials WHERE id = ?)", id).Scan(&exists); err != nil {
		return fmt.Errorf("platformdb: verify credential: %w", err)
	}
	if exists != 1 {
		return ErrCredentialNotFound
	}
	var currentRevision int64
	secretErr := tx.QueryRowContext(ctx,
		"SELECT secret_revision FROM credential_secrets WHERE credential_id = ?", id).Scan(&currentRevision)
	switch {
	case secretErr == nil:
		if record.SecretRevision <= currentRevision {
			return ErrCredentialRevisionConflict
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE credential_secrets
SET envelope_version = ?, secret_revision = ?, nonce = ?, ciphertext = ?, updated_at = ?
WHERE credential_id = ?`,
			record.EnvelopeVersion, record.SecretRevision,
			append([]byte(nil), record.Nonce...), append([]byte(nil), record.Ciphertext...),
			stamp.Unix(), id); err != nil {
			return fmt.Errorf("platformdb: update credential secret: %w", err)
		}
	case errors.Is(secretErr, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
INSERT INTO credential_secrets
(credential_id, envelope_version, secret_revision, nonce, ciphertext, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, record.EnvelopeVersion, record.SecretRevision,
			append([]byte(nil), record.Nonce...), append([]byte(nil), record.Ciphertext...),
			created.Unix(), stamp.Unix()); err != nil {
			return fmt.Errorf("platformdb: insert credential secret: %w", err)
		}
	default:
		return fmt.Errorf("platformdb: inspect credential secret revision: %w", secretErr)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE credentials SET configured = 1, updated_at = ? WHERE id = ?", stamp.Unix(), id); err != nil {
		return fmt.Errorf("platformdb: mark credential configured: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformdb: commit credential secret: %w", err)
	}
	committed = true
	return nil
}
