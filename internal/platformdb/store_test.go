package platformdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenConfiguresSQLiteAndAppliesMigration(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "platform", "ddbot.sqlite")
	store, err := Open(ctx, Config{Path: databasePath, Now: func() time.Time { return time.Unix(1700000000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	var busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("busy_timeout = %d, want at least 5000", busyTimeout)
	}
	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}
	rows, err := store.db.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var rowVersion int
		if err := rows.Scan(&rowVersion); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, rowVersion)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 6 || versions[0] != 1 || versions[1] != 2 || versions[2] != 3 || versions[3] != 4 || versions[4] != 5 || versions[5] != 6 {
		t.Fatalf("migration order = %#v, want [1 2 3 4 5 6]", versions)
	}
	var tableName string
	if err := store.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'delivery_migration_holds'").Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName != "delivery_migration_holds" {
		t.Fatalf("table = %q", tableName)
	}
}

func TestMigrationsAreIdempotentAcrossRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "ddbot.sqlite")
	first, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	version, err := second.SchemaVersion(ctx)
	if err != nil || version != 6 {
		t.Fatalf("restart schema version = %d, err = %v", version, err)
	}
}

func TestOpenRejectsFutureSchemaWithoutDowngrading(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "ddbot.sqlite")
	first, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (999, 'future', 'sha256:future', 1700000000)"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, Config{Path: databasePath})
	if second != nil || !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("Open(future schema) = store %v, err %v", second, err)
	}
}

func TestBackupCreatesIndependentConsistentSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, Config{Path: filepath.Join(dir, "source.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE backup_probe (value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO backup_probe (value) VALUES ('persisted')`); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(dir, "backups", "snapshot.sqlite")
	if err := store.Backup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(ctx, destination); !errors.Is(err, ErrBackupDestinationExists) {
		t.Fatalf("second backup error = %v, want destination exists", err)
	}
	backup, err := Open(ctx, Config{Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var value string
	if err := backup.db.QueryRowContext(ctx, "SELECT value FROM backup_probe").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "persisted" {
		t.Fatalf("backup value = %q", value)
	}
}

func TestBackupRejectsSourceAsDestination(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "source.sqlite")
	store, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Backup(ctx, databasePath); !errors.Is(err, ErrBackupSameDatabase) {
		t.Fatalf("same database error = %v", err)
	}
}

func TestFailedOpenCanBeReportedWithoutTakingLegacyDown(t *testing.T) {
	ctx := context.Background()
	blockedPath := filepath.Join(t.TempDir(), "not-a-database")
	if err := ensureParentDirectory(blockedPath); err != nil {
		t.Fatal(err)
	}
	// A directory at the database path is an explicit, deterministic open
	// failure on every supported runner.
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Config{Path: blockedPath})
	if err == nil || store != nil {
		t.Fatalf("Open(directory) = store %v, err %v; want failure", store, err)
	}
	probe := NewProbe(nil, err)
	if report := probe.Health(ctx); report.Status != StatusDegraded || report.HTTPStatus() != 200 {
		t.Fatalf("health report = %#v, want degraded liveness", report)
	}
	if report := probe.Ready(ctx); report.Status != StatusNotReady || report.HTTPStatus() != 503 {
		t.Fatalf("ready report = %#v, want not_ready", report)
	}
}
