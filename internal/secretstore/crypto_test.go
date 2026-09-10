package secretstore

import (
	"bytes"
	"errors"
	"testing"
)

func TestEncryptDecryptRoundTripAndRandomNonce(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	plaintext := []byte("test-secret-do-not-leak-123")
	first, err := Encrypt(key, "cred-a", "openai_compatible", 1, plaintext, bytes.NewReader(bytes.Repeat([]byte{0x21}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encrypt(key, "cred-a", "openai_compatible", 2, plaintext, bytes.NewReader(bytes.Repeat([]byte{0x22}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) || bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("repeated plaintext encryption reused nonce or ciphertext")
	}
	decrypted, err := Decrypt(key, "cred-a", "openai_compatible", first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}
	if string(decrypted) == "" {
		t.Fatal("decrypted secret unexpectedly empty")
	}
}

func TestAADBindsCredentialIdentityAndEnvelopeContext(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	envelope, err := Encrypt(key, "cred-a", "twitter", 7, []byte("secret-value"), bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		id       string
		typeName string
		version  int
		revision int64
	}{
		{name: "credential id", id: "cred-b", typeName: "twitter", version: envelope.Version, revision: envelope.Revision},
		{name: "credential type", id: "cred-a", typeName: "bilibili", version: envelope.Version, revision: envelope.Revision},
		{name: "version", id: "cred-a", typeName: "twitter", version: envelope.Version + 1, revision: envelope.Revision},
		{name: "revision", id: "cred-a", typeName: "twitter", version: envelope.Version, revision: envelope.Revision + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modified := envelope
			modified.Version = tc.version
			modified.Revision = tc.revision
			if _, err := Decrypt(key, tc.id, tc.typeName, modified); err == nil {
				t.Fatal("AAD/context mutation unexpectedly decrypted")
			}
		})
	}

	modifiedCipher := Envelope{Version: envelope.Version, Revision: envelope.Revision, Nonce: append([]byte(nil), envelope.Nonce...), Ciphertext: append([]byte(nil), envelope.Ciphertext...)}
	modifiedCipher.Ciphertext[0] ^= 0x80
	if _, err := Decrypt(key, "cred-a", "twitter", modifiedCipher); !errors.Is(err, ErrSecretIntegrity) {
		t.Fatalf("ciphertext tamper error = %v, want integrity", err)
	}
	modifiedNonce := envelope
	modifiedNonce.Nonce = append([]byte(nil), envelope.Nonce...)
	modifiedNonce.Nonce[0] ^= 0x80
	if _, err := Decrypt(key, "cred-a", "twitter", modifiedNonce); err == nil {
		t.Fatal("nonce tamper unexpectedly decrypted")
	}
	if _, err := Decrypt(bytes.Repeat([]byte{0x32}, 32), "cred-a", "twitter", envelope); err == nil {
		t.Fatal("wrong key unexpectedly decrypted")
	}
}

func TestSecretInputAndEnvelopeValidation(t *testing.T) {
	key := bytes.Repeat([]byte{0x51}, 32)
	random := bytes.NewReader(bytes.Repeat([]byte{0x61}, 64))
	if _, err := Encrypt(key, "cred", "generic", 1, nil, random); !errors.Is(err, ErrSecretInput) {
		t.Fatalf("empty secret error = %v", err)
	}
	if _, err := Encrypt(key, "cred", "generic", 1, bytes.Repeat([]byte{'x'}, MaxSecretBytes+1), random); !errors.Is(err, ErrSecretInput) {
		t.Fatalf("oversized secret error = %v", err)
	}
	if _, err := Encrypt(bytes.Repeat([]byte{0x71}, 31), "cred", "generic", 1, []byte("value"), random); !errors.Is(err, ErrInvalidMasterKey) {
		t.Fatalf("short key error = %v", err)
	}
	if _, err := Decrypt(key, "cred", "generic", Envelope{Version: SecretEnvelopeVersion, Revision: 1, Nonce: []byte{1}, Ciphertext: []byte{1}}); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("malformed envelope error = %v", err)
	}
}

func TestBuildAADIsCanonicalAndDomainSeparated(t *testing.T) {
	first := BuildAAD("cred-a", "generic", 1, 2)
	second := BuildAAD("cred-a", "generic", 1, 2)
	if !bytes.Equal(first, second) {
		t.Fatal("AAD is not deterministic")
	}
	if bytes.Equal(first, BuildAAD("cred-a", "generic", 1, 3)) {
		t.Fatal("revision is not bound into AAD")
	}
	if bytes.Equal(first, BuildSentinelAAD(1)) {
		t.Fatal("sentinel AAD shares credential domain")
	}
}
