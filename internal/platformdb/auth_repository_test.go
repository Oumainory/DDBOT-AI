package platformdb

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/session"
)

const testPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestAuthRepositorySetupTokenIsHashedAndConsumedAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "auth.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	now := time.Unix(1700000000, 0).UTC()
	rawToken := "setup-token-plaintext"
	tokenHash := session.HashToken(rawToken)
	created, err := repository.EnsureSetupToken(ctx, tokenHash, now, now.Add(30*time.Minute))
	if err != nil || !created {
		t.Fatalf("EnsureSetupToken = %v, %v", created, err)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, "SELECT token_hash FROM setup_tokens WHERE singleton = 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != tokenHash || stored == rawToken {
		t.Fatalf("stored setup token = %q", stored)
	}
	if state, err := repository.State(ctx); err != nil || state != AuthSetupRequired {
		t.Fatalf("state before setup = %q, %v", state, err)
	}
	admin, err := repository.CreateAdministrator(ctx, tokenHash, "admin_1", "admin", testPasswordHash, now)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Username != "admin" {
		t.Fatalf("administrator = %#v", admin)
	}
	if state, err := repository.State(ctx); err != nil || state != AuthReady {
		t.Fatalf("state after setup = %q, %v", state, err)
	}
	var consumedAt int64
	if err := store.db.QueryRowContext(ctx, "SELECT consumed_at FROM setup_tokens WHERE singleton = 1").Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if consumedAt == 0 {
		t.Fatal("setup token was not marked consumed")
	}
	if _, err := repository.CreateAdministrator(ctx, tokenHash, "admin_2", "other", testPasswordHash, now); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("setup replay error = %v, want ErrSetupComplete", err)
	}
}

func TestAuthRepositoryConcurrentSetupCreatesOneAdministrator(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "concurrent.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	now := time.Unix(1700000000, 0).UTC()
	tokenHash := session.HashToken("concurrent-setup-token")
	if created, err := repository.EnsureSetupToken(ctx, tokenHash, now, now.Add(time.Hour)); err != nil || !created {
		t.Fatalf("EnsureSetupToken = %v, %v", created, err)
	}

	var wait sync.WaitGroup
	errorsCh := make(chan error, 2)
	for _, adminID := range []string{"admin_a", "admin_b"} {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			_, setupErr := repository.CreateAdministrator(ctx, tokenHash, id, id, testPasswordHash, now)
			errorsCh <- setupErr
		}(adminID)
	}
	wait.Wait()
	close(errorsCh)
	successes := 0
	for setupErr := range errorsCh {
		if setupErr == nil {
			successes++
			continue
		}
		if !errors.Is(setupErr, ErrSetupComplete) && !errors.Is(setupErr, ErrAdminExists) {
			t.Fatalf("concurrent setup error = %v", setupErr)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent setup successes = %d, want 1", successes)
	}
	if count := rawInt(t, store.db, "SELECT COUNT(*) FROM administrators"); count != 1 {
		t.Fatalf("administrator count = %d, want 1", count)
	}
}

func TestAuthRepositorySessionStoresHashAndEnforcesExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "sessions.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	now := time.Unix(1700000000, 0).UTC()
	if _, err := repository.CreateAdministrator(ctx, session.HashToken("setup"), "admin_1", "admin", testPasswordHash, now); !errors.Is(err, ErrSetupTokenMissing) {
		// This also asserts that setup cannot bypass the persisted token row.
		t.Fatalf("administrator without setup token = %v", err)
	}
	setupHash := session.HashToken("setup")
	if _, err := repository.EnsureSetupToken(ctx, setupHash, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateAdministrator(ctx, setupHash, "admin_1", "admin", testPasswordHash, now); err != nil {
		t.Fatal(err)
	}
	rawSession := "server-session-secret"
	record := SessionRecord{
		SessionIDHash: session.HashToken(rawSession),
		AdminID:       "admin_1",
		CreatedAt:     now,
		LastSeenAt:    now,
		ExpiresAt:     now.Add(time.Hour),
		CSRFSecret:    "csrf-secret",
		UserAgentHash: session.HashMetadata("test-agent"),
	}
	if err := repository.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err := store.db.QueryRowContext(ctx, "SELECT session_id_hash FROM sessions").Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != record.SessionIDHash || storedHash == rawSession {
		t.Fatalf("stored session hash = %q", storedHash)
	}
	lookedUp, err := repository.LookupSession(ctx, record.SessionIDHash, now.Add(10*time.Minute))
	if err != nil || lookedUp.AdminID != "admin_1" || lookedUp.CSRFSecret != record.CSRFSecret {
		t.Fatalf("LookupSession = %#v, %v", lookedUp, err)
	}
	if err := repository.RevokeSession(ctx, record.SessionIDHash, now.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.LookupSession(ctx, record.SessionIDHash, now.Add(21*time.Minute)); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked session error = %v", err)
	}
}
