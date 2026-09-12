package audit

import (
	"context"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

func TestLedgerHashChainDetectsTamper(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger := New(platformdb.NewMigrationRepository(store), nil)
	if _, err := ledger.AppendSafe(ctx, "admin", "migration.create", "connector_migration", "m1", "success", "req-1", "", map[string]any{"count": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.AppendSafe(ctx, "admin", "migration.commit", "connector_migration", "m1", "success", "req-2", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := ledger.VerifyAuditChain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerPruneWritesAnchorAndKeepsChainVerifiable(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1700000100, 0).UTC()
	ledger := New(platformdb.NewMigrationRepository(store), func() time.Time { return now })
	if _, err := ledger.Append(ctx, Entry{OccurredAt: now.Add(-2 * time.Hour).Unix(), Action: "old", ResourceType: "test", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Append(ctx, Entry{OccurredAt: now.Add(-time.Hour).Unix(), Action: "keep", ResourceType: "test", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	pruned, err := ledger.Prune(ctx, now.Add(-90*time.Minute))
	if err != nil || pruned != 1 {
		t.Fatalf("prune=%d err=%v", pruned, err)
	}
	if err := ledger.VerifyAuditChain(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := ledger.List(ctx, "", "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].Action != "audit.prune" || entries[1].Action != "keep" {
		t.Fatalf("unexpected retained audit entries=%#v", entries)
	}
}
