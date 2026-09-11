package platformdb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigrationDefinitionsAreOrderedAndIndependentlyChecksummed(t *testing.T) {
	definitions, err := migrationDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 6 {
		t.Fatalf("migration count = %d, want 6", len(definitions))
	}
	if definitions[0].version != 1 || definitions[0].filename != "001_core.sql" || definitions[0].name != "core" {
		t.Fatalf("v1 definition = %#v", definitions[0])
	}
	if definitions[1].version != 2 || definitions[1].filename != "002_contracts.sql" || definitions[1].name != "contracts" {
		t.Fatalf("v2 definition = %#v", definitions[1])
	}
	if definitions[2].version != 3 || definitions[2].filename != "003_contract_guards.sql" || definitions[2].name != "contract_guards" {
		t.Fatalf("v3 definition = %#v", definitions[2])
	}
	if definitions[0].checksum != "sha256:34ccff6f49b883e80fbbd76abfb54eaddd34d9b9355305a9b8c1e032a5b95751" {
		t.Fatalf("immutable v1 checksum changed: %s", definitions[0].checksum)
	}
	if definitions[1].checksum != "sha256:9cb02c938480c5ea5b15581b3ecdcf76dd2795491af9e97871c3b03af0cc1184" {
		t.Fatalf("immutable v2 checksum changed: %s", definitions[1].checksum)
	}
	if definitions[2].checksum != "sha256:381fb13152366c40b2e4d1c69bc28ea8a62b4aeeee02438f2ea509f19bc4a59d" {
		t.Fatalf("immutable v3 checksum changed: %s", definitions[2].checksum)
	}
	if definitions[3].version != 4 || definitions[3].filename != "004_admin_auth.sql" || definitions[3].name != "admin_auth" {
		t.Fatalf("v4 definition = %#v", definitions[3])
	}
	if definitions[3].checksum != "sha256:48da493fdf4c57c08bece88fb90f0c2af7eeca597a899f2b66a19fcc569a75ac" {
		t.Fatalf("immutable v4 checksum changed: %s", definitions[3].checksum)
	}
	if definitions[4].version != 5 || definitions[4].filename != "005_secret_store.sql" || definitions[4].name != "secret_store" {
		t.Fatalf("v5 definition = %#v", definitions[4])
	}
	if definitions[4].checksum != "sha256:31464165512e3ac8e33668944d45ca52bae39c46ed1c6efec8ca64f288f2a133" {
		t.Fatalf("v5 checksum changed: %s", definitions[4].checksum)
	}
	if definitions[5].version != 6 || definitions[5].filename != "006_observation.sql" || definitions[5].name != "observation" {
		t.Fatalf("v6 definition = %#v", definitions[5])
	}
	if definitions[5].checksum != "sha256:61d1fac2b075a3a9f0abbde25f7d461947b48287a9f3e3d9dac2f69166b62518" {
		t.Fatalf("v6 checksum changed: %s", definitions[5].checksum)
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

func TestV1ToV2HistoryCanBePreparedBeforeV3(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "v2.sqlite")
	createV2Database(t, databasePath)
	db := openRawDatabase(t, databasePath)
	defer db.Close()
	if version := rawSchemaVersion(t, db); version != 2 {
		t.Fatalf("prepared schema version = %d, want v2", version)
	}
	for _, column := range []string{"canonical_query", "command_type", "execution_status", "completed_at"} {
		if !rawHasColumn(t, db, "idempotency_records", column) {
			t.Fatalf("prepared v2 database missing idempotency column %q", column)
		}
	}
	if !rawHasColumn(t, db, "delivery_migration_holds", "route_decision_id") {
		t.Fatal("prepared v2 database missing route_decision_id")
	}
	var triggerCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'trg_delivery_migration_holds_route_decision_%'").Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 0 {
		t.Fatalf("prepared v2 database has %d v3 trigger(s)", triggerCount)
	}
}

