package secretstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestMasterKeyEncodingRoundTripAndStrictParsing(t *testing.T) {
	key := bytes.Repeat([]byte{0x7a}, 32)
	encoded, err := EncodeMasterKey(key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMasterKey(encoded)
	if err != nil || !bytes.Equal(decoded, key) {
		t.Fatalf("decoded key = %x, err = %v", decoded, err)
	}
	for _, malformed := range []string{
		"",
		"ddbot-ai-master-key-v2:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		MasterKeyPrefix + "not-base64",
		MasterKeyPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		MasterKeyPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		encoded + "\n",
	} {
		if _, err := DecodeMasterKey(malformed); !errors.Is(err, ErrMasterKeyInvalid) {
			t.Fatalf("DecodeMasterKey(%q) = %v, want invalid", malformed, err)
		}
	}
	if _, err := EncodeMasterKey(make([]byte, 31)); !errors.Is(err, ErrInvalidMasterKey) {
		t.Fatalf("short EncodeMasterKey error = %v", err)
	}
}

func TestFileKeyProviderCreatesReloadsAndDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "master.key")
	firstKey, err := (FileKeyProvider{Path: path, Random: bytes.NewReader(bytes.Repeat([]byte{0x11}, 32))}).LoadOrCreate(false)
	if err != nil || len(firstKey) != 32 {
		t.Fatalf("first LoadOrCreate = %x, %v", firstKey, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte("test-secret")) {
		t.Fatal("unexpected test secret in key file")
	}
	secondKey, err := (FileKeyProvider{Path: path, Random: bytes.NewReader(bytes.Repeat([]byte{0x22}, 32))}).LoadOrCreate(false)
	if err != nil || !bytes.Equal(firstKey, secondKey) {
		t.Fatalf("reload key = %x, %v; want original", secondKey, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("master key mode = %o, want owner-only", info.Mode().Perm())
		}
	}
}

func TestFileKeyProviderMissingExistingSecretEntersMissingError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if _, err := (FileKeyProvider{Path: path, Random: bytes.NewReader(bytes.Repeat([]byte{0x11}, 32))}).LoadOrCreate(true); !errors.Is(err, ErrMasterKeyMissing) {
		t.Fatalf("missing existing key error = %v", err)
	}
}

func TestFileKeyProviderMalformedExistingFileIsNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, []byte("malformed-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileKeyProvider{Path: path, Random: bytes.NewReader(bytes.Repeat([]byte{0x11}, 32))}).LoadOrCreate(false); !errors.Is(err, ErrMasterKeyInvalid) {
		t.Fatalf("malformed key error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "malformed-key" {
		t.Fatalf("malformed key file was replaced: %q", contents)
	}
}

func TestFileKeyProviderExclusiveCreateRaceKeepsOneKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "master.key")
	providers := []FileKeyProvider{
		{Path: path, Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 32))},
		{Path: path, Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))},
	}
	keys := make([][]byte, len(providers))
	errs := make([]error, len(providers))
	var wait sync.WaitGroup
	for index := range providers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			keys[index], errs[index] = providers[index].LoadOrCreate(false)
		}(index)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("provider %d error = %v", index, err)
		}
	}
	if !bytes.Equal(keys[0], keys[1]) {
		t.Fatalf("exclusive create race returned different keys: %x / %x", keys[0], keys[1])
	}
}
