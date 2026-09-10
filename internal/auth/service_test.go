package auth

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	_ "modernc.org/sqlite"
)

const setupTestPassword = "correct horse battery staple"

func newSetupCostGuardFixture(t *testing.T, now time.Time, hasher PasswordHasher) (*platformdb.Store, *Service, string, string) {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "setup-cost.sqlite")
	store, err := platformdb.Open(ctx, platformdb.Config{Path: databasePath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	repository := platformdb.NewAuthRepository(store)
	rawToken := "setup-cost-token"
	if created, err := repository.EnsureSetupToken(ctx, hashSetupToken(rawToken), now, now.Add(time.Hour)); err != nil || !created {
		t.Fatalf("EnsureSetupToken = %v, %v", created, err)
	}
	service := NewService(repository, Config{
		Now:            func() time.Time { return now },
		Random:         bytes.NewReader(bytes.Repeat([]byte{0x42}, 256)),
		PasswordHasher: hasher,
	})
	return store, service, rawToken, databasePath
}

func TestSetupCostGuardSkipsPasswordHasherForRejectedRequests(t *testing.T) {
	cases := []struct {
		name string
		want error
	}{
		{name: "wrong token", want: platformdb.ErrSetupTokenInvalid},
		{name: "expired token", want: platformdb.ErrSetupTokenExpired},
		{name: "consumed token", want: platformdb.ErrSetupTokenConsumed},
		{name: "setup complete", want: platformdb.ErrSetupComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			hasher := func(string) (string, error) {
				calls.Add(1)
				return "fake-password-hash", nil
			}
			now := time.Unix(1700000000, 0).UTC()
			store, service, token, databasePath := newSetupCostGuardFixture(t, now, hasher)
			defer func() { _ = store.Close() }()
			if tc.name == "expired token" {
				service.now = func() time.Time { return now.Add(time.Hour) }
			}
			if tc.name == "consumed token" {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", databasePath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(context.Background(), "UPDATE setup_tokens SET consumed_at = ? WHERE singleton = 1", now.Unix()); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = platformdb.Open(context.Background(), platformdb.Config{Path: databasePath, Now: func() time.Time { return now }})
				if err != nil {
					t.Fatal(err)
				}
				service = NewService(platformdb.NewAuthRepository(store), Config{
					Now:            func() time.Time { return now },
					Random:         bytes.NewReader(bytes.Repeat([]byte{0x42}, 256)),
					PasswordHasher: hasher,
				})
			}
			if tc.name == "setup complete" {
				if _, err := platformdb.NewAuthRepository(store).CreateAdministrator(context.Background(), hashSetupToken(token), "admin_existing", "existing", "fake-password-hash", now); err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.Setup(context.Background(), token+map[bool]string{true: "-wrong", false: ""}[tc.name == "wrong token"], "admin", setupTestPassword)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Setup error = %v, want %v", err, tc.want)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("password hasher calls = %d, want 0", got)
			}
		})
	}
}

func TestSetupCostGuardCallsPasswordHasherOnceForValidToken(t *testing.T) {
	var calls atomic.Int32
	now := time.Unix(1700000000, 0).UTC()
	store, service, token, _ := newSetupCostGuardFixture(t, now, func(string) (string, error) {
		calls.Add(1)
		return "fake-password-hash", nil
	})
	defer store.Close()
	result, err := service.Setup(context.Background(), token, "admin", setupTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if result.Admin.Username != "admin" {
		t.Fatalf("setup result = %#v", result.Admin)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("password hasher calls = %d, want 1", got)
	}
}

func TestSetupCostGuardFinalTransactionRejectsTOCTOU(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	var competitor *Service
	var competitorErr error
	store, service, token, _ := newSetupCostGuardFixture(t, now, nil)
	defer store.Close()
	competitor = NewService(platformdb.NewAuthRepository(store), Config{
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x24}, 256)),
		PasswordHasher: func(string) (string, error) {
			return "fake-password-hash", nil
		},
	})
	service.passwordHasher = func(string) (string, error) {
		_, competitorErr = competitor.Setup(ctx, token, "competing", setupTestPassword)
		return "fake-password-hash", nil
	}
	_, err := service.Setup(ctx, token, "original", setupTestPassword)
	if competitorErr != nil {
		t.Fatalf("competing setup = %v", competitorErr)
	}
	if !errors.Is(err, platformdb.ErrSetupComplete) {
		t.Fatalf("original setup = %v, want %v", err, platformdb.ErrSetupComplete)
	}
	if state, stateErr := platformdb.NewAuthRepository(store).State(ctx); stateErr != nil || state != platformdb.AuthReady {
		t.Fatalf("state after competing setup = %q, %v", state, stateErr)
	}
}

