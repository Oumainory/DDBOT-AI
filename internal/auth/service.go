package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/csrf"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/session"
)

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrRateLimited        = errors.New("auth: login rate limited")
	ErrUnavailable        = errors.New("auth: unavailable")
)

const (
	DefaultSetupTokenTTL = 30 * time.Minute
	DefaultSessionTTL    = 24 * time.Hour
)

type Clock func() time.Time

// PasswordHasher is the expensive password derivation boundary used during
// setup. Production services use HashPassword; tests may inject a counting or
// otherwise deterministic implementation without changing the Argon2id
// policy itself.
type PasswordHasher func(string) (string, error)

type Config struct {
	Now            Clock
	Random         io.Reader
	SetupTokenTTL  time.Duration
	SessionTTL     time.Duration
	RateLimiter    *RateLimiter
	PasswordHasher PasswordHasher
}

type Service struct {
	repo           *platformdb.AuthRepository
	now            Clock
	random         io.Reader
	setupTokenTTL  time.Duration
	sessionTTL     time.Duration
	rateLimiter    *RateLimiter
	passwordHasher PasswordHasher
}

type BootstrapResult struct {
	Created   bool
	Token     string
	ExpiresAt time.Time
}

type SetupResult struct {
	Admin platformdb.AdministratorRecord
}

type LoginResult struct {
	Admin        platformdb.AdministratorRecord
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

func NewService(repo *platformdb.AuthRepository, config Config) *Service {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.SetupTokenTTL <= 0 {
		config.SetupTokenTTL = DefaultSetupTokenTTL
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = DefaultSessionTTL
	}
	if config.RateLimiter == nil {
		config.RateLimiter = NewRateLimiter(RateLimiterConfig{Now: config.Now})
	}
	if config.PasswordHasher == nil {
		config.PasswordHasher = HashPassword
	}
	return &Service{
		repo:           repo,
		now:            config.Now,
		random:         config.Random,
		setupTokenTTL:  config.SetupTokenTTL,
		sessionTTL:     config.SessionTTL,
		rateLimiter:    config.RateLimiter,
		passwordHasher: config.PasswordHasher,
	}
}

// Bootstrap creates a token only when setup is incomplete and there is no
// active token. The raw token is returned solely for the caller's dedicated
// interactive/container bootstrap output; it is never part of an HTTP API.
func (s *Service) Bootstrap(ctx context.Context) (BootstrapResult, error) {
	if s == nil || s.repo == nil {
		return BootstrapResult{}, ErrUnavailable
	}
	state, err := s.repo.State(ctx)
	if err != nil {
		return BootstrapResult{}, ErrUnavailable
	}
	if state == platformdb.AuthReady {
		return BootstrapResult{}, nil
	}
	raw, hash, err := session.GenerateToken(s.random)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("auth: generate setup token: %w", err)
	}
	now := s.now().UTC()
	expires := now.Add(s.setupTokenTTL)
	created, err := s.repo.EnsureSetupToken(ctx, hash, now, expires)
	if err != nil {
		return BootstrapResult{}, ErrUnavailable
	}
	if !created {
		return BootstrapResult{}, nil
	}
	return BootstrapResult{Created: true, Token: raw, ExpiresAt: expires}, nil
}

