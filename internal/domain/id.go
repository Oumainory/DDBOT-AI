// Package domain contains the small, platform-independent contracts shared by
// the Phase 3 domain and API layers.  It deliberately has no runtime or
// connector side effects.
package domain

import "github.com/google/uuid"

// NewID returns a RFC 9562 UUIDv7 application identifier.
func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// IsUUIDv7 reports whether value is a canonical RFC 9562 UUIDv7.
func IsUUIDv7(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.Version() == 7 && id.Variant() == uuid.RFC4122
}
