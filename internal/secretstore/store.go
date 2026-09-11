package secretstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

var (
	ErrSecretStoreUnavailable = errors.New("secretstore: secret store is unavailable")
	ErrSecretStoreRecovery    = errors.New("secretstore: secret store is in recovery")
	ErrCredentialNotFound     = errors.New("secretstore: credential not found")
	ErrSecretNotConfigured    = errors.New("secretstore: credential secret is not configured")
)

type State string

const (
	StateReady       State = "ready"
	StateRecovery    State = "recovery"
	StateUnavailable State = "unavailable"
)

type Config struct {
	MasterKeyFile  string
	Random         io.Reader
	Now            func() time.Time
	MaxSecretBytes int
}

type Service struct {
	repo           *platformdb.SecretRepository
	key            []byte
	state          State
	initErr        error
	random         io.Reader
	now            func() time.Time
	maxSecretBytes int
	mu             sync.Mutex
}

type CredentialInput struct {
	ID     string
	Type   string
	Label  string
	Source string
}

type CredentialView struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Label      string    `json:"label"`
	Configured bool      `json:"configured"`
	Masked     bool      `json:"masked"`
	Source     string    `json:"source"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// New explicitly initializes the Secret Store. It never starts a goroutine or
// writes a Master Key to SQLite. Initialization failures are represented by a
// stable state so callers can keep Legacy and P1B Auth running.
func New(ctx context.Context, repo *platformdb.SecretRepository, cfg Config) *Service {
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxSecretBytes <= 0 || cfg.MaxSecretBytes > MaxSecretBytes {
		cfg.MaxSecretBytes = MaxSecretBytes
	}
	service := &Service{
		repo:           repo,
		state:          StateUnavailable,
		random:         cfg.Random,
		now:            cfg.Now,
		maxSecretBytes: cfg.MaxSecretBytes,
	}
	if repo == nil {
		service.initErr = ErrSecretStoreUnavailable
		return service
	}
	ctx = normalizeSecretContext(ctx)
	existing, err := repo.HasStoredSecretData(ctx)
	if err != nil {
		service.initErr = err
		return service
	}
	key, err := (FileKeyProvider{Path: cfg.MasterKeyFile, Random: cfg.Random}).LoadOrCreate(existing)
	if err != nil {
		service.initErr = err
		if errors.Is(err, ErrMasterKeyMissing) || errors.Is(err, ErrMasterKeyInvalid) {
			service.state = StateRecovery
		}
		return service
	}
	service.key = append([]byte(nil), key...)
	state, stateErr := repo.SecretStoreState(ctx)
	if stateErr == nil {
		if err := decryptSentinel(service.key, Envelope{
			Version:    state.EnvelopeVersion,
			Nonce:      state.SentinelNonce,
			Ciphertext: state.SentinelCipher,
		}); err != nil {
			service.initErr = err
			service.state = StateRecovery
			return service
		}
		if !service.validateConsistency(ctx) {
			return service
		}
		return service
	}
	if !errors.Is(stateErr, platformdb.ErrSecretStoreStateMissing) {
		service.initErr = stateErr
		return service
	}
	if existing {
		// Ciphertext without a sentinel is a durable invariant failure. Even a
		// valid-looking key must not silently bless an unverifiable database.
		service.initErr = ErrSecretStoreRecovery
		service.state = StateRecovery
		return service
	}
	sentinel, err := encryptSentinel(service.key, cfg.Random)
	if err != nil {
		service.initErr = err
		return service
	}
	stamp := cfg.Now().UTC()
	err = repo.CreateSecretStoreState(ctx, platformdb.SecretStoreStateRecord{
		EnvelopeVersion: sentinel.Version,
		SentinelNonce:   sentinel.Nonce,
		SentinelCipher:  sentinel.Ciphertext,
		CreatedAt:       stamp,
		UpdatedAt:       stamp,
	})
	if errors.Is(err, platformdb.ErrSecretStoreStateExists) {
		// A concurrent initializer won the insert. Verify the durable sentinel
		// rather than replacing it.
		state, stateErr = repo.SecretStoreState(ctx)
		if stateErr == nil && decryptSentinel(service.key, Envelope{Version: state.EnvelopeVersion, Nonce: state.SentinelNonce, Ciphertext: state.SentinelCipher}) == nil {
			if !service.validateConsistency(ctx) {
				return service
			}
			return service
		}
		service.initErr = ErrSecretStoreRecovery
		service.state = StateRecovery
		return service
	}
	if err != nil {
		service.initErr = err
		return service
	}
	if !service.validateConsistency(ctx) {
		return service
	}
	return service
}

// validateConsistency is the final startup gate after the sentinel has
// authenticated the Master Key. An invariant mismatch is a recoverable
// durable state; an inability to run the read-only check remains unavailable.
func (s *Service) validateConsistency(ctx context.Context) bool {
	err := s.repo.ValidateSecretStoreConsistency(ctx)
	if err == nil {
		s.state = StateReady
		return true
	}
	s.initErr = err
	if errors.Is(err, platformdb.ErrSecretStoreInvariant) {
		s.state = StateRecovery
	}
	return false
}

func normalizeSecretContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *Service) State() State {
	if s == nil {
		return StateUnavailable
	}
	return s.state
}

// Check implements the platformdb health dependency boundary.
func (s *Service) Check(context.Context) (platformdb.CheckStatus, string) {
	switch s.State() {
	case StateReady:
		return platformdb.CheckOK, "secret_store_ready"
	case StateRecovery:
		return platformdb.CheckDegraded, "secret_store_recovery"
	default:
		return platformdb.CheckDegraded, "secret_store_unavailable"
	}
}

func (s *Service) requireReady() error {
	if s == nil {
		return ErrSecretStoreUnavailable
	}
	switch s.state {
	case StateReady:
		return nil
	case StateRecovery:
		return ErrSecretStoreRecovery
	default:
		return ErrSecretStoreUnavailable
	}
}

func validateCredentialInput(input CredentialInput) error {
	if !validOpaque(input.ID, 256) || !validToken(input.Type, 128) || !validToken(input.Source, 64) || len(input.Label) > 256 {
		return ErrSecretInput
	}
	return nil
}

func validOpaque(value string, max int) bool {
	if strings.TrimSpace(value) == "" || len(value) > max || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validToken(value string, max int) bool {
	if !validOpaque(value, max) {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func (s *Service) CreateCredential(ctx context.Context, input CredentialInput) (CredentialView, error) {
	if err := s.requireReady(); err != nil {
		return CredentialView{}, err
	}
	if err := validateCredentialInput(input); err != nil {
		return CredentialView{}, err
	}
	stamp := s.now().UTC()
	if err := s.repo.CreateCredential(ctx, platformdb.CredentialMetadataRecord{
		ID: input.ID, Type: input.Type, Label: input.Label, Source: input.Source,
		CreatedAt: stamp, UpdatedAt: stamp,
	}); err != nil {
		return CredentialView{}, err
	}
	return CredentialView{ID: input.ID, Type: input.Type, Label: input.Label, Source: input.Source, UpdatedAt: stamp}, nil
}

func viewOf(record platformdb.CredentialMetadataRecord) CredentialView {
	return CredentialView{
		ID: record.ID, Type: record.Type, Label: record.Label, Source: record.Source,
		Configured: record.Configured, Masked: record.Configured, UpdatedAt: record.UpdatedAt,
	}
}

func (s *Service) Metadata(ctx context.Context, id string) (CredentialView, error) {
	if s == nil || s.repo == nil {
		return CredentialView{}, ErrSecretStoreUnavailable
	}
	record, err := s.repo.Credential(ctx, id)
	if errors.Is(err, platformdb.ErrCredentialNotFound) {
		return CredentialView{}, ErrCredentialNotFound
	}
	if err != nil {
		return CredentialView{}, err
	}
	return viewOf(record), nil
}

func (s *Service) MetadataList(ctx context.Context) ([]CredentialView, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSecretStoreUnavailable
	}
	records, err := s.repo.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]CredentialView, 0, len(records))
	for _, record := range records {
		views = append(views, viewOf(record))
	}
	return views, nil
}

func (s *Service) SetSecret(ctx context.Context, id string, plaintext []byte) error {
	if err := s.requireReady(); err != nil {
		return err
	}
	if len(plaintext) == 0 || len(plaintext) > s.maxSecretBytes {
		return ErrSecretInput
	}
	// Serialize revision selection per explicit Service owner. This is not a
	// background worker or global lock; it only prevents two in-process updates
	// from selecting the same revision.
	s.mu.Lock()
	defer s.mu.Unlock()
	metadata, err := s.repo.Credential(ctx, id)
	if errors.Is(err, platformdb.ErrCredentialNotFound) {
		return ErrCredentialNotFound
	}
	if err != nil {
		return err
	}
	current, err := s.repo.CredentialSecret(ctx, id)
	revision := int64(1)
	if err == nil {
		revision = current.SecretRevision + 1
	} else if !errors.Is(err, platformdb.ErrSecretNotConfigured) {
		return err
	}
	envelope, err := Encrypt(s.key, metadata.ID, metadata.Type, revision, plaintext, s.random)
	if err != nil {
		return err
	}
	stamp := s.now().UTC()
	return s.repo.PutCredentialSecret(ctx, id, platformdb.CredentialSecretRecord{
		EnvelopeVersion: envelope.Version,
		SecretRevision:  envelope.Revision,
		Nonce:           envelope.Nonce,
		Ciphertext:      envelope.Ciphertext,
		CreatedAt:       stamp,
		UpdatedAt:       stamp,
	})
}

func (s *Service) ResolveSecret(ctx context.Context, id string) ([]byte, error) {
	if err := s.requireReady(); err != nil {
		return nil, err
	}
	metadata, err := s.repo.Credential(ctx, id)
	if errors.Is(err, platformdb.ErrCredentialNotFound) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.repo.CredentialSecret(ctx, id)
	if errors.Is(err, platformdb.ErrSecretNotConfigured) {
		return nil, ErrSecretNotConfigured
	}
	if err != nil {
		return nil, err
	}
	plaintext, err := Decrypt(s.key, metadata.ID, metadata.Type, Envelope{
		Version: secret.EnvelopeVersion, Revision: secret.SecretRevision,
		Nonce: secret.Nonce, Ciphertext: secret.Ciphertext,
	})
	if err != nil {
		if errors.Is(err, ErrInvalidMasterKey) {
			return nil, ErrSecretStoreRecovery
		}
		return nil, ErrSecretIntegrity
	}
	return plaintext, nil
}

// InitError is intentionally for internal diagnostics/tests only. It is not
// used by health/readiness responses and never contains key material.
func (s *Service) InitError() error {
	if s == nil {
		return ErrSecretStoreUnavailable
	}
	return s.initErr
}

func (s *Service) String() string {
	if s == nil {
		return "secretstore.Service(state=unavailable)"
	}
	return fmt.Sprintf("secretstore.Service(state=%s)", s.state)
}