func TestV1ToV6TakesBackupBeforeSchemaMutation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	backupPath := filepath.Join(dir, "v1-before-v5.sqlite")
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
	if version, err := store.SchemaVersion(ctx); err != nil || version != 6 {
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
	if version := rawSchemaVersion(t, live); version != 6 {
		t.Fatalf("live schema version = %d, want v6", version)
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
	if _, err := live.Exec("UPDATE delivery_migration_holds SET event_id = 'legacy-event-updated' WHERE delivery_id = 'legacy-delivery'"); err != nil {
		t.Fatalf("updating unrelated legacy hold field = %v", err)
	}
}

func TestV2ToV6TakesV2BackupAndPreservesLegacyHold(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v2.sqlite")
	backupPath := filepath.Join(dir, "v2-before-v5.sqlite")
	createV2Database(t, databasePath)

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
	if version, err := store.SchemaVersion(ctx); err != nil || version != 6 {
		t.Fatalf("live schema version = %d, err = %v", version, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	backup := openRawDatabase(t, backupPath)
	if version := rawSchemaVersion(t, backup); version != 2 {
		t.Fatalf("backup schema version = %d, want v2", version)
	}
	if !rawHasColumn(t, backup, "delivery_migration_holds", "route_decision_id") {
		t.Fatal("v2 backup missing route_decision_id")
	}
	var backupRoute sql.NullString
	if err := backup.QueryRow("SELECT route_decision_id FROM delivery_migration_holds WHERE delivery_id = 'legacy-delivery'").Scan(&backupRoute); err != nil {
		t.Fatal(err)
	}
	if backupRoute.Valid {
		t.Fatalf("v2 backup legacy route decision id = %q, want NULL", backupRoute.String)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}

	live := openRawDatabase(t, databasePath)
	defer live.Close()
	if version := rawSchemaVersion(t, live); version != 6 {
		t.Fatalf("live schema version = %d, want v6", version)
	}
	var liveRoute sql.NullString
	if err := live.QueryRow("SELECT route_decision_id FROM delivery_migration_holds WHERE delivery_id = 'legacy-delivery'").Scan(&liveRoute); err != nil {
		t.Fatal(err)
	}
	if liveRoute.Valid {
		t.Fatalf("live legacy route decision id = %q, want NULL", liveRoute.String)
	}
}

func TestV3ToV6TakesV3BackupBeforeAuthSchemaMutation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v3.sqlite")
	backupPath := filepath.Join(dir, "v3-before-v5.sqlite")
	createV3Database(t, databasePath)

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
	if version, err := store.SchemaVersion(ctx); err != nil || version != 6 {
		t.Fatalf("live schema version = %d, err = %v", version, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	backup := openRawDatabase(t, backupPath)
	if version := rawSchemaVersion(t, backup); version != 3 {
		t.Fatalf("backup schema version = %d, want v3", version)
	}
	if rawTableExists(t, backup, "administrators") || rawTableExists(t, backup, "sessions") || rawTableExists(t, backup, "setup_state") || rawTableExists(t, backup, "setup_tokens") {
		t.Fatal("v3 backup unexpectedly contains v4 authentication tables")
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}

	live := openRawDatabase(t, databasePath)
	defer live.Close()
	for _, table := range []string{"administrators", "sessions", "setup_state", "setup_tokens"} {
		if !rawTableExists(t, live, table) {
			t.Fatalf("live upgraded database missing table %q", table)
		}
	}
}

func TestV4ToV6TakesBackupBeforeSecretSchemaMutation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v4.sqlite")
	backupPath := filepath.Join(dir, "v4-before-v5.sqlite")
	createV4Database(t, databasePath)

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
	if version, err := store.SchemaVersion(ctx); err != nil || version != 6 {
		t.Fatalf("live schema version = %d, err = %v", version, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	backup := openRawDatabase(t, backupPath)
	if version := rawSchemaVersion(t, backup); version != 4 {
		t.Fatalf("backup schema version = %d, want v4", version)
	}
	if rawTableExists(t, backup, "secret_store_state") || rawTableExists(t, backup, "credentials") || rawTableExists(t, backup, "credential_secrets") {
		t.Fatal("v4 backup unexpectedly contains secret-store tables")
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}

	live := openRawDatabase(t, databasePath)
	defer live.Close()
	if version := rawSchemaVersion(t, live); version != 6 {
		t.Fatalf("live schema version = %d, want v6", version)
	}
	for _, table := range []string{"secret_store_state", "credentials", "credential_secrets"} {
		if !rawTableExists(t, live, table) {
			t.Fatalf("live database missing secret-store table %q", table)
		}
	}
}

func TestMigration005FailureRollsBackSecretSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v4-conflict.sqlite")
	backupPath := filepath.Join(dir, "v4-conflict-backup.sqlite")
	createV4Database(t, databasePath)
	db := openRawDatabase(t, databasePath)
	if _, err := db.Exec("CREATE TABLE credentials (id TEXT PRIMARY KEY)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Config{
		Path: databasePath,
		PreMigrationBackup: PreMigrationBackupConfig{
			Destination: backupPath,
		},
	})
	if store != nil || err == nil {
		t.Fatalf("conflicting v5 migration = store %v, err %v", store, err)
	}
	verify := openRawDatabase(t, databasePath)
	defer verify.Close()
	if rawSchemaVersion(t, verify) != 4 {
		t.Fatalf("schema version after v5 rollback = %d, want v4", rawSchemaVersion(t, verify))
	}
	if rawTableExists(t, verify, "secret_store_state") || rawTableExists(t, verify, "credential_secrets") {
		t.Fatal("failed v5 migration left partial secret-store tables")
	}
	if count := rawInt(t, verify, "SELECT COUNT(*) FROM schema_migrations WHERE version = 5"); count != 0 {
		t.Fatalf("v5 migration history row after rollback = %d", count)
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

func TestPreMigrationBackupDestinationCollisionBlocksV6Migration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v3.sqlite")
	backupPath := filepath.Join(dir, "existing-backup.sqlite")
	createV3Database(t, databasePath)
	if err := os.WriteFile(backupPath, []byte("operator backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath}})
	if store != nil || !errors.Is(err, ErrPreMigrationBackup) {
		t.Fatalf("collision Open = store %v, err %v", store, err)
	}
	db := openRawDatabase(t, databasePath)
	defer db.Close()
	if rawSchemaVersion(t, db) != 3 {
		t.Fatal("backup destination collision mutated live schema")
	}
	if rawTableExists(t, db, "administrators") {
		t.Fatal("backup destination collision left authentication table")
	}
}

func TestSecondStartupAfterV6DoesNotRepeatPreMigrationBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v1.sqlite")
	backupPath := filepath.Join(dir, "before-v5.sqlite")
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

func TestV5ToV6TakesBackupBeforeObservationSchemaMutation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v5.sqlite")
	backupPath := filepath.Join(dir, "v5-before-v6.sqlite")
	createV5Database(t, databasePath)

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
	if version, err := store.SchemaVersion(ctx); err != nil || version != 6 {
		t.Fatalf("live schema version = %d, err = %v", version, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	backup := openRawDatabase(t, backupPath)
	if version := rawSchemaVersion(t, backup); version != 5 {
		t.Fatalf("backup schema version = %d, want v5", version)
	}
	for _, table := range []string{"observed_events", "route_observations", "delivery_observations"} {
		if rawTableExists(t, backup, table) {
			t.Fatalf("v5 backup unexpectedly contains observation table %q", table)
		}
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}

	live := openRawDatabase(t, databasePath)
	defer live.Close()
	if version := rawSchemaVersion(t, live); version != 6 {
		t.Fatalf("live schema version = %d, want v6", version)
	}
	for _, table := range []string{"observed_events", "route_observations", "delivery_observations"} {
		if !rawTableExists(t, live, table) {
			t.Fatalf("live database missing observation table %q", table)
		}
	}
	if got := rawString(t, live, "SELECT response_body FROM idempotency_records WHERE idempotency_key = 'legacy-key'"); got != "legacy-response" {
		t.Fatalf("legacy response after v6 upgrade = %q", got)
	}
	if count := rawInt(t, live, "SELECT COUNT(*) FROM delivery_migration_holds WHERE delivery_id = 'legacy-delivery'"); count != 1 {
		t.Fatalf("legacy migration hold count = %d, want 1", count)
	}

	second, err := Open(ctx, Config{
		Path:               databasePath,
		PreMigrationBackup: PreMigrationBackupConfig{Destination: filepath.Join(dir, "second.sqlite")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if got := second.LastPreMigrationBackupPath(); got != "" {
		t.Fatalf("latest v6 startup repeated backup: %q", got)
	}
}

func TestV5ToV6BackupFailureLeavesLiveSchemaUntouched(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v5.sqlite")
	blockedDestination := filepath.Join(dir, "blocked")
	createV5Database(t, databasePath)
	if err := makeDirectory(blockedDestination); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Config{
		Path:               databasePath,
		PreMigrationBackup: PreMigrationBackupConfig{Destination: blockedDestination},
	})
	if store != nil || !errors.Is(err, ErrPreMigrationBackup) {
		t.Fatalf("Open with blocked v6 backup = store %v, err %v", store, err)
	}

	db := openRawDatabase(t, databasePath)
	defer db.Close()
	if version := rawSchemaVersion(t, db); version != 5 {
		t.Fatalf("live schema changed after v6 backup failure: version %d", version)
	}
	for _, table := range []string{"observed_events", "route_observations", "delivery_observations"} {
		if rawTableExists(t, db, table) {
			t.Fatalf("backup failure left observation table %q", table)
		}
	}
	if count := rawInt(t, db, "SELECT COUNT(*) FROM schema_migrations WHERE version = 6"); count != 0 {
		t.Fatalf("v6 migration history row after backup failure = %d", count)
	}
	if got := rawString(t, db, "SELECT response_body FROM idempotency_records WHERE idempotency_key = 'legacy-key'"); got != "legacy-response" {
		t.Fatalf("legacy response after backup failure = %q", got)
	}
}

func TestMigration006RollsBackObservationSchemaAsOneTransaction(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v5-conflict.sqlite")
	backupPath := filepath.Join(dir, "v5-conflict-backup.sqlite")
	createV5Database(t, databasePath)
	db := openRawDatabase(t, databasePath)
	if _, err := db.Exec("CREATE TABLE observed_events (id TEXT PRIMARY KEY)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Config{
		Path:               databasePath,
		PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath},
	})
	if store != nil || err == nil {
		t.Fatalf("Open(conflicting v6 migration) = store %v, err %v", store, err)
	}
	if !fileExists(backupPath) {
		t.Fatal("failed v6 migration did not retain pre-migration backup")
	}
	verify := openRawDatabase(t, databasePath)
	defer verify.Close()
	if version := rawSchemaVersion(t, verify); version != 5 {
		t.Fatalf("schema version after v6 rollback = %d, want v5", version)
	}
	if rawTableExists(t, verify, "route_observations") || rawTableExists(t, verify, "delivery_observations") {
		t.Fatal("failed v6 migration left partial observation tables")
	}
	if count := rawInt(t, verify, "SELECT COUNT(*) FROM schema_migrations WHERE version = 6"); count != 0 {
		t.Fatalf("v6 migration history row after rollback = %d", count)
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
	want := databasePath + ".pre-migration-v1-to-v6-20231114T221320.123000000Z.sqlite"
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

func TestMigration003ChecksumMismatchIsRejected(t *testing.T) {
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
	mustExec(t, db, "UPDATE schema_migrations SET checksum = 'sha256:tampered-v3' WHERE version = 3")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, Config{Path: databasePath})
	if opened != nil || !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("Open(v3 checksum mismatch) = store %v, err %v", opened, err)
	}
}

func TestMigration004ChecksumMismatchIsRejected(t *testing.T) {
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
	mustExec(t, db, "UPDATE schema_migrations SET checksum = 'sha256:tampered-v4' WHERE version = 4")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, Config{Path: databasePath})
	if opened != nil || !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("Open(v4 checksum mismatch) = store %v, err %v", opened, err)
	}
}

func TestMigration005ChecksumMismatchIsRejected(t *testing.T) {
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
	mustExec(t, db, "UPDATE schema_migrations SET checksum = 'sha256:tampered-v5' WHERE version = 5")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, Config{Path: databasePath})
	if opened != nil || !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("Open(v5 checksum mismatch) = store %v, err %v", opened, err)
	}
}

func TestMigration006ChecksumMismatchIsRejected(t *testing.T) {
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
	mustExec(t, db, "UPDATE schema_migrations SET checksum = 'sha256:tampered-v6' WHERE version = 6")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, Config{Path: databasePath})
	if opened != nil || !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("Open(v6 checksum mismatch) = store %v, err %v", opened, err)
	}
}

func TestDeliveryMigrationHoldRouteDecisionGuards(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "latest.sqlite")
	store, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	insert := func(deliveryID string, routeDecisionID any) error {
		_, err := store.db.ExecContext(ctx, `INSERT INTO delivery_migration_holds
(delivery_id, migration_id, event_id, route_snapshot_json, logical_target_json, message_snapshot_json, payload_schema_version, route_decision_id, status, created_at)
VALUES (?, 'migration-1', 'event-1', '{}', '{}', '{}', 1, ?, 'migration_held', 1700000000)`, deliveryID, routeDecisionID)
		return err
	}
	for index, routeDecisionID := range []any{nil, "", "   "} {
		if err := insert("invalid-route-"+string(rune('a'+index)), routeDecisionID); err == nil {
			t.Fatalf("invalid route decision id %#v was accepted", routeDecisionID)
		}
	}
	if err := insert("valid-route", "route-1"); err != nil {
		t.Fatalf("valid route decision id insert = %v", err)
	}
	for _, value := range []any{nil, "", "   "} {
		if _, err := store.db.ExecContext(ctx, "UPDATE delivery_migration_holds SET route_decision_id = ? WHERE delivery_id = 'valid-route'", value); err == nil {
			t.Fatalf("invalid route decision id update %#v was accepted", value)
		}
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE delivery_migration_holds SET route_decision_id = 'route-2' WHERE delivery_id = 'valid-route'"); err != nil {
		t.Fatalf("valid route decision id update = %v", err)
	}
}

