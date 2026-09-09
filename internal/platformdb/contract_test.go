package platformdb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigrationDefinitionsAreOrderedAndIndependentlyChecksummed(t *testing.T) {
	definitions, err := migrationDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("migration count = %d, want 2", len(definitions))
	}
	if definitions[0].version != 1 || definitions[0].filename != "001_core.sql" || definitions[0].name != "core" {
		t.Fatalf("v1 definition = %#v", definitions[0])
	}
	if definitions[1].version != 2 || definitions[1].filename != "002_contracts.sql" || definitions[1].name != "contracts" {
		t.Fatalf("v2 definition = %#v", definitions[1])
	}
	if definitions[0].checksum != "sha256:34ccff6f49b883e80fbbd76abfb54eaddd34d9b9355305a9b8c1e032a5b95751" {
		t.Fatalf("immutable v1 checksum changed: %s", definitions[0].checksum)
	}
	if definitions[1].checksum == definitions[0].checksum || !strings.HasPrefix(definitions[1].checksum, "sha256:") {
		t.Fatalf("v2 checksum = %q", definitions[1].checksum)
	}
	for index, definition := range definitions {
		if definition.version != index+1 {
			t.Fatalf("definition %d has version %d", index, definition.version)
		}
	}
}

func TestFreshAndLatestDatabaseDoNotCreatePreMigrationBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	freshPath := filepath.Join(dir, "fresh.sqlite")
	freshBackup := filepath.Join(dir, "fresh-backup.sqlite")
	fresh, err := Open(ctx, Config{Path: freshPath, PreMigrationBackup: PreMigrationBackupConfig{Destination: freshBackup}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.LastPreMigrationBackupPath(); got != "" {
		t.Fatalf("fresh backup path = %q, want empty", got)
	}
	_ = fresh.Close()
	if fileExists(freshBackup) {
		t.Fatal("fresh database created an unnecessary backup")
	}

	latestBackup := filepath.Join(dir, "latest-backup.sqlite")
	latest, err := Open(ctx, Config{Path: freshPath, PreMigrationBackup: PreMigrationBackupConfig{Destination: latestBackup}})
	if err != nil {
		t.Fatal(err)
	}
	defer latest.Close()
	if got := latest.LastPreMigrationBackupPath(); got != "" {
		t.Fatalf("latest backup path = %q, want empty", got)
	}
	if fileExists(latestBackup) {
		t.Fatal("latest database created an unnecessary backup")
	}
}

func TestV1ToV2TakesBackupBeforeSchemaMutation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	backupPath := filepath.Join(dir, "v1-before-v2.sqlite")
	createV1Database(t, databasePath)

	store, err := Open(ctx, Config{
		Path: databasePath,
		Now:  func() time.Time { return time.Unix(1700000000, 0).UTC() },
		PreMigrationBackup: PreMigrationBackupConfig{
			Destination: backupPath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.LastPreMigrationBackupPath(); got != backupPath {
		t.Fatalf("pre-migration backup path = %q, want %q", got, backupPath)
	}
	if version, err := store.SchemaVersion(ctx); err != nil || version != 2 {
		t.Fatalf("live schema version = %d, err = %v", version, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	backup := openRawDatabase(t, backupPath)
	defer backup.Close()
	if version := rawSchemaVersion(t, backup); version != 1 {
		t.Fatalf("backup schema version = %d, want v1", version)
	}
	for _, column := range []string{"canonical_query", "command_type", "execution_status", "completed_at"} {
		if rawHasColumn(t, backup, "idempotency_records", column) {
			t.Fatalf("v1 backup unexpectedly has idempotency column %q", column)
		}
	}
	if rawHasColumn(t, backup, "delivery_migration_holds", "route_decision_id") {
		t.Fatal("v1 backup unexpectedly has route_decision_id")
	}
	var body string
	if err := backup.QueryRow("SELECT response_body FROM idempotency_records WHERE idempotency_key = 'legacy-key'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "legacy-response" {
		t.Fatalf("backup legacy response = %q", body)
	}

	live := openRawDatabase(t, databasePath)
	defer live.Close()
	if version := rawSchemaVersion(t, live); version != 2 {
		t.Fatalf("live schema version = %d, want v2", version)
	}
	for _, column := range []string{"canonical_query", "command_type", "execution_status", "completed_at"} {
		if !rawHasColumn(t, live, "idempotency_records", column) {
			t.Fatalf("live database missing idempotency column %q", column)
		}
	}
	if !rawHasColumn(t, live, "delivery_migration_holds", "route_decision_id") {
		t.Fatal("live database missing route_decision_id")
	}
	if got := rawString(t, live, "SELECT response_body FROM idempotency_records WHERE idempotency_key = 'legacy-key'"); got != "legacy-response" {
		t.Fatalf("live legacy response = %q", got)
	}
	var routeDecisionID sql.NullString
	if err := live.QueryRow("SELECT route_decision_id FROM delivery_migration_holds WHERE delivery_id = 'legacy-delivery'").Scan(&routeDecisionID); err != nil {
		t.Fatal(err)
	}
	if routeDecisionID.Valid {
		t.Fatalf("legacy route decision id = %q, want NULL rather than fabricated identity", routeDecisionID.String)
	}
}

func TestPreMigrationBackupFailureLeavesV1Untouched(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	blockedDestination := filepath.Join(dir, "blocked")
	createV1Database(t, databasePath)
	if err := makeDirectory(blockedDestination); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: blockedDestination}})
	if store != nil || !errors.Is(err, ErrPreMigrationBackup) {
		t.Fatalf("Open with blocked backup = store %v, err %v", store, err)
	}

	db := openRawDatabase(t, databasePath)
	defer db.Close()
	if rawSchemaVersion(t, db) != 1 {
		t.Fatalf("live schema changed after backup failure")
	}
	for _, column := range []string{"canonical_query", "command_type", "execution_status", "completed_at"} {
		if rawHasColumn(t, db, "idempotency_records", column) {
			t.Fatalf("backup failure left v2 column %q", column)
		}
	}
	if rawHasColumn(t, db, "delivery_migration_holds", "route_decision_id") {
		t.Fatal("backup failure left route_decision_id")
	}
	if got := rawString(t, db, "SELECT response_body FROM idempotency_records WHERE idempotency_key = 'legacy-key'"); got != "legacy-response" {
		t.Fatalf("legacy data after backup failure = %q", got)
	}
	if count := rawInt(t, db, "SELECT COUNT(*) FROM schema_migrations WHERE version = 2"); count != 0 {
		t.Fatalf("v2 migration row count after backup failure = %d", count)
	}
}

func TestSecondStartupAfterV2DoesNotRepeatPreMigrationBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	backupPath := filepath.Join(dir, "before-v2.sqlite")
	createV1Database(t, databasePath)
	first, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath}})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath}})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.LastPreMigrationBackupPath() != "" {
		t.Fatalf("second startup repeated backup: %q", second.LastPreMigrationBackupPath())
	}
	if rawSize(t, backupPath) == 0 {
		t.Fatal("pre-migration backup was not retained")
	}
}

