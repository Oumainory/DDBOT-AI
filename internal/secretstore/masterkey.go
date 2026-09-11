package secretstore

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	MasterKeyVersion     = 1
	MasterKeyPrefix      = "ddbot-ai-master-key-v1:"
	DefaultMasterKeyFile = "ddbot-ai.master.key"
	masterKeyFilePerm    = 0o600
	masterKeyParentPerm  = 0o700
)

var (
	ErrMasterKeyMissing     = errors.New("secretstore: master key is missing")
	ErrMasterKeyInvalid     = errors.New("secretstore: master key is invalid")
	ErrMasterKeyUnavailable = errors.New("secretstore: master key is unavailable")
)

// EncodeMasterKey uses a strict, versioned text representation. The key is
// never included in errors or ordinary status values.
func EncodeMasterKey(key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidMasterKey
	}
	return MasterKeyPrefix + base64.RawURLEncoding.EncodeToString(key), nil
}

func DecodeMasterKey(encoded string) ([]byte, error) {
	if encoded == "" || strings.ContainsAny(encoded, "\r\n\t ") || !strings.HasPrefix(encoded, MasterKeyPrefix) {
		return nil, ErrMasterKeyInvalid
	}
	value := strings.TrimPrefix(encoded, MasterKeyPrefix)
	if value == "" {
		return nil, ErrMasterKeyInvalid
	}
	key, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, ErrMasterKeyInvalid
	}
	return key, nil
}

type MasterKeyProvider interface {
	LoadOrCreate(existingSecretData bool) ([]byte, error)
}

// FileKeyProvider persists a random 256-bit key in a separate file. It never
// overwrites an existing file and treats an existing secret database without
// a key as Recovery rather than generating a replacement.
type FileKeyProvider struct {
	Path   string
	Random io.Reader
}

func (p FileKeyProvider) path() string {
	if strings.TrimSpace(p.Path) == "" {
		return DefaultMasterKeyFile
	}
	return strings.TrimSpace(p.Path)
}

func (p FileKeyProvider) LoadOrCreate(existingSecretData bool) ([]byte, error) {
	path := p.path()
	encoded, err := os.ReadFile(path)
	if err == nil {
		return DecodeMasterKey(string(encoded))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrMasterKeyUnavailable
	}
	if existingSecretData {
		return nil, ErrMasterKeyMissing
	}
	if err := ensureKeyParent(path); err != nil {
		return nil, fmt.Errorf("%w: create parent", ErrMasterKeyUnavailable)
	}
	random := p.Random
	if random == nil {
		random = rand.Reader
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, fmt.Errorf("%w: generate key", ErrMasterKeyUnavailable)
	}
	text, err := EncodeMasterKey(key)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, masterKeyFilePerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another initializer won the exclusive create race. Load the file
			// instead of generating or replacing a second key.
			encoded, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, ErrMasterKeyUnavailable
			}
			return DecodeMasterKey(string(encoded))
		}
		return nil, ErrMasterKeyUnavailable
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, text); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: write key", ErrMasterKeyUnavailable)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: sync key", ErrMasterKeyUnavailable)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("%w: close key", ErrMasterKeyUnavailable)
	}
	created = false
	_ = os.Chmod(path, masterKeyFilePerm)
	// Re-read and strictly parse the persisted representation before any
	// sentinel/ciphertext is written. A durable Secret Store must never depend
	// on an in-memory key that was not confirmed on disk.
	persisted, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reload key", ErrMasterKeyUnavailable)
	}
	return DecodeMasterKey(string(persisted))
}

func ensureKeyParent(path string) error {
	parent := filepath.Dir(filepath.Clean(path))
	if parent == "." || parent == "" {
		return nil
	}
	info, err := os.Stat(parent)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%w: key parent is not a directory", ErrMasterKeyUnavailable)
		}
		// Existing directories are owned by the operator. In particular, do
		// not chmod shared or bind-mounted parents such as /run/secrets.
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: inspect key parent", ErrMasterKeyUnavailable)
	}
	// MkdirAll applies masterKeyParentPerm to directories it creates, subject
	// to the host umask, while leaving any already-existing ancestor alone.
	// Do not chmod the resulting path: a concurrent initializer may have
	// created it, and its ownership/permissions then belong to the operator.
	if err := os.MkdirAll(parent, masterKeyParentPerm); err != nil {
		return fmt.Errorf("%w: create key parent", ErrMasterKeyUnavailable)
	}
	return nil
}
