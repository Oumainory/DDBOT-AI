// Package secretstore contains the explicit P1C Secret Store boundary. It
// owns AES-256-GCM envelope handling, Master Key lifecycle, and credential
// resolution; it is not a connector or HTTP API.
package secretstore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	SecretEnvelopeVersion = 1
	MaxSecretBytes        = 64 * 1024
	secretAADFormat       = byte(1)
)

var (
	ErrInvalidMasterKey  = errors.New("secretstore: invalid master key")
	ErrCiphertextInvalid = errors.New("secretstore: invalid ciphertext")
	ErrSecretIntegrity   = errors.New("secretstore: secret integrity check failed")
	ErrSecretInput       = errors.New("secretstore: secret input is invalid")
)

// Envelope is the durable encrypted payload. Plaintext and Master Key bytes
// are deliberately absent from this type.
type Envelope struct {
	Version    int
	Revision   int64
	Nonce      []byte
	Ciphertext []byte
}

// BuildAAD is the only credential AAD builder. Its canonical bytes are:
//
//	"DDBOT-AI\x00credential-secret\x00" || 0x01 ||
//	u32be(len(credentialID)) || credentialID ||
//	u32be(len(credentialType)) || credentialType ||
//	u32be(envelopeVersion) || u64be(secretRevision)
//
// Length prefixes make the context unambiguous while the domain prefix keeps
// credential ciphertext separate from every other Secret Store ciphertext.
func BuildAAD(credentialID, credentialType string, envelopeVersion int, secretRevision int64) []byte {
	result := make([]byte, 0, 64+len(credentialID)+len(credentialType))
	result = append(result, []byte("DDBOT-AI\x00credential-secret\x00")...)
	result = append(result, secretAADFormat)
	result = appendLengthPrefixed(result, []byte(credentialID))
	result = appendLengthPrefixed(result, []byte(credentialType))
	var version [4]byte
	binary.BigEndian.PutUint32(version[:], uint32(envelopeVersion))
	result = append(result, version[:]...)
	var revision [8]byte
	binary.BigEndian.PutUint64(revision[:], uint64(secretRevision))
	result = append(result, revision[:]...)
	return result
}

// BuildSentinelAAD deliberately uses a different domain from credential AAD.
func BuildSentinelAAD(envelopeVersion int) []byte {
	result := make([]byte, 0, 40)
	result = append(result, []byte("DDBOT-AI\x00secret-store-sentinel\x00")...)
	result = append(result, secretAADFormat)
	var version [4]byte
	binary.BigEndian.PutUint32(version[:], uint32(envelopeVersion))
	return append(result, version[:]...)
}

func appendLengthPrefixed(dst, value []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	dst = append(dst, length[:]...)
	return append(dst, value...)
}

func validateKey(key []byte) error {
	if len(key) != 32 {
		return ErrInvalidMasterKey
	}
	return nil
}

func validatePlaintext(plaintext []byte) error {
	if len(plaintext) == 0 || len(plaintext) > MaxSecretBytes {
		return ErrSecretInput
	}
	return nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidMasterKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidMasterKey
	}
	return aead, nil
}

// Seal encrypts plaintext with a caller-supplied AAD. It is the shared
// primitive used by credential envelopes and the sentinel.
func Seal(key, aad, plaintext []byte, random io.Reader) (nonce, ciphertext []byte, err error) {
	if err := validatePlaintext(plaintext); err != nil {
		return nil, nil, err
	}
	if len(aad) == 0 {
		return nil, nil, ErrSecretInput
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, nil, fmt.Errorf("secretstore: generate nonce: %w", err)
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, aad)
	return nonce, ciphertext, nil
}

// Open authenticates and decrypts an envelope. Authentication failures are
// intentionally collapsed into ErrSecretIntegrity so callers cannot expose
// low-level crypto details through an ordinary API.
func Open(key, aad, nonce, ciphertext []byte) ([]byte, error) {
	if len(aad) == 0 {
		return nil, ErrCiphertextInvalid
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() || len(ciphertext) < aead.Overhead() {
		return nil, ErrCiphertextInvalid
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrSecretIntegrity
	}
	if err := validatePlaintext(plaintext); err != nil {
		return nil, ErrSecretIntegrity
	}
	return plaintext, nil
}

func Encrypt(key []byte, credentialID, credentialType string, revision int64, plaintext []byte, random io.Reader) (Envelope, error) {
	if strings.TrimSpace(credentialID) == "" || strings.TrimSpace(credentialType) == "" || revision <= 0 {
		return Envelope{}, ErrSecretInput
	}
	aad := BuildAAD(credentialID, credentialType, SecretEnvelopeVersion, revision)
	nonce, ciphertext, err := Seal(key, aad, plaintext, random)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		Version:    SecretEnvelopeVersion,
		Revision:   revision,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

func Decrypt(key []byte, credentialID, credentialType string, envelope Envelope) ([]byte, error) {
	if strings.TrimSpace(credentialID) == "" || strings.TrimSpace(credentialType) == "" || envelope.Version != SecretEnvelopeVersion || envelope.Revision <= 0 {
		return nil, ErrCiphertextInvalid
	}
	aad := BuildAAD(credentialID, credentialType, envelope.Version, envelope.Revision)
	return Open(key, aad, envelope.Nonce, envelope.Ciphertext)
}

func encryptSentinel(key []byte, random io.Reader) (Envelope, error) {
	nonce, ciphertext, err := Seal(key, BuildSentinelAAD(SecretEnvelopeVersion), []byte("DDBOT-AI Secret Store v1"), random)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Version: SecretEnvelopeVersion, Nonce: nonce, Ciphertext: ciphertext}, nil
}

func decryptSentinel(key []byte, envelope Envelope) error {
	if envelope.Version != SecretEnvelopeVersion {
		return ErrCiphertextInvalid
	}
	plaintext, err := Open(key, BuildSentinelAAD(envelope.Version), envelope.Nonce, envelope.Ciphertext)
	if err != nil {
		return err
	}
	expected := []byte("DDBOT-AI Secret Store v1")
	if len(plaintext) != len(expected) || subtle.ConstantTimeCompare(plaintext, expected) != 1 {
		return ErrSecretIntegrity
	}
	return nil
}

func cloneBytes(value []byte) []byte {
	return bytes.Clone(value)
}