func TestDefaultPreMigrationBackupDestinationIsDeterministic(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	createV1Database(t, databasePath)
	now := time.Unix(1700000000, 123000000).UTC()
	store, err := Open(ctx, Config{Path: databasePath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := store.LastPreMigrationBackupPath()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	want := databasePath + ".pre-migration-v1-to-v2-20231114T221320.123000000Z.sqlite"
	if backupPath != want {
		t.Fatalf("default backup path = %q, want %q", backupPath, want)
	}
	if !fileExists(want) {
		t.Fatalf("default pre-migration backup %q was not created", want)
	}
}

func TestMigration002ChecksumMismatchIsRejected(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "latest.sqlite")
	store, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db := openRawDatabase(t, databasePath)
	mustExec(t, db, "UPDATE schema_migrations SET checksum = 'sha256:tampered-v2' WHERE version = 2")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, Config{Path: databasePath})
	if opened != nil || !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("Open(v2 checksum mismatch) = store %v, err %v", opened, err)
	}
}

func TestMigrationIntegrityAndHistoryRejectionsHappenBeforeBackup(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, []migrationDefinition)
		want   error
	}{
		{name: "v1 checksum", mutate: func(t *testing.T, db *sql.DB, _ []migrationDefinition) {
			mustExec(t, db, "UPDATE schema_migrations SET checksum = 'sha256:tampered' WHERE version = 1")
		}, want: ErrMigrationChecksum},
		{name: "future", mutate: func(t *testing.T, db *sql.DB, _ []migrationDefinition) {
			mustExec(t, db, "INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (99, 'future', 'sha256:future', 1)")
		}, want: ErrFutureSchema},
		{name: "unknown", mutate: func(t *testing.T, db *sql.DB, _ []migrationDefinition) {
			mustExec(t, db, "INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (0, 'unknown', 'sha256:unknown', 1)")
		}, want: ErrUnknownMigration},
		{name: "missing history", mutate: func(t *testing.T, db *sql.DB, definitions []migrationDefinition) {
			mustExec(t, db, "DELETE FROM schema_migrations WHERE version = 1")
			mustExec(t, db, "INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, 1)", definitions[1].version, definitions[1].name, definitions[1].checksum)
		}, want: ErrMissingMigration},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			databasePath := filepath.Join(dir, "database.sqlite")
			createV1Database(t, databasePath)
			db := openRawDatabase(t, databasePath)
			definitions, err := migrationDefinitions()
			if err != nil {
				t.Fatal(err)
			}
			// The mutation is intentionally performed before the new owner opens
			// the database, so inspection—not migration SQL—must reject it.
			test.mutate(t, db, definitions)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			backupPath := filepath.Join(dir, "should-not-exist.sqlite")
			store, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath}})
			if store != nil || !errors.Is(err, test.want) {
				t.Fatalf("Open() = store %v, err %v, want %v", store, err, test.want)
			}
			if fileExists(backupPath) {
				t.Fatal("invalid migration history was hidden by a backup")
			}
		})
	}
}

