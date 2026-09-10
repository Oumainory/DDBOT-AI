package platformdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretRepositoryPersistsMetadataStateAndEncryptedEnvelope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "platform.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewSecretRepository(store)
	stamp := time.Unix(1700000000, 0).UTC()

	if stored, err := repo.HasStoredSecretData(ctx); err != nil || stored {
		t.Fatalf("fresh secret data = %v, %v", stored, err)
	}
	if _, err := repo.SecretStoreState(ctx); !errors.Is(err, ErrSecretStoreStateMissing) {
		t.Fatalf("missing state error = %v", err)
	}
	if err := repo.CreateSecretStoreState(ctx, SecretStoreStateRecord{
		EnvelopeVersion: 1,
		SentinelNonce:   []byte{1, 2, 3},
		SentinelCipher:  []byte{4, 5, 6},
		CreatedAt:       stamp,
		UpdatedAt:       stamp,
	}); err != nil {
		t.Fatal(err)
	}
	if stored, err := repo.HasStoredSecretData(ctx); err != nil || !stored {
		t.Fatalf("stored secret data = %v, %v", stored, err)
	}
	if err := repo.CreateSecretStoreState(ctx, SecretStoreStateRecord{
		EnvelopeVersion: 1,
		SentinelNonce:   []byte{7},
		SentinelCipher:  []byte{8},
	}); !errors.Is(err, ErrSecretStoreStateExists) {
		t.Fatalf("duplicate state error = %v", err)
	}

	if err := repo.CreateCredential(ctx, CredentialMetadataRecord{
		ID: "credential-1", Type: "generic", Label: "test", Source: "manual",
		CreatedAt: stamp, UpdatedAt: stamp,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateCredential(ctx, CredentialMetadataRecord{ID: "credential-1", Type: "generic", Label: "duplicate", Source: "manual"}); !errors.Is(err, ErrCredentialExists) {
		t.Fatalf("duplicate credential error = %v", err)
	}
	if _, err := repo.CredentialSecret(ctx, "credential-1"); !errors.Is(err, ErrSecretNotConfigured) {
		t.Fatalf("missing credential secret error = %v", err)
	}

	first := CredentialSecretRecord{
		EnvelopeVersion: 1,
		SecretRevision:  1,
		Nonce:           []byte{9, 10},
		Ciphertext:      []byte{11, 12},
		CreatedAt:       stamp,
		UpdatedAt:       stamp,
	}
	if err := repo.PutCredentialSecret(ctx, "credential-1", first); err != nil {
		t.Fatal(err)
	}
	metadata, err := repo.Credential(ctx, "credential-1")
	if err != nil || !metadata.Configured {
		t.Fatalf("configured metadata = %#v, %v", metadata, err)
	}
	storedEnvelope, err := repo.CredentialSecret(ctx, "credential-1")
	if err != nil || storedEnvelope.SecretRevision != 1 {
		t.Fatalf("stored envelope = %#v, %v", storedEnvelope, err)
	}
	storedEnvelope.Nonce[0] = 99
	storedEnvelope.Ciphertext[0] = 98
	unchanged, err := repo.CredentialSecret(ctx, "credential-1")
	if err != nil || unchanged.Nonce[0] != 9 || unchanged.Ciphertext[0] != 11 {
		t.Fatalf("repository returned mutable secret buffers: %#v, %v", unchanged, err)
	}

	if err := repo.PutCredentialSecret(ctx, "credential-1", first); !errors.Is(err, ErrCredentialRevisionConflict) {
		t.Fatalf("non-increasing revision error = %v", err)
	}
	second := first
	second.SecretRevision = 2
	second.Nonce = []byte{13, 14}
	second.Ciphertext = []byte{15, 16}
	second.UpdatedAt = stamp.Add(time.Minute)
	if err := repo.PutCredentialSecret(ctx, "credential-1", second); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.CredentialSecret(ctx, "credential-1")
	if err != nil || updated.SecretRevision != 2 || updated.Nonce[0] != 13 {
		t.Fatalf("updated envelope = %#v, %v", updated, err)
	}
}

func TestSecretRepositoryDoesNotPartiallyWriteInvalidEnvelope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "platform.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewSecretRepository(store)
	if err := repo.CreateCredential(ctx, CredentialMetadataRecord{ID: "credential-1", Type: "generic", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutCredentialSecret(ctx, "credential-1", CredentialSecretRecord{EnvelopeVersion: 1, SecretRevision: 1, Nonce: []byte{1}, Ciphertext: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutCredentialSecret(ctx, "missing", CredentialSecretRecord{EnvelopeVersion: 1, SecretRevision: 1, Nonce: []byte{3}, Ciphertext: []byte{4}}); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("missing credential write error = %v", err)
	}
	current, err := repo.CredentialSecret(ctx, "credential-1")
	if err != nil || current.SecretRevision != 1 || current.Nonce[0] != 1 {
		t.Fatalf("existing envelope after failed write = %#v, %v", current, err)
	}
}