func TestSetupConcurrentRequestsCreateOneAdministrator(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, _, token, _ := newSetupCostGuardFixture(t, now, nil)
	defer store.Close()
	var calls atomic.Int32
	release := make(chan struct{})
	secondHasher := make(chan struct{})
	hasher := func(string) (string, error) {
		if calls.Add(1) == 2 {
			close(secondHasher)
		}
		<-release
		return "fake-password-hash", nil
	}
	services := []*Service{
		NewService(platformdb.NewAuthRepository(store), Config{Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 256)), PasswordHasher: hasher}),
		NewService(platformdb.NewAuthRepository(store), Config{Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{0x32}, 256)), PasswordHasher: hasher}),
	}
	errs := make(chan error, len(services))
	var wait sync.WaitGroup
	for index, service := range services {
		wait.Add(1)
		go func(i int, service *Service) {
			defer wait.Done()
			_, err := service.Setup(ctx, token, "admin_"+string(rune('a'+i)), setupTestPassword)
			errs <- err
		}(index, service)
	}
	select {
	case <-secondHasher:
		close(release)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("concurrent setup requests did not both reach password hashing")
	}
	wait.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, platformdb.ErrSetupComplete) && !errors.Is(err, platformdb.ErrAdminExists) {
			t.Fatalf("concurrent setup error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent setup successes = %d, want 1", successes)
	}
	if state, stateErr := platformdb.NewAuthRepository(store).State(ctx); stateErr != nil || state != platformdb.AuthReady {
		t.Fatalf("state after concurrent setup = %q, %v", state, stateErr)
	}
}

func TestBootstrapTokenIsOneTimeAndDoesNotReopenCompletedSetup(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "service.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	clock := func() time.Time { return now }
	service := NewService(platformdb.NewAuthRepository(store), Config{
		Now:    clock,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 256)),
	})
	first, err := service.Bootstrap(ctx)
	if err != nil || !first.Created || first.Token == "" || !first.ExpiresAt.Equal(now.Add(DefaultSetupTokenTTL)) {
		t.Fatalf("first bootstrap = %#v, %v", first, err)
	}
	second, err := service.Bootstrap(ctx)
	if err != nil || second.Created || second.Token != "" {
		t.Fatalf("restart bootstrap = %#v, %v", second, err)
	}
	if _, err := service.Setup(ctx, first.Token, "Admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Bootstrap(ctx)
	if err != nil || completed.Created || completed.Token != "" {
		t.Fatalf("completed bootstrap = %#v, %v", completed, err)
	}
	if _, err := service.Setup(ctx, first.Token, "second", "correct horse battery staple"); !errors.Is(err, platformdb.ErrSetupComplete) {
		t.Fatalf("consumed setup replay = %v", err)
	}
}

func TestExpiredBootstrapTokenCanBeReplacedBeforeSetup(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "expired.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	service := NewService(platformdb.NewAuthRepository(store), Config{
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(append(bytes.Repeat([]byte{0x19}, 32), bytes.Repeat([]byte{0x2a}, 256)...)),
	})
	first, err := service.Bootstrap(ctx)
	if err != nil || !first.Created {
		t.Fatalf("first bootstrap = %#v, %v", first, err)
	}
	now = now.Add(DefaultSetupTokenTTL + time.Second)
	if _, err := service.Setup(ctx, first.Token, "admin", "correct horse battery staple"); !errors.Is(err, platformdb.ErrSetupTokenExpired) {
		t.Fatalf("expired setup error = %v", err)
	}
	second, err := service.Bootstrap(ctx)
	if err != nil || !second.Created || second.Token == first.Token {
		t.Fatalf("replacement bootstrap = %#v, %v", second, err)
	}
}
