package pairing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

type fakeVerifier struct{ verified bool }

func (v fakeVerifier) VerifyTarget(_ context.Context, _ string, value Verification) (Verification, error) {
	value.Verified = v.verified
	return value, nil
}

func pairingFixture(t *testing.T) (*Service, string) {
	t.Helper()
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	domainRepo := platformdb.NewDomainRepository(store)
	now := time.Unix(1700000000, 0).UTC()
	connector, err := domainRepo.CreateConnector(ctx, domain.Connector{Kind: domain.ConnectorTelegram, Name: "Telegram", Role: domain.ConnectorExtra, Enabled: true, Status: domain.ConnectorActive, Endpoint: "https://telegram.test"})
	if err != nil {
		t.Fatal(err)
	}
	repo := platformdb.NewMigrationRepository(store)
	return New(Config{Repository: repo, Domain: domainRepo, Now: func() time.Time { return now }}), connector.ID
}

func TestPairingCodeIsHashOnlyAndOneTime(t *testing.T) {
	service, connectorID := pairingFixture(t)
	created, err := service.Create(context.Background(), connectorID, "admin", "session")
	if err != nil {
		t.Fatal(err)
	}
	if created.Code == "" {
		t.Fatal("plaintext code missing from one-time response")
	}
	stored, err := service.repo.Pairing(context.Background(), created.Challenge.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CodeSHA256 == created.Code || stored.CodeSHA256 != HashCode(created.Code) {
		t.Fatalf("stored code digest = %q", stored.CodeSHA256)
	}
	if _, err := service.VerifyCode(context.Background(), created.Challenge.ChallengeID, created.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyCode(context.Background(), created.Challenge.ChallengeID, created.Code); !errors.Is(err, ErrConsumed) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestPairingLocksAfterFiveFailures(t *testing.T) {
	service, connectorID := pairingFixture(t)
	created, err := service.Create(context.Background(), connectorID, "admin", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxFailures; i++ {
		_, err = service.VerifyCode(context.Background(), created.Challenge.ChallengeID, "wrong")
		if i < MaxFailures-1 && !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt %d = %v", i, err)
		}
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("fifth attempt = %v", err)
	}
}

func TestPairingVerificationFailureDoesNotConsumeCode(t *testing.T) {
	service, connectorID := pairingFixture(t)
	created, err := service.Create(context.Background(), connectorID, "admin", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.VerifyTarget(context.Background(), created.Challenge.ChallengeID, created.Code, Verification{TargetType: domain.TargetGroup, ExternalID: "-100", DisplayName: "test"}, fakeVerifier{})
	if !errors.Is(err, ErrVerification) {
		t.Fatalf("verification error = %v", err)
	}
	if _, err := service.VerifyCode(context.Background(), created.Challenge.ChallengeID, created.Code); err != nil {
		t.Fatalf("code consumed after failed verification: %v", err)
	}
}
