// Package pairing implements the short-lived Telegram target pairing
// protocol. It stores only a SHA-256 digest of the one-time code; the
// plaintext is returned to the caller exactly once from Create.
package pairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

const (
	DefaultLifetime = 10 * time.Minute
	MaxFailures     = 5
)

var (
	ErrInvalid           = platformdb.ErrPairingInvalid
	ErrExpired           = platformdb.ErrPairingExpired
	ErrLocked            = platformdb.ErrPairingLocked
	ErrConsumed          = platformdb.ErrPairingConsumed
	ErrVerification      = errors.New("pairing: telegram_verification_failed")
	ErrUnsupportedTarget = errors.New("pairing: unsupported target")
)

type Config struct {
	Repository *platformdb.MigrationRepository
	Domain     *platformdb.DomainRepository
	Now        func() time.Time
	Lifetime   time.Duration
}

type Service struct {
	repo     *platformdb.MigrationRepository
	domain   *platformdb.DomainRepository
	now      func() time.Time
	lifetime time.Duration
}

type Created struct {
	Challenge platformdb.PairingChallenge `json:"challenge"`
	Code      string                      `json:"-"`
}

type Verification struct {
	TargetType  domain.TargetType
	ExternalID  string
	DisplayName string
	Metadata    map[string]any
	Verified    bool
}

// Verifier is implemented by the Telegram adapter. A verifier must establish
// membership and sendability (or an explicitly supported safe probe) before
// returning Verified=true. A manual fallback cannot bypass this contract.
type Verifier interface {
	VerifyTarget(context.Context, string, Verification) (Verification, error)
}

func New(config Config) *Service {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	life := config.Lifetime
	if life <= 0 {
		life = DefaultLifetime
	}
	return &Service{repo: config.Repository, domain: config.Domain, now: now, lifetime: life}
}
func (s *Service) require() error {
	if s == nil || s.repo == nil || s.domain == nil {
		return platformdb.ErrDomainUnavailable
	}
	return nil
}
func (s *Service) stamp() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return fmt.Sprintf("sha256:%x", sum[:])
}

// HashCode is exported for adapter implementations and tests; it never
// returns the plaintext pairing code.
func HashCode(code string) string { return hashCode(strings.TrimSpace(code)) }

// Create generates 32 random bytes and returns an URL-safe, human-copyable
// code. Only this return value contains the plaintext code.
func (s *Service) Create(ctx context.Context, connectorID, adminID, sessionID string) (Created, error) {
	if err := s.require(); err != nil {
		return Created{}, err
	}
	connector, err := s.domain.Connector(ctx, connectorID)
	if err != nil {
		return Created{}, err
	}
	if connector.Kind != domain.ConnectorTelegram {
		return Created{}, ErrUnsupportedTarget
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Created{}, err
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	now := s.stamp()
	id, err := domain.NewID()
	if err != nil {
		return Created{}, err
	}
	challenge := platformdb.PairingChallenge{ChallengeID: id, ConnectorID: connectorID, CodeSHA256: hashCode(code), CreatedAt: now.Unix(), ExpiresAt: now.Add(s.lifetime).Unix(), AdminID: strings.TrimSpace(adminID), SessionID: strings.TrimSpace(sessionID)}
	if err := s.repo.CreatePairing(ctx, challenge); err != nil {
		return Created{}, err
	}
	s.audit(ctx, challenge, "telegram.pairing.create", "created", nil)
	stored, err := s.repo.Pairing(ctx, challenge.ChallengeID)
	if err != nil {
		return Created{}, err
	}
	return Created{Challenge: stored, Code: code}, nil
}

func (s *Service) VerifyCode(ctx context.Context, challengeID, code string) (platformdb.PairingChallenge, error) {
	if err := s.require(); err != nil {
		return platformdb.PairingChallenge{}, err
	}
	_, err := s.checkCode(ctx, challengeID, code)
	if err != nil {
		return platformdb.PairingChallenge{}, err
	}
	if err := s.repo.ConsumePairing(ctx, challengeID, s.stamp()); err != nil {
		return platformdb.PairingChallenge{}, ErrConsumed
	}
	verified, readErr := s.repo.Pairing(ctx, challengeID)
	if readErr == nil {
		s.audit(ctx, verified, "telegram.pairing.verify", "success", nil)
	}
	return verified, readErr
}

// VerifyTargetByCode is the incoming Telegram-command boundary. Dashboard
// verification already knows the challenge id; a chat message contains only
// `/bind CODE`, so resolve the SHA-256 digest to the single active challenge
// before running the exact same expiry/failure/consume/target checks.
func (s *Service) VerifyTargetByCode(ctx context.Context, code string, verification Verification, verifier Verifier) (domain.Target, error) {
	if err := s.require(); err != nil {
		return domain.Target{}, err
	}
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return domain.Target{}, ErrInvalid
	}
	challenge, err := s.repo.PairingByCodeHash(ctx, hashCode(trimmed))
	if err != nil {
		return domain.Target{}, ErrInvalid
	}
	return s.VerifyTarget(ctx, challenge.ChallengeID, trimmed, verification, verifier)
}

// VerifyTargetForConnector binds the URL resource identity to the durable
// challenge before performing the one-time verification.  Without this
// check, a valid challenge could be replayed through a different connector's
// endpoint and the path would be misleading even though the target row still
// used the challenge's connector.
func (s *Service) VerifyTargetForConnector(ctx context.Context, connectorID, challengeID, code string, verification Verification, verifier Verifier) (domain.Target, error) {
	if err := s.require(); err != nil {
		return domain.Target{}, err
	}
	challenge, err := s.repo.Pairing(ctx, strings.TrimSpace(challengeID))
	if err != nil || challenge.ConnectorID != strings.TrimSpace(connectorID) {
		return domain.Target{}, ErrInvalid
	}
	return s.VerifyTarget(ctx, challenge.ChallengeID, code, verification, verifier)
}

