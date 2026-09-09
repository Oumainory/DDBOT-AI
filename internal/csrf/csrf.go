// Package csrf contains the independent server-side CSRF token contract.
package csrf

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

var ErrInvalidToken = errors.New("csrf: invalid token")

func GenerateToken(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	data := make([]byte, 32)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func Validate(expected, supplied string) bool {
	expected = strings.TrimSpace(expected)
	supplied = strings.TrimSpace(supplied)
	if expected == "" || supplied == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}