func TestDeliveryMigrationHoldGuardDoesNotBlockUnrelatedLegacyUpdates(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "v2.sqlite")
	createV2Database(t, databasePath)
	store, err := Open(ctx, Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var routeDecisionID sql.NullString
	if err := store.db.QueryRowContext(ctx, "SELECT route_decision_id FROM delivery_migration_holds WHERE delivery_id = 'legacy-delivery'").Scan(&routeDecisionID); err != nil {
		t.Fatal(err)
	}
	if routeDecisionID.Valid {
		t.Fatalf("legacy route decision id = %q, want NULL", routeDecisionID.String)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE delivery_migration_holds SET event_id = 'legacy-event-updated' WHERE delivery_id = 'legacy-delivery'"); err != nil {
		t.Fatalf("unrelated legacy hold update = %v", err)
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

func TestMigration004RollsBackAuthenticationSchemaAsOneTransaction(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "v3.sqlite")
	backupPath := filepath.Join(dir, "before-failed-v4.sqlite")
	createV3Database(t, databasePath)
	db := openRawDatabase(t, databasePath)
	// Make the first v4 statement fail without modifying the immutable v3
	// history. The transaction must not leave setup_state or setup_tokens.
	mustExec(t, db, "CREATE TABLE administrators (id TEXT PRIMARY KEY)")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Config{Path: databasePath, PreMigrationBackup: PreMigrationBackupConfig{Destination: backupPath}})
	if store != nil || err == nil {
		t.Fatalf("Open(conflicting v4 migration) = store %v, err %v", store, err)
	}
	if !fileExists(backupPath) {
		t.Fatal("failed v4 migration did not retain the pre-migration backup")
	}
	db = openRawDatabase(t, databasePath)
	defer db.Close()
	if rawSchemaVersion(t, db) != 3 {
		t.Fatal("failed v4 migration changed schema history")
	}
	if rawTableExists(t, db, "setup_state") || rawTableExists(t, db, "setup_tokens") || rawTableExists(t, db, "sessions") {
		t.Fatal("failed v4 migration left partial authentication tables")
	}
}

func createV2Database(t *testing.T, path string) {
	t.Helper()
	createV1Database(t, path)
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
	if _, err := tx.Exec(definitions[1].sql); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", definitions[1].version, definitions[1].name, definitions[1].checksum, 1700000000); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createV3Database(t *testing.T, path string) {
	t.Helper()
	createV2Database(t, path)
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
	if _, err := tx.Exec(definitions[2].sql); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", definitions[2].version, definitions[2].name, definitions[2].checksum, 1700000000); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createV4Database(t *testing.T, path string) {
	t.Helper()
	createV3Database(t, path)
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
	if _, err := tx.Exec(definitions[3].sql); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", definitions[3].version, definitions[3].name, definitions[3].checksum, 1700000000); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createV5Database(t *testing.T, path string) {
	t.Helper()
	createV4Database(t, path)
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
	if _, err := tx.Exec(definitions[4].sql); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", definitions[4].version, definitions[4].name, definitions[4].checksum, 1700000000); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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

func rawTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count != 0
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
