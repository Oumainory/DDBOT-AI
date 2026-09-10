package secretstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	_ "modernc.org/sqlite"
)

type secretFixture struct {
	store   *platformdb.Store
	service *Service
	dbPath  string
	keyPath string
	now     time.Time
}

type incrementingReader struct {
	next byte
}

func (r *incrementingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}

func newSecretFixture(t *testing.T) secretFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dbPath := filepath.Join(t.TempDir(), "platform.sqlite")
	keyPath := filepath.Join(t.TempDir(), "keys", "master.key")
	store, err := platformdb.Open(ctx, platformdb.Config{Path: dbPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	service := New(ctx, platformdb.NewSecretRepository(store), Config{
		MasterKeyFile: keyPath,
		Random:        &incrementingReader{next: 0x11},
		Now:           func() time.Time { return now },
	})
	if service.State() != StateReady {
		_ = store.Close()
		t.Fatalf("Secret Store state = %s, init error = %v", service.State(), service.InitError())
	}
	if status, code := service.Check(ctx); status != platformdb.CheckOK || code != "secret_store_ready" {
		t.Fatalf("ready health check = %s/%s", status, code)
	}
	return secretFixture{store: store, service: service, dbPath: dbPath, keyPath: keyPath, now: now}
}

func reopenSecretFixture(t *testing.T, fixture secretFixture) secretFixture {
	t.Helper()
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: fixture.dbPath, Now: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatal(err)
	}
	service := New(context.Background(), platformdb.NewSecretRepository(store), Config{
		MasterKeyFile: fixture.keyPath,
		Random:        &incrementingReader{next: 0x22},
		Now:           func() time.Time { return fixture.now },
	})
	return secretFixture{store: store, service: service, dbPath: fixture.dbPath, keyPath: fixture.keyPath, now: fixture.now}
}

