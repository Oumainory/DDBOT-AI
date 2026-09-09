// Package idempotency defines the request identity contract for replayable
// domain commands. It does not persist records; the HTTP/API layer will map
// this value to SQLite once that layer is introduced.
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
)

const (
	MinKeyLength = 16
	MaxKeyLength = 128
)

var (
	ErrInvalidKey    = errors.New("idempotency: invalid key")
	ErrInvalidPath   = errors.New("idempotency: invalid request path")
	ErrInvalidMethod = errors.New("idempotency: invalid request method")
)

type Fingerprint struct {
	Method         string `json:"method"`
	NormalizedPath string `json:"normalized_path"`
	CanonicalQuery string `json:"canonical_query"`
	BodySHA256     string `json:"body_sha256"`
}

func NewFingerprint(method, requestPath string, body []byte) (Fingerprint, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || strings.ContainsAny(method, " \t\r\n") {
		return Fingerprint{}, ErrInvalidMethod
	}
	normalizedPath, canonicalQuery, err := normalizeRequestPath(requestPath)
	if err != nil {
		return Fingerprint{}, err
	}

	canonicalBody, err := CanonicalBody(body)
	if err != nil {
		return Fingerprint{}, err
	}
	bodyHash := sha256.Sum256(canonicalBody)

	return Fingerprint{
		Method:         method,
		NormalizedPath: normalizedPath,
		CanonicalQuery: canonicalQuery,
		BodySHA256:     hex.EncodeToString(bodyHash[:]),
	}, nil
}

func NormalizeKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if len(key) < MinKeyLength || len(key) > MaxKeyLength {
		return "", ErrInvalidKey
	}
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return "", ErrInvalidKey
		}
	}
	return key, nil
}

// NormalizePath canonicalizes the concrete API path, not only the router
// template. Resource identifiers therefore remain part of the identity.
func NormalizePath(requestPath string) (string, error) {
	normalizedPath, _, err := normalizeRequestPath(requestPath)
	return normalizedPath, err
}

// CanonicalQuery returns the sorted, escaped query string that participates in
// a command fingerprint. It is persisted separately from NormalizedPath so a
// SQLite record can audit every request-semantic component without retaining
// the raw request body.
func CanonicalQuery(requestPath string) (string, error) {
	_, canonicalQuery, err := normalizeRequestPath(requestPath)
	return canonicalQuery, err
}

func normalizeRequestPath(requestPath string) (string, string, error) {
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "" || strings.ContainsAny(requestPath, "\r\n") {
		return "", "", ErrInvalidPath
	}

	u, err := url.ParseRequestURI(requestPath)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return "", "", ErrInvalidPath
	}

	cleanPath := path.Clean(u.Path)
	if cleanPath == "." {
		cleanPath = "/"
	}
	if len(cleanPath) > 1 {
		cleanPath = strings.TrimRight(cleanPath, "/")
	}

	values := u.Query()
	u.RawQuery = values.Encode()
	if u.RawQuery == "" {
		return cleanPath, "", nil
	}
	return cleanPath + "?" + u.RawQuery, u.RawQuery, nil
}

// CanonicalBody compacts JSON command bodies and lets encoding/json sort map
// keys. Non-JSON bodies are hashed byte-for-byte after trimming only leading
// and trailing whitespace, which keeps the contract deterministic without
// pretending arbitrary payloads are semantically equivalent.
func CanonicalBody(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []byte(""), nil
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return append([]byte(nil), trimmed...), nil
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("idempotency: request body contains multiple JSON values")
		}
		return nil, err
	}

	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func (f Fingerprint) Equal(other Fingerprint) bool {
	return f.Method == other.Method &&
		f.NormalizedPath == other.NormalizedPath &&
		f.effectiveCanonicalQuery() == other.effectiveCanonicalQuery() &&
		f.BodySHA256 == other.BodySHA256
}

func (f Fingerprint) effectiveCanonicalQuery() string {
	if f.CanonicalQuery != "" {
		return f.CanonicalQuery
	}
	if question := strings.IndexByte(f.NormalizedPath, '?'); question >= 0 {
		return f.NormalizedPath[question+1:]
	}
	return ""
}

type Comparison uint8

const (
	SameRequest Comparison = iota
	ConflictingRequest
)

func Compare(existing, incoming Fingerprint) Comparison {
	if existing.Equal(incoming) {
		return SameRequest
	}
	return ConflictingRequest
}

// SortedQueryKeys is kept small and explicit for callers that need to log a
// redacted explanation of a normalized request without exposing its body.
func SortedQueryKeys(requestPath string) ([]string, error) {
	u, err := url.ParseRequestURI(requestPath)
	if err != nil {
		return nil, ErrInvalidPath
	}
	keys := make([]string, 0, len(u.Query()))
	for key := range u.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}
