package auth

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

func TestResetAdministratorPasswordUsesInjectedHasherAndInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "service-reset.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := platformdb.NewAuthRepository(store)
	service := NewService(repository, Config{
		Now:            func() time.Time { return now },
		Random:         bytes.NewReader(bytes.Repeat([]byte{0x51}, 256)),
		PasswordHasher: HashPassword,
	})
	bootstrap, err := service.Bootstrap(ctx)
	if err != nil || !bootstrap.Created {
		t.Fatalf("Bootstrap = %#v, %v", bootstrap, err)
	}
	if _, err := service.Setup(ctx, bootstrap.Token, "admin", setupTestPassword); err != nil {
		t.Fatal(err)
	}
	login, err := service.Login(ctx, "admin", setupTestPassword, "reset-test", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	service.passwordHasher = func(password string) (string, error) {
		calls.Add(1)
		if password != "new administrator password" {
			t.Fatalf("reset hasher received unexpected password %q", password)
		}
		return HashPassword(password)
	}
	if err := service.ResetAdministratorPassword(ctx, "new administrator password"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("reset hasher calls = %d, want 1", got)
	}
	if _, err := service.LookupSession(ctx, login.SessionToken); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old session lookup = %v, want ErrInvalidCredentials", err)
	}
	admin, err := repository.AdministratorByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := VerifyPassword("new administrator password", admin.PasswordHash)
	if err != nil || !valid {
		t.Fatalf("stored reset password verification = %v, valid=%v", err, valid)
	}
}

func TestResetAdministratorPasswordValidatesPolicyBeforeHasher(t *testing.T) {
	var calls atomic.Int32
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "invalid-reset.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(platformdb.NewAuthRepository(store), Config{PasswordHasher: func(string) (string, error) {
		calls.Add(1)
		return "unused", nil
	}})
	if err := service.ResetAdministratorPassword(context.Background(), "short"); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("invalid reset password = %v, want ErrPasswordPolicy", err)
	}
	if calls.Load() != 0 {
		t.Fatal("password hasher called for invalid reset password")
	}
}