// VerifyTargetByCodeForConnector is the code-only form used by clients that
// address `/pairing/verify` without putting the challenge id in the URL. The
// digest lookup remains hash-only, then the connector resource is checked
// before any failure counter or consume operation is attempted.
func (s *Service) VerifyTargetByCodeForConnector(ctx context.Context, connectorID, code string, verification Verification, verifier Verifier) (domain.Target, error) {
	if err := s.require(); err != nil {
		return domain.Target{}, err
	}
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return domain.Target{}, ErrInvalid
	}
	challenge, err := s.repo.PairingByCodeHash(ctx, hashCode(trimmed))
	if err != nil || challenge.ConnectorID != strings.TrimSpace(connectorID) {
		return domain.Target{}, ErrInvalid
	}
	return s.VerifyTarget(ctx, challenge.ChallengeID, trimmed, verification, verifier)
}

func (s *Service) checkCode(ctx context.Context, challengeID, code string) (platformdb.PairingChallenge, error) {
	challenge, err := s.repo.Pairing(ctx, challengeID)
	if err != nil {
		return platformdb.PairingChallenge{}, ErrInvalid
	}
	now := s.stamp().Unix()
	if challenge.ConsumedAt != nil {
		s.audit(ctx, challenge, "telegram.pairing.verify", "consumed", nil)
		return platformdb.PairingChallenge{}, ErrConsumed
	}
	if challenge.LockedAt != nil || challenge.FailureCount >= MaxFailures {
		s.audit(ctx, challenge, "telegram.pairing.verify", "locked", nil)
		return platformdb.PairingChallenge{}, ErrLocked
	}
	if now >= challenge.ExpiresAt {
		s.audit(ctx, challenge, "telegram.pairing.verify", "expired", nil)
		return platformdb.PairingChallenge{}, ErrExpired
	}
	supplied := hashCode(strings.TrimSpace(code))
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(challenge.CodeSHA256)) != 1 {
		count, failErr := s.repo.FailPairing(ctx, challengeID, s.stamp())
		if failErr != nil {
			return platformdb.PairingChallenge{}, ErrInvalid
		}
		if count >= MaxFailures {
			challenge.FailureCount = count
			s.audit(ctx, challenge, "telegram.pairing.verify", "locked", nil)
			return platformdb.PairingChallenge{}, ErrLocked
		}
		challenge.FailureCount = count
		s.audit(ctx, challenge, "telegram.pairing.verify", "invalid", nil)
		return platformdb.PairingChallenge{}, ErrInvalid
	}
	return challenge, nil
}

func (s *Service) VerifyTarget(ctx context.Context, challengeID, code string, verification Verification, verifier Verifier) (domain.Target, error) {
	if err := s.require(); err != nil {
		return domain.Target{}, err
	}
	challenge, err := s.checkCode(ctx, challengeID, code)
	if err != nil {
		return domain.Target{}, err
	}
	if verifier == nil {
		return domain.Target{}, ErrVerification
	}
	verification.ExternalID = strings.TrimSpace(verification.ExternalID)
	if (verification.TargetType != domain.TargetGroup && verification.TargetType != domain.TargetChannel) || verification.ExternalID == "" {
		return domain.Target{}, ErrUnsupportedTarget
	}
	verified, err := verifier.VerifyTarget(ctx, challenge.ConnectorID, verification)
	if err != nil || !verified.Verified {
		s.audit(ctx, challenge, "telegram.pairing.manual_verification", "failed", map[string]any{"target_type": verification.TargetType})
		return domain.Target{}, ErrVerification
	}
	if err := s.repo.ConsumePairing(ctx, challengeID, s.stamp()); err != nil {
		return domain.Target{}, ErrConsumed
	}
	metadata := verified.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["verified"] = true
	metadata["verified_at"] = s.stamp().UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(metadata)
	target := domain.Target{ConnectorID: challenge.ConnectorID, TargetType: verified.TargetType, ExternalID: verified.ExternalID, DisplayName: verified.DisplayName, MetadataJSON: string(raw), Status: domain.TargetResolved}
	target, err = s.domain.UpsertTarget(ctx, target)
	if err != nil {
		s.audit(ctx, challenge, "telegram.pairing.manual_verification", "failed", map[string]any{"target_type": verified.TargetType})
		return domain.Target{}, err
	}
	s.audit(ctx, challenge, "telegram.pairing.manual_verification", "success", map[string]any{"target_type": target.TargetType})
	return target, nil
}

// audit records only challenge identity and outcome. The plaintext code is
// deliberately not available to this helper and can therefore never enter
// the audit ledger.
func (s *Service) audit(ctx context.Context, challenge platformdb.PairingChallenge, action, outcome string, metadata map[string]any) {
	if s == nil || s.repo == nil {
		return
	}
	raw := "{}"
	if metadata != nil {
		if data, err := json.Marshal(metadata); err == nil {
			raw = string(data)
		}
	}
	_, _ = s.repo.AppendAudit(ctx, platformdb.AuditEntry{
		OccurredAt: s.stamp().Unix(), PrincipalID: challenge.AdminID, Action: action,
		ResourceType: "telegram_pairing", ResourceID: challenge.ChallengeID,
		Outcome: outcome, MetadataJSON: raw,
	})
}