func (s *Service) Setup(ctx context.Context, setupToken, username, password string) (SetupResult, error) {
	if s == nil || s.repo == nil {
		return SetupResult{}, ErrUnavailable
	}
	if strings.TrimSpace(setupToken) == "" {
		return SetupResult{}, platformdb.ErrSetupTokenInvalid
	}
	normalizedUsername, err := NormalizeUsername(username)
	if err != nil {
		return SetupResult{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return SetupResult{}, err
	}
	tokenHash := hashSetupToken(setupToken)
	if err := s.repo.ValidateSetupToken(ctx, tokenHash, s.now()); err != nil {
		return SetupResult{}, err
	}
	passwordHash, err := s.passwordHasher(password)
	if err != nil {
		return SetupResult{}, err
	}
	adminID, err := randomIdentifier(s.random, "admin_")
	if err != nil {
		return SetupResult{}, fmt.Errorf("auth: generate administrator id: %w", err)
	}
	admin, err := s.repo.CreateAdministrator(ctx, tokenHash, adminID, normalizedUsername, passwordHash, s.now())
	if err != nil {
		return SetupResult{}, err
	}
	return SetupResult{Admin: admin}, nil
}

// ResetAdministratorPassword is intentionally separate from Setup. It keeps
// setup permanently completed, derives a fresh Argon2id hash through the same
// injectable boundary used by setup, and lets the repository atomically
// replace the hash while revoking existing sessions.
func (s *Service) ResetAdministratorPassword(ctx context.Context, password string) error {
	if s == nil || s.repo == nil {
		return ErrUnavailable
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	passwordHash, err := s.passwordHasher(password)
	if err != nil {
		return err
	}
	return s.repo.ResetAdministratorPassword(ctx, passwordHash, s.now())
}

func (s *Service) Login(ctx context.Context, username, password, rateKey, userAgent string) (LoginResult, error) {
	if s == nil || s.repo == nil {
		return LoginResult{}, ErrUnavailable
	}
	normalizedUsername, usernameErr := NormalizeUsername(username)
	if usernameErr != nil {
		normalizedUsername = strings.ToLower(strings.TrimSpace(username))
	}
	if !s.rateLimiter.Allow(rateKeyFor(rateKey, normalizedUsername)) {
		return LoginResult{}, ErrRateLimited
	}
	if usernameErr != nil || ValidatePassword(password) != nil {
		s.rateLimiter.Failure(rateKeyFor(rateKey, normalizedUsername))
		return LoginResult{}, ErrInvalidCredentials
	}
	admin, err := s.repo.AdministratorByUsername(ctx, normalizedUsername)
	if err != nil {
		if errors.Is(err, platformdb.ErrAdminNotFound) {
			s.rateLimiter.Failure(rateKeyFor(rateKey, normalizedUsername))
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, ErrUnavailable
	}
	if admin.Disabled {
		s.rateLimiter.Failure(rateKeyFor(rateKey, normalizedUsername))
		return LoginResult{}, ErrInvalidCredentials
	}
	valid, verifyErr := VerifyPassword(password, admin.PasswordHash)
	if verifyErr != nil || !valid {
		s.rateLimiter.Failure(rateKeyFor(rateKey, normalizedUsername))
		return LoginResult{}, ErrInvalidCredentials
	}
	now := s.now().UTC()
	expires := now.Add(s.sessionTTL)
	rawSession, sessionHash, err := session.GenerateToken(s.random)
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	csrfToken, err := csrf.GenerateToken(s.random)
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	if err := s.repo.CreateSession(ctx, platformdb.SessionRecord{
		SessionIDHash: sessionHash,
		AdminID:       admin.ID,
		Username:      admin.Username,
		CreatedAt:     now,
		LastSeenAt:    now,
		ExpiresAt:     expires,
		CSRFSecret:    csrfToken,
		UserAgentHash: session.HashMetadata(userAgent),
	}); err != nil {
		return LoginResult{}, ErrUnavailable
	}
	s.rateLimiter.Reset(rateKeyFor(rateKey, normalizedUsername))
	return LoginResult{Admin: admin, SessionToken: rawSession, CSRFToken: csrfToken, ExpiresAt: expires}, nil
}

func (s *Service) LookupSession(ctx context.Context, rawToken string) (platformdb.SessionRecord, error) {
	if s == nil || s.repo == nil || strings.TrimSpace(rawToken) == "" {
		return platformdb.SessionRecord{}, ErrInvalidCredentials
	}
	record, err := s.repo.LookupSession(ctx, session.HashToken(rawToken), s.now())
	if err != nil {
		if errors.Is(err, platformdb.ErrSessionNotFound) || errors.Is(err, platformdb.ErrSessionExpired) || errors.Is(err, platformdb.ErrSessionRevoked) || errors.Is(err, platformdb.ErrAdminDisabled) {
			return platformdb.SessionRecord{}, ErrInvalidCredentials
		}
		return platformdb.SessionRecord{}, ErrUnavailable
	}
	return record, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if s == nil || s.repo == nil {
		return ErrUnavailable
	}
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	if err := s.repo.RevokeSession(ctx, session.HashToken(rawToken), s.now()); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) State(ctx context.Context) (platformdb.AuthState, error) {
	if s == nil || s.repo == nil {
		return "", ErrUnavailable
	}
	state, err := s.repo.State(ctx)
	if err != nil {
		return "", ErrUnavailable
	}
	return state, nil
}

func hashSetupToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func randomIdentifier(reader io.Reader, prefix string) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	data := make([]byte, 16)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data), nil
}

func rateKeyFor(remote, username string) string {
	return strings.TrimSpace(remote) + "\x00" + strings.TrimSpace(username)
}