func TestSecretStoreLifecycleRevisionAndMaskedMetadata(t *testing.T) {
	fixture := newSecretFixture(t)
	defer fixture.store.Close()
	ctx := context.Background()
	if _, err := fixture.service.CreateCredential(ctx, CredentialInput{ID: "cred-a", Type: "openai_compatible", Label: "OpenAI", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	view, err := fixture.service.Metadata(ctx, "cred-a")
	if err != nil || view.Configured || view.Masked {
		t.Fatalf("metadata before secret = %#v, %v", view, err)
	}
	secret := []byte("test-secret-do-not-leak-123")
	if err := fixture.service.SetSecret(ctx, "cred-a", secret); err != nil {
		t.Fatal(err)
	}
	view, err = fixture.service.Metadata(ctx, "cred-a")
	if err != nil || !view.Configured || !view.Masked {
		t.Fatalf("metadata after secret = %#v, %v", view, err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), string(secret)) || strings.Contains(string(encoded), "ciphertext") || strings.Contains(string(encoded), "nonce") {
		t.Fatalf("masked metadata leaked secret material: %s", encoded)
	}
	resolved, err := fixture.service.ResolveSecret(ctx, "cred-a")
	if err != nil || !bytes.Equal(resolved, secret) {
		t.Fatalf("ResolveSecret = %q, %v", resolved, err)
	}
	repository := platformdb.NewSecretRepository(fixture.store)
	firstEnvelope, err := repository.CredentialSecret(ctx, "cred-a")
	if err != nil || firstEnvelope.SecretRevision != 1 || len(firstEnvelope.Nonce) != 12 {
		t.Fatalf("first envelope = %#v, %v", firstEnvelope, err)
	}
	if bytes.Contains(firstEnvelope.Ciphertext, secret) {
		t.Fatal("repository envelope contains plaintext secret")
	}
	if err := fixture.service.SetSecret(ctx, "cred-a", secret); err != nil {
		t.Fatal(err)
	}
	secondEnvelope, err := repository.CredentialSecret(ctx, "cred-a")
	if err != nil || secondEnvelope.SecretRevision != 2 {
		t.Fatalf("second envelope = %#v, %v", secondEnvelope, err)
	}
	if bytes.Equal(firstEnvelope.Nonce, secondEnvelope.Nonce) || bytes.Equal(firstEnvelope.Ciphertext, secondEnvelope.Ciphertext) {
		t.Fatal("secret update reused nonce or ciphertext")
	}
}

func TestSecretStoreRestartUsesSameKeyAndSentinel(t *testing.T) {
	fixture := newSecretFixture(t)
	if _, err := fixture.service.CreateCredential(context.Background(), CredentialInput{ID: "cred-restart", Type: "generic", Label: "Restart", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetSecret(context.Background(), "cred-restart", []byte("restart-secret")); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(fixture.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := reopenSecretFixture(t, fixture)
	defer restarted.store.Close()
	if restarted.service.State() != StateReady {
		t.Fatalf("restart state = %s, init error = %v", restarted.service.State(), restarted.service.InitError())
	}
	keyAfter, err := os.ReadFile(fixture.keyPath)
	if err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatalf("master key changed on restart: %q / %q, err=%v", keyBefore, keyAfter, err)
	}
	resolved, err := restarted.service.ResolveSecret(context.Background(), "cred-restart")
	if err != nil || string(resolved) != "restart-secret" {
		t.Fatalf("restart ResolveSecret = %q, %v", resolved, err)
	}
}

func TestFreshStoreWithExistingValidKeyCreatesSentinel(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dbPath := filepath.Join(t.TempDir(), "platform.sqlite")
	keyPath := filepath.Join(t.TempDir(), "master.key")
	store, err := platformdb.Open(ctx, platformdb.Config{Path: dbPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := FileKeyProvider{Path: keyPath, Random: bytes.NewReader(bytes.Repeat([]byte{0x71}, 32))}
	keyBefore, err := provider.LoadOrCreate(false)
	if err != nil {
		t.Fatal(err)
	}
	service := New(ctx, platformdb.NewSecretRepository(store), Config{MasterKeyFile: keyPath, Random: &incrementingReader{next: 0x81}, Now: func() time.Time { return now }})
	if service.State() != StateReady {
		t.Fatalf("state with existing key = %s, init error = %v", service.State(), service.InitError())
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMasterKey(string(keyAfter))
	if err != nil || !bytes.Equal(decoded, keyBefore) {
		t.Fatalf("existing key changed during sentinel initialization: %x / %x, err=%v", keyBefore, decoded, err)
	}
}

func TestExistingSecretWithMissingKeyEntersRecoveryWithoutReplacement(t *testing.T) {
	fixture := newSecretFixture(t)
	if _, err := fixture.service.CreateCredential(context.Background(), CredentialInput{ID: "cred-recovery", Type: "generic", Label: "Recovery", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetSecret(context.Background(), "cred-recovery", []byte("recovery-secret")); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(fixture.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.keyPath); err != nil {
		t.Fatal(err)
	}
	recovered := reopenSecretFixture(t, fixture)
	defer recovered.store.Close()
	if recovered.service.State() != StateRecovery {
		t.Fatalf("missing-key state = %s, init error = %v", recovered.service.State(), recovered.service.InitError())
	}
	if status, code := recovered.service.Check(context.Background()); status != platformdb.CheckDegraded || code != "secret_store_recovery" {
		t.Fatalf("recovery health check = %s/%s", status, code)
	}
	if _, err := recovered.service.ResolveSecret(context.Background(), "cred-recovery"); !errors.Is(err, ErrSecretStoreRecovery) {
		t.Fatalf("ResolveSecret in recovery = %v", err)
	}
	if err := recovered.service.SetSecret(context.Background(), "cred-recovery", []byte("new-secret")); !errors.Is(err, ErrSecretStoreRecovery) {
		t.Fatalf("SetSecret in recovery = %v", err)
	}
	if _, err := recovered.service.Metadata(context.Background(), "cred-recovery"); err != nil {
		t.Fatalf("metadata in recovery = %v", err)
	}
	if _, err := os.Stat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key was regenerated: stat err = %v", err)
	}
	if bytes.Equal(keyBefore, nil) {
		t.Fatal("fixture key unexpectedly empty")
	}
}

func TestCiphertextWithoutSentinelEntersRecovery(t *testing.T) {
	fixture := newSecretFixture(t)
	if _, err := fixture.service.CreateCredential(context.Background(), CredentialInput{ID: "cred-no-sentinel", Type: "generic", Label: "No sentinel", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetSecret(context.Background(), "cred-no-sentinel", []byte("ciphertext-without-sentinel")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM secret_store_state WHERE singleton = 1"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := reopenSecretFixture(t, fixture)
	defer recovered.store.Close()
	if recovered.service.State() != StateRecovery {
		t.Fatalf("ciphertext-without-sentinel state = %s, init error = %v", recovered.service.State(), recovered.service.InitError())
	}
	if _, err := recovered.service.ResolveSecret(context.Background(), "cred-no-sentinel"); !errors.Is(err, ErrSecretStoreRecovery) {
		t.Fatalf("ResolveSecret without sentinel = %v", err)
	}
}

func TestWrongMalformedKeyAndCorruptedSentinelEnterRecovery(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutateKey      bool
		malformed      bool
		mutateSentinel bool
	}{
		{name: "wrong key", mutateKey: true},
		{name: "malformed key", mutateKey: true, malformed: true},
		{name: "corrupted sentinel", mutateSentinel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSecretFixture(t)
			if _, err := fixture.service.CreateCredential(context.Background(), CredentialInput{ID: "cred-integrity", Type: "generic", Label: "Integrity", Source: "manual"}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.SetSecret(context.Background(), "cred-integrity", []byte("integrity-secret")); err != nil {
				t.Fatal(err)
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.mutateKey {
				value := []byte("malformed-key")
				if !tc.malformed {
					encoded, err := EncodeMasterKey(bytes.Repeat([]byte{0x91}, 32))
					if err != nil {
						t.Fatal(err)
					}
					value = []byte(encoded)
				}
				if err := os.WriteFile(fixture.keyPath, value, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if tc.mutateSentinel {
				db, err := sql.Open("sqlite", fixture.dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(context.Background(), "UPDATE secret_store_state SET sentinel_ciphertext = randomblob(length(sentinel_ciphertext)) WHERE singleton = 1"); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			recovered := reopenSecretFixture(t, fixture)
			defer recovered.store.Close()
			if recovered.service.State() != StateRecovery {
				t.Fatalf("state = %s, init error = %v", recovered.service.State(), recovered.service.InitError())
			}
		})
	}
}

func TestFailedSecretUpdatePreservesPreviousSecret(t *testing.T) {
	fixture := newSecretFixture(t)
	if _, err := fixture.service.CreateCredential(context.Background(), CredentialInput{ID: "cred-atomic", Type: "generic", Label: "Atomic", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetSecret(context.Background(), "cred-atomic", []byte("secret-a")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetSecret(context.Background(), "cred-atomic", []byte("secret-b")); err == nil {
		t.Fatal("secret update on closed database unexpectedly succeeded")
	}
	restarted := reopenSecretFixture(t, fixture)
	defer restarted.store.Close()
	resolved, err := restarted.service.ResolveSecret(context.Background(), "cred-atomic")
	if err != nil || string(resolved) != "secret-a" {
		t.Fatalf("previous secret after failed update = %q, %v", resolved, err)
	}
}
