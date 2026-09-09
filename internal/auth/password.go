// Package auth contains the narrow administrator authentication domain. HTTP
// handlers are kept in internal/adminauth; this package does not know about
// cookies, routes, or response envelopes.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordRunes = 12
	MaxPasswordBytes = 1024
	ArgonVersion     = uint32(19)
	ArgonMemory      = uint32(64 * 1024)
	ArgonIterations  = uint32(3)
	ArgonParallelism = uint8(2)
	ArgonSaltBytes   = 16
	ArgonKeyBytes    = 32
)

var (
	ErrPasswordPolicy = errors.New("auth: password does not satisfy policy")
	ErrPasswordHash   = errors.New("auth: invalid argon2id hash")
)

// ValidatePassword applies a deliberately small policy. The password is
// never trimmed: whitespace is meaningful to the user, while an all-whitespace
// value is rejected as an obvious empty input.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || len(password) > MaxPasswordBytes || utf8.RuneCountInString(password) < MinPasswordRunes || strings.TrimSpace(password) == "" {
		return ErrPasswordPolicy
	}
	return nil
}

// HashPassword returns the standard encoded Argon2id form:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, ArgonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, ArgonIterations, ArgonMemory, ArgonParallelism, ArgonKeyBytes)
	encode := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		ArgonVersion,
		ArgonMemory,
		ArgonIterations,
		ArgonParallelism,
		encode.EncodeToString(salt),
		encode.EncodeToString(key),
	), nil
}

// VerifyPassword parses the parameters from the stored encoded hash. It does
// not silently replace malformed or unsafe parameters with local defaults.
func VerifyPassword(password, encoded string) (bool, error) {
	if err := ValidatePassword(password); err != nil {
		return false, err
	}
	version, memory, iterations, parallelism, salt, expected, err := parseEncodedHash(encoded)
	if err != nil {
		return false, err
	}
	if version != ArgonVersion || memory == 0 || iterations == 0 || parallelism == 0 || len(salt) == 0 || len(expected) == 0 {
		return false, ErrPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parseEncodedHash(encoded string) (uint32, uint32, uint32, uint8, []byte, []byte, error) {
	if len(encoded) > 4096 {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	version, err := parseUintField(parts[2], "v", 32)
	if err != nil {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	memory, memoryErr := parseUintField(parameters[0], "m", 32)
	iterations, iterationsErr := parseUintField(parameters[1], "t", 32)
	parallelismValue, parallelismErr := parseUintField(parameters[2], "p", 8)
	if memoryErr != nil || iterationsErr != nil || parallelismErr != nil {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	decode := base64.RawStdEncoding
	salt, err := decode.DecodeString(parts[4])
	if err != nil {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	expected, err := decode.DecodeString(parts[5])
	if err != nil {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	if len(salt) < 8 || len(salt) > 64 || len(expected) < 16 || len(expected) > 128 || memory > 1024*1024 || iterations > 10 || parallelismValue == 0 || parallelismValue > 64 {
		return 0, 0, 0, 0, nil, nil, ErrPasswordHash
	}
	return uint32(version), uint32(memory), uint32(iterations), uint8(parallelismValue), salt, expected, nil
}

func parseUintField(value, name string, bitSize int) (uint64, error) {
	prefix := name + "="
	if !strings.HasPrefix(value, prefix) || strings.Count(value, "=") != 1 {
		return 0, ErrPasswordHash
	}
	number := strings.TrimPrefix(value, prefix)
	if number == "" {
		return 0, ErrPasswordHash
	}
	parsed, err := strconv.ParseUint(number, 10, bitSize)
	if err != nil {
		return 0, ErrPasswordHash
	}
	return parsed, nil
}