func TestMigration002RollsBackAsOneTransaction(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	backupPath := filepath.Join(dir, "before-failed-v2.sqlite")
	createV1Database(t, databasePath)
	db := openRawDatabase(t, databasePath)
	mustExec(t, db, "ALTER TABLE idempotency_records ADD COLUMN command_type TEXT")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath}})
	if store != nil || err == nil {
		t.Fatalf("Open(conflicting v2 migration) = store %v, err %v", store, err)
	}
	if !fileExists(backupPath) {
		t.Fatal("failed migration did not retain the pre-migration backup")
	}
	db = openRawDatabase(t, databasePath)
	defer db.Close()
	if rawSchemaVersion(t, db) != 1 {
		t.Fatal("failed migration changed schema history")
	}
	if rawHasColumn(t, db, "idempotency_records", "canonical_query") || rawHasColumn(t, db, "delivery_migration_holds", "route_decision_id") {
		t.Fatal("failed v2 migration left partial columns")
	}
}

func createV1Database(t *testing.T, path string) {
	t.Helper()
	db := openRawDatabase(t, path)
	defer db.Close()
	definitions, err := migrationDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(definitions[0].sql); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
checksum TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", definitions[0].version, definitions[0].name, definitions[0].checksum, 1700000000); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO idempotency_records
(principal, idempotency_key, method, normalized_path, body_sha256, status_code, response_headers_json, response_body, created_at, expires_at)
VALUES ('admin-1', 'legacy-key', 'POST', '/api/v2/legacy', 'legacy-hash', 200, '{}', 'legacy-response', 1700000000, 1700604800)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO delivery_migration_holds
(delivery_id, migration_id, event_id, route_snapshot_json, logical_target_json, message_snapshot_json, payload_schema_version, status, created_at)
VALUES ('legacy-delivery', 'legacy-migration', 'legacy-event', '{}', '{}', '{}', 1, 'migration_held', 1700000000)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func openRawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path, defaultBusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func rawSchemaVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	return rawInt(t, db, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations")
}

func rawHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}

func rawString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func rawInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func rawSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := fileInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func fileExists(path string) bool {
	_, err := fileInfo(path)
	return err == nil
}

func fileInfo(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func makeDirectory(path string) error {
	return os.Mkdir(path, 0o700)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
