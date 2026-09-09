// Package platformdb owns the DDBOT-AI platform SQLite database.
//
// It is intentionally separate from Legacy WSa storage. The package has no
// startup goroutine or global database handle: callers explicitly call Open,
// retain the returned single-owner Store, and decide how a failure should be
// reported while Legacy continues to run.
package platformdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultBusyTimeout = 5 * time.Second

var (
	ErrInvalidPath             = errors.New("platformdb: database path is required")
	ErrDatabaseClosed          = errors.New("platformdb: database is closed")
	ErrFutureSchema            = errors.New("platformdb: database schema is newer than this binary")
	ErrUnknownMigration        = errors.New("platformdb: database contains an unknown migration")
	ErrMissingMigration        = errors.New("platformdb: database migration history is incomplete")
	ErrMigrationHistory        = errors.New("platformdb: database migration history is missing")
	ErrMigrationChecksum       = errors.New("platformdb: migration checksum mismatch")
	ErrPreMigrationBackup      = errors.New("platformdb: pre-migration backup failed")
	ErrBackupDestinationExists = errors.New("platformdb: backup destination already exists")
	ErrBackupSameDatabase      = errors.New("platformdb: backup destination is the source database")
)

// PreMigrationBackupConfig controls the destination of the automatic backup
// taken before an existing database receives pending migrations. An empty
// destination uses the deterministic default derived from the source path,
// schema versions and Config.Now.
type PreMigrationBackupConfig struct {
	Destination string
}

// Config controls one explicit SQLite owner. MaxOpenConns is intentionally not
// configurable: every connection-level PRAGMA must apply to the sole owner.
type Config struct {
	Path               string
	BusyTimeout        time.Duration
	Now                func() time.Time
	PreMigrationBackup PreMigrationBackupConfig
}

func (c Config) normalized() (Config, error) {
	c.Path = strings.TrimSpace(c.Path)
	if c.Path == "" {
		return Config{}, ErrInvalidPath
	}
	if c.BusyTimeout <= 0 {
		c.BusyTimeout = defaultBusyTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c, nil
}

// Store is the single writer/owner for the platform database. The underlying
// *sql.DB is kept private so future services cannot accidentally open a second
// independent writer.
type Store struct {
	db                     *sql.DB
	path                   string
	databaseExisted        bool
	busyTimeout            time.Duration
	now                    func() time.Time
	preMigrationBackup     PreMigrationBackupConfig
	lastPreMigrationBackup string
}

// Open creates the parent directory when needed, opens SQLite, configures the
// required connection PRAGMAs, and applies all embedded migrations. Any error
// is returned to the caller; it never panics or changes Legacy state.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if err := ensureParentDirectory(normalized.Path); err != nil {
		return nil, err
	}

	databaseExisted := false
	if !isMemoryPath(normalized.Path) && !strings.HasPrefix(normalized.Path, "file:") {
		if _, statErr := os.Stat(normalized.Path); statErr == nil {
			databaseExisted = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("platformdb: inspect database path: %w", statErr)
		}
	}

	db, err := sql.Open("sqlite", sqliteDSN(normalized.Path, normalized.BusyTimeout))
	if err != nil {
		return nil, fmt.Errorf("platformdb: open sqlite: %w", err)
	}
	// SQLite is intentionally single-owner in V1. This also guarantees that
	// PRAGMA foreign_keys and busy_timeout are not lost on another pooled
	// connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{
		db:                 db,
		path:               normalized.Path,
		databaseExisted:    databaseExisted,
		busyTimeout:        normalized.BusyTimeout,
		now:                normalized.Now,
		preMigrationBackup: normalized.PreMigrationBackup,
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = db.Close()
		}
	}()

	if err := store.configure(ctx); err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		return nil, err
	}
	// SQLite creates the file lazily. Tighten permissions where the host OS
	// supports them; Windows ignores Unix mode bits without failing the open.
	if !isMemoryPath(normalized.Path) && !strings.HasPrefix(normalized.Path, "file:") {
		_ = os.Chmod(normalized.Path, 0o600)
	}
	closeOnError = false
	return store, nil
}

func (s *Store) configure(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrDatabaseClosed
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("platformdb: ping sqlite: %w", err)
	}
	busyMilliseconds := s.busyTimeout.Milliseconds()
	if busyMilliseconds <= 0 {
		busyMilliseconds = defaultBusyTimeout.Milliseconds()
	}
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyMilliseconds),
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("platformdb: configure %s: %w", pragma, err)
		}
	}

	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("platformdb: verify foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("platformdb: verify foreign_keys: got %d", foreignKeys)
	}

	var busyTimeout int
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("platformdb: verify busy_timeout: %w", err)
	}
	if busyTimeout < int(busyMilliseconds) {
		return fmt.Errorf("platformdb: verify busy_timeout: got %dms, want at least %dms", busyTimeout, busyMilliseconds)
	}

	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("platformdb: verify journal_mode: %w", err)
	}
	if !isMemoryPath(s.path) && strings.ToLower(journalMode) != "wal" {
		return fmt.Errorf("platformdb: verify journal_mode: got %q, want %q", journalMode, "wal")
	}
	return nil
}

