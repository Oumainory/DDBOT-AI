package platformdb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/session"
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

func TestAuthRepositoryValidateSetupTokenIsReadOnly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "precheck.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	now := time.Unix(1700000000, 0).UTC()
	rawToken := "precheck-token"
	tokenHash := session.HashToken(rawToken)
	if created, err := repository.EnsureSetupToken(ctx, tokenHash, now, now.Add(time.Hour)); err != nil || !created {
		t.Fatalf("EnsureSetupToken = %v, %v", created, err)
	}
	beforeState, err := repository.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeConsumed := rawInt(t, store.db, "SELECT COUNT(*) FROM setup_tokens WHERE singleton = 1 AND consumed_at IS NOT NULL")
	if err := repository.ValidateSetupToken(ctx, tokenHash, now); err != nil {
		t.Fatalf("valid precheck = %v", err)
	}
	afterState, err := repository.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterConsumed := rawInt(t, store.db, "SELECT COUNT(*) FROM setup_tokens WHERE singleton = 1 AND consumed_at IS NOT NULL")
	if beforeState != afterState || beforeConsumed != afterConsumed || afterConsumed != 0 {
		t.Fatalf("precheck mutated state: before=%q/%d after=%q/%d", beforeState, beforeConsumed, afterState, afterConsumed)
	}

	cases := []struct {
		name string
		hash string
		at   time.Time
		want error
	}{
		{name: "wrong token", hash: session.HashToken("wrong-token"), at: now, want: ErrSetupTokenInvalid},
		{name: "expired token", hash: tokenHash, at: now.Add(time.Hour), want: ErrSetupTokenExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := repository.ValidateSetupToken(ctx, tc.hash, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateSetupToken = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthRepositoryValidateSetupTokenStableStates(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	t.Run("missing token", func(t *testing.T) {
		store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "missing.sqlite")})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := NewAuthRepository(store).ValidateSetupToken(ctx, session.HashToken("missing"), now); !errors.Is(err, ErrSetupTokenMissing) {
			t.Fatalf("ValidateSetupToken = %v, want %v", err, ErrSetupTokenMissing)
		}
	})

	t.Run("consumed token", func(t *testing.T) {
		store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "consumed.sqlite")})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		repository := NewAuthRepository(store)
		hash := session.HashToken("consumed")
		if _, err := repository.EnsureSetupToken(ctx, hash, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, "UPDATE setup_tokens SET consumed_at = ? WHERE singleton = 1", now.Unix()); err != nil {
			t.Fatal(err)
		}
		if err := repository.ValidateSetupToken(ctx, hash, now); !errors.Is(err, ErrSetupTokenConsumed) {
			t.Fatalf("ValidateSetupToken = %v, want %v", err, ErrSetupTokenConsumed)
		}
		if got := rawInt(t, store.db, "SELECT COUNT(*) FROM administrators"); got != 0 {
			t.Fatalf("administrator count = %d, want 0", got)
		}
	})

	t.Run("setup complete", func(t *testing.T) {
		store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "complete.sqlite")})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		repository := NewAuthRepository(store)
		hash := session.HashToken("complete")
		if _, err := repository.EnsureSetupToken(ctx, hash, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateAdministrator(ctx, hash, "admin_1", "admin", testPasswordHash, now); err != nil {
			t.Fatal(err)
		}
		if err := repository.ValidateSetupToken(ctx, hash, now); !errors.Is(err, ErrSetupComplete) {
			t.Fatalf("ValidateSetupToken = %v, want %v", err, ErrSetupComplete)
		}
	})
}

func TestAuthRepositorySetupPrecheckDoesNotBypassFinalRecheck(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "precheck-race.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	now := time.Unix(1700000000, 0).UTC()
	tokenHash := session.HashToken("race-token")
	if _, err := repository.EnsureSetupToken(ctx, tokenHash, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateSetupToken(ctx, tokenHash, now); err != nil {
		t.Fatalf("precheck = %v", err)
	}
	if _, err := repository.CreateAdministrator(ctx, tokenHash, "admin_competing", "competing", testPasswordHash, now); err != nil {
		t.Fatalf("competing setup = %v", err)
	}
	if _, err := repository.CreateAdministrator(ctx, tokenHash, "admin_original", "original", testPasswordHash, now); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("original final recheck = %v, want %v", err, ErrSetupComplete)
	}
	if got := rawInt(t, store.db, "SELECT COUNT(*) FROM administrators"); got != 1 {
		t.Fatalf("administrator count = %d, want 1", got)
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

func TestAuthRepositoryResetAdministratorPasswordRevokesSessionsAtomically(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "password-reset.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	setupHash := session.HashToken("setup")
	if created, err := repository.EnsureSetupToken(ctx, setupHash, now, now.Add(time.Hour)); err != nil || !created {
		t.Fatalf("EnsureSetupToken = %v, %v", created, err)
	}
	if _, err := repository.CreateAdministrator(ctx, setupHash, "admin_1", "admin", "old-hash", now); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"session-one", "session-two"} {
		if err := repository.CreateSession(ctx, SessionRecord{
			SessionIDHash: session.HashToken(raw), AdminID: "admin_1", CreatedAt: now,
			LastSeenAt: now, ExpiresAt: now.Add(time.Hour), CSRFSecret: "csrf-" + raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	resetAt := now.Add(10 * time.Minute)
	if err := repository.ResetAdministratorPassword(ctx, "new-hash", resetAt); err != nil {
		t.Fatal(err)
	}
	admin, err := repository.AdministratorByID(ctx, "admin_1")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Username != "admin" || admin.PasswordHash != "new-hash" || !admin.PasswordChangedAt.Equal(resetAt) || !admin.UpdatedAt.Equal(resetAt) {
		t.Fatalf("administrator after reset = %#v", admin)
	}
	for _, raw := range []string{"session-one", "session-two"} {
		if _, err := repository.LookupSession(ctx, session.HashToken(raw), resetAt); !errors.Is(err, ErrSessionRevoked) {
			t.Fatalf("session %q after reset = %v, want ErrSessionRevoked", raw, err)
		}
	}
	var completed int
	if err := store.db.QueryRowContext(ctx, "SELECT completed FROM setup_state WHERE singleton = 1").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("setup completed = %d, want 1", completed)
	}
	var consumedAt sql.NullInt64
	if err := store.db.QueryRowContext(ctx, "SELECT consumed_at FROM setup_tokens WHERE singleton = 1").Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if !consumedAt.Valid {
		t.Fatal("password reset reopened setup token")
	}
}

func TestAuthRepositoryResetAdministratorPasswordRejectsInvalidState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "password-reset-invalid.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewAuthRepository(store)
	if err := repository.ResetAdministratorPassword(ctx, "new-hash", time.Unix(1700000000, 0)); !errors.Is(err, ErrAuthInvariant) {
		t.Fatalf("reset before setup = %v, want ErrAuthInvariant", err)
	}
}
