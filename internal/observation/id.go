package observation

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

type idGenerator struct {
	random io.Reader
}

func (g idGenerator) next(prefix string) (string, error) {
	randomSource := g.random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	var raw [16]byte
	if _, err := io.ReadFull(randomSource, raw[:]); err != nil {
		return "", fmt.Errorf("observation: generate id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