type appliedMigration struct {
	version  int
	name     string
	checksum string
}

type migrationInspection struct {
	definitions []migrationDefinition
	applied     map[int]appliedMigration
	current     int
	pending     []migrationDefinition
	brandNew    bool
}

// Migrate inspects migration history without mutating the database, takes a
// pre-migration backup when an existing database has pending work, and only
// then applies the pending SQL in one transaction. The separation is
// deliberate: backup failure must leave both the schema and history untouched.
func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrDatabaseClosed
	}
	inspection, err := s.inspectMigrations(ctx)
	if err != nil {
		return err
	}
	if len(inspection.pending) > 0 && !inspection.brandNew {
		destination, err := s.preMigrationBackupDestination(inspection)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPreMigrationBackup, err)
		}
		if err := s.Backup(ctx, destination); err != nil {
			return fmt.Errorf("%w: %v", ErrPreMigrationBackup, err)
		}
		s.lastPreMigrationBackup = destination
	}
	return s.applyMigrations(ctx, inspection)
}

func (s *Store) inspectMigrations(ctx context.Context) (migrationInspection, error) {
	definitions, err := migrationDefinitions()
	if err != nil {
		return migrationInspection{}, err
	}
	inspection := migrationInspection{
		definitions: definitions,
		applied:     make(map[int]appliedMigration),
	}

	var tableExists int
	if err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations')").Scan(&tableExists); err != nil {
		return migrationInspection{}, fmt.Errorf("platformdb: inspect schema_migrations: %w", err)
	}
	if tableExists == 0 {
		var objectCount int
		if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sqlite_master
WHERE name NOT LIKE 'sqlite_%'
  AND type IN ('table', 'index', 'view', 'trigger')`).Scan(&objectCount); err != nil {
			return migrationInspection{}, fmt.Errorf("platformdb: inspect database objects: %w", err)
		}
		if objectCount != 0 {
			return migrationInspection{}, ErrMigrationHistory
		}
		inspection.brandNew = true
		inspection.pending = append([]migrationDefinition(nil), definitions...)
		return inspection, nil
	}

	rows, err := s.db.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return migrationInspection{}, fmt.Errorf("platformdb: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var migration appliedMigration
		if err := rows.Scan(&migration.version, &migration.name, &migration.checksum); err != nil {
			_ = rows.Close()
			return migrationInspection{}, fmt.Errorf("platformdb: scan schema_migrations: %w", err)
		}
		if _, exists := inspection.applied[migration.version]; exists {
			_ = rows.Close()
			return migrationInspection{}, fmt.Errorf("%w: duplicate version %d", ErrMigrationHistory, migration.version)
		}
		inspection.applied[migration.version] = migration
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return migrationInspection{}, fmt.Errorf("platformdb: read schema_migrations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return migrationInspection{}, fmt.Errorf("platformdb: close schema_migrations: %w", err)
	}

	known := make(map[int]migrationDefinition, len(definitions))
	for _, definition := range definitions {
		known[definition.version] = definition
	}
	for _, existing := range inspection.applied {
		if existing.version > definitions[len(definitions)-1].version {
			return migrationInspection{}, fmt.Errorf("%w: version %d", ErrFutureSchema, existing.version)
		}
		definition, ok := known[existing.version]
		if !ok {
			return migrationInspection{}, fmt.Errorf("%w: version %d", ErrUnknownMigration, existing.version)
		}
		if existing.name != definition.name || existing.checksum != definition.checksum {
			return migrationInspection{}, fmt.Errorf("%w: version %d", ErrMigrationChecksum, existing.version)
		}
	}

	maxApplied := 0
	for version := range inspection.applied {
		if version > maxApplied {
			maxApplied = version
		}
	}
	for version := 1; version <= maxApplied; version++ {
		if _, ok := inspection.applied[version]; !ok {
			return migrationInspection{}, fmt.Errorf("%w: missing version %d", ErrMissingMigration, version)
		}
	}
	inspection.current = maxApplied
	for _, definition := range definitions {
		if _, ok := inspection.applied[definition.version]; !ok {
			inspection.pending = append(inspection.pending, definition)
		}
	}
	return inspection, nil
}

func (s *Store) preMigrationBackupDestination(inspection migrationInspection) (string, error) {
	if configured := strings.TrimSpace(s.preMigrationBackup.Destination); configured != "" {
		if sameDatabasePath(s.path, configured) {
			return "", ErrBackupSameDatabase
		}
		return configured, nil
	}
	if isMemoryPath(s.path) || strings.HasPrefix(s.path, "file:") {
		return "", ErrInvalidPath
	}
	latest := inspection.definitions[len(inspection.definitions)-1].version
	stamp := s.now().UTC().Format("20060102T150405.000000000Z")
	return fmt.Sprintf("%s.pre-migration-v%d-to-v%d-%s.sqlite", s.path, inspection.current, latest, stamp), nil
}

func (s *Store) applyMigrations(ctx context.Context, inspection migrationInspection) error {
	if len(inspection.pending) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformdb: begin migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("platformdb: create schema_migrations: %w", err)
	}

	for _, definition := range inspection.pending {
		if _, err := tx.ExecContext(ctx, definition.sql); err != nil {
			return fmt.Errorf("platformdb: apply migration %d (%s): %w", definition.version, definition.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			definition.version, definition.name, definition.checksum, s.now().UTC().Unix(),
		); err != nil {
			return fmt.Errorf("platformdb: record migration %d: %w", definition.version, err)
		}
	}

	if _, err := tx.ExecContext(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("platformdb: verify migration transaction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformdb: commit migrations: %w", err)
	}
	committed = true
	return nil
}

// LastPreMigrationBackupPath reports the backup path created by the most
// recent successful migration gate. It is intentionally read-only and empty
// when the store opened without pending migrations.
func (s *Store) LastPreMigrationBackupPath() string {
	if s == nil {
		return ""
	}
	return s.lastPreMigrationBackup
}

// SchemaVersion returns the highest successfully recorded migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrDatabaseClosed
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("platformdb: schema version: %w", err)
	}
	return version, nil
}

// Ping checks the owner connection without exposing the underlying *sql.DB.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrDatabaseClosed
	}
	return s.db.PingContext(ctx)
}

// Close releases the explicit owner. Calling it more than once follows the
// database/sql contract and is safe.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Backup creates a consistent SQLite snapshot using SQLite's own VACUUM INTO
// mechanism. The destination must not already exist, so an operator cannot
// accidentally overwrite a prior backup.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if s == nil || s.db == nil {
		return ErrDatabaseClosed
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return ErrInvalidPath
	}
	if sameDatabasePath(s.path, destination) {
		return ErrBackupSameDatabase
	}
	if _, err := os.Lstat(destination); err == nil {
		return ErrBackupDestinationExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("platformdb: inspect backup destination: %w", err)
	}
	if err := ensureParentDirectory(destination); err != nil {
		return err
	}
	quoted := "'" + strings.ReplaceAll(destination, "'", "''") + "'"
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("platformdb: backup: %w", err)
	}
	_ = os.Chmod(destination, 0o600)
	return nil
}

func ensureParentDirectory(path string) error {
	if isMemoryPath(path) || strings.HasPrefix(path, "file:") {
		return nil
	}
	parent := filepath.Dir(filepath.Clean(path))
	if parent == "." || parent == "" {
		return nil
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("platformdb: create database directory: %w", err)
	}
	return nil
}

func isMemoryPath(path string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(path))
	return trimmed == ":memory:" || strings.HasPrefix(trimmed, "file::memory:")
}

// sqliteDSN repeats the connection-level PRAGMAs in the driver DSN. The
// Store still executes and verifies them explicitly, but the DSN also covers
// the rare case where database/sql has to recreate the sole connection after
// a transient driver-level close.
func sqliteDSN(path string, busyTimeout time.Duration) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	busyMilliseconds := busyTimeout.Milliseconds()
	if busyMilliseconds <= 0 {
		busyMilliseconds = defaultBusyTimeout.Milliseconds()
	}
	pragmas := "_pragma=foreign_keys(1)&_pragma=busy_timeout(" + strconv.FormatInt(busyMilliseconds, 10) + ")"
	if !isMemoryPath(path) {
		pragmas += "&_pragma=journal_mode(WAL)"
	}
	return path + separator + pragmas
}

func sameDatabasePath(source, destination string) bool {
	if source == destination {
		return true
	}
	if isMemoryPath(source) || isMemoryPath(destination) ||
		strings.HasPrefix(source, "file:") || strings.HasPrefix(destination, "file:") {
		return false
	}
	sourceAbs, sourceErr := filepath.Abs(source)
	destinationAbs, destinationErr := filepath.Abs(destination)
	return sourceErr == nil && destinationErr == nil &&
		strings.EqualFold(filepath.Clean(sourceAbs), filepath.Clean(destinationAbs))
}
