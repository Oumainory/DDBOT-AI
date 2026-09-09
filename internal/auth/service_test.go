package auth

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

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
