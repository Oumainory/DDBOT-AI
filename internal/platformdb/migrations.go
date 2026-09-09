package platformdb

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
)

// migrationFiles is deliberately embedded in the binary. A deployment never
// depends on a writable working directory to discover its schema.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

type migrationDefinition struct {
	version  int
	name     string
	sql      string
	checksum string
}

func migrationDefinitions() ([]migrationDefinition, error) {
	const (
		version = 1
		name    = "core"
		file    = "migrations/001_core.sql"
	)

	schema, err := fs.ReadFile(migrationFiles, file)
	if err != nil {
		return nil, fmt.Errorf("platformdb: read migration %s: %w", file, err)
	}
	hash := sha256.Sum256(schema)
	return []migrationDefinition{{
		version:  version,
		name:     name,
		sql:      string(schema),
		checksum: "sha256:" + hex.EncodeToString(hash[:]),
	}}, nil
}
