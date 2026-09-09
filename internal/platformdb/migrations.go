package platformdb

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// migrationFiles is deliberately embedded in the binary. A deployment never
// depends on a writable working directory to discover its schema.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

type migrationDefinition struct {
	filename string
	version  int
	name     string
	sql      string
	checksum string
}

func migrationDefinitions() ([]migrationDefinition, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("platformdb: read migration directory: %w", err)
	}

	definitions := make([]migrationDefinition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		filename := entry.Name()
		base := strings.TrimSuffix(filename, ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 || len(parts[0]) != 3 || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("platformdb: invalid migration filename %q", filename)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("platformdb: invalid migration version in %q", filename)
		}
		name := parts[1]
		for _, r := range name {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return nil, fmt.Errorf("platformdb: invalid migration name in %q", filename)
			}
		}

		path := "migrations/" + filename
		schema, err := fs.ReadFile(migrationFiles, path)
		if err != nil {
			return nil, fmt.Errorf("platformdb: read migration %s: %w", path, err)
		}
		hash := sha256.Sum256(schema)
		definitions = append(definitions, migrationDefinition{
			filename: filename,
			version:  version,
			name:     name,
			sql:      string(schema),
			checksum: "sha256:" + hex.EncodeToString(hash[:]),
		})
	}

	if len(definitions) == 0 {
		return nil, errors.New("platformdb: no migrations embedded")
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].version < definitions[j].version
	})
	seen := make(map[int]struct{}, len(definitions))
	for index, definition := range definitions {
		if _, ok := seen[definition.version]; ok {
			return nil, fmt.Errorf("platformdb: duplicate migration version %d", definition.version)
		}
		seen[definition.version] = struct{}{}
		expectedVersion := index + 1
		if definition.version != expectedVersion {
			return nil, fmt.Errorf("platformdb: migration versions must be contiguous from 1, got %d at %s", definition.version, definition.filename)
		}
	}
	return definitions, nil
}
