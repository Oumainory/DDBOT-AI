package platformdb

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
)

func durableIdempotencyStore(t *testing.T, retention ...time.Duration) (*Store, *DurableIdempotencyStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "idempotency.sqlite")
	store, err := Open(context.Background(), Config{Path: path, Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	durable := NewIdempotencyStore(store, retention...)
	return store, durable, path
}

func TestDurableIdempotencyPersistsClaimCompletionAndExactReplay(t *testing.T) {
	store, durable, path := durableIdempotencyStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	fingerprint, err := idempotency.NewFingerprint("post", "/api/v2/sources/one?b=2&a=1&a=0", []byte(`{"name":"source","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	key := "durable-claim-key-01"
	claimed, outcome, err := durable.BeginCommand("admin-1", key, "create_subscription", fingerprint, now)
	if err != nil || outcome != idempotency.OutcomeReserved || claimed.ExecutionStatus != idempotency.ExecutionInProgress {
		t.Fatalf("claim = %#v, outcome=%v, err=%v", claimed, outcome, err)
	}
	if claimed.ExpiresAt.Unix() != now.Add(idempotency.DefaultRetention).Unix() {
		t.Fatalf("expiry = %v, want first claim + retention", claimed.ExpiresAt)
	}
	if _, err := durable.CompleteCommand("admin-1", key, "create_subscription", fingerprint, 201, map[string]string{
		"Content-Type":  "application/json",
		"Location":      "/api/v2/sources/one",
		"Set-Cookie":    "session=must-not-persist",
		"Authorization": "Bearer must-not-persist",
	}, []byte(`{"data":{"id":"source-1"}}`), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if raw := countIdempotencyRows(t, store); raw != 1 {
		t.Fatalf("idempotency rows = %d, want 1", raw)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, Config{Path: path, Now: func() time.Time { return now.Add(2 * time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayStore := NewIdempotencyStore(reopened)
	replayed, outcome, err := replayStore.BeginCommand("admin-1", key, "create_subscription", fingerprint, now.Add(2*time.Minute))
	if err != nil || outcome != idempotency.OutcomeReplay || replayed.StatusCode != 201 || string(replayed.Body) != `{"data":{"id":"source-1"}}` {
		t.Fatalf("replay = %#v, outcome=%v, err=%v", replayed, outcome, err)
	}
	if _, ok := replayed.Headers["Set-Cookie"]; ok {
		t.Fatal("Set-Cookie was persisted in replay headers")
	}
	if _, ok := replayed.Headers["Authorization"]; ok {
		t.Fatal("Authorization was persisted in replay headers")
	}
	if replayed.Headers["Content-Type"] != "application/json" || replayed.Headers["Location"] != "/api/v2/sources/one" {
		t.Fatalf("allowlisted headers = %#v", replayed.Headers)
	}
	if replayed.ExpiresAt.Unix() != claimed.ExpiresAt.Unix() {
		t.Fatalf("replay extended expiry: first=%v replay=%v", claimed.ExpiresAt, replayed.ExpiresAt)
	}

	queryConflict, err := idempotency.NewFingerprint("post", "/api/v2/sources/one?a=1&b=2", []byte(`{"name":"source","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := replayStore.BeginCommand("admin-1", key, "create_subscription", queryConflict, now.Add(3*time.Minute)); !errors.Is(err, idempotency.ErrConflict) || outcome != idempotency.OutcomeConflict {
		t.Fatalf("query conflict = outcome=%v err=%v", outcome, err)
	}
	if _, outcome, err := replayStore.BeginCommand("admin-1", key, "delete_subscription", fingerprint, now.Add(3*time.Minute)); !errors.Is(err, idempotency.ErrConflict) || outcome != idempotency.OutcomeConflict {
		t.Fatalf("command conflict = outcome=%v err=%v", outcome, err)
	}
	methodConflict, err := idempotency.NewFingerprint("PATCH", "/api/v2/sources/one?b=2&a=1&a=0", []byte(`{"name":"source","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := replayStore.BeginCommand("admin-1", key, "create_subscription", methodConflict, now.Add(3*time.Minute)); !errors.Is(err, idempotency.ErrConflict) || outcome != idempotency.OutcomeConflict {
		t.Fatalf("method conflict = outcome=%v err=%v", outcome, err)
	}
	pathConflict, err := idempotency.NewFingerprint("POST", "/api/v2/sources/two?b=2&a=1&a=0", []byte(`{"name":"source","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := replayStore.BeginCommand("admin-1", key, "create_subscription", pathConflict, now.Add(3*time.Minute)); !errors.Is(err, idempotency.ErrConflict) || outcome != idempotency.OutcomeConflict {
		t.Fatalf("path conflict = outcome=%v err=%v", outcome, err)
	}
	bodyConflict, err := idempotency.NewFingerprint("POST", "/api/v2/sources/one?b=2&a=1&a=0", []byte(`{"name":"different","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := replayStore.BeginCommand("admin-1", key, "create_subscription", bodyConflict, now.Add(3*time.Minute)); !errors.Is(err, idempotency.ErrConflict) || outcome != idempotency.OutcomeConflict {
		t.Fatalf("body conflict = outcome=%v err=%v", outcome, err)
	}
}

func TestDurableIdempotencyInProgressSurvivesRestartWithoutReexecution(t *testing.T) {
	store, durable, path := durableIdempotencyStore(t)
	now := time.Unix(1700000000, 0).UTC()
	fingerprint, err := idempotency.NewFingerprint("post", "/api/v2/route-decisions/r1/replay", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := durable.BeginCommand("admin-1", "restart-in-progress-01", "manual_replay", fingerprint, now); err != nil || outcome != idempotency.OutcomeReserved {
		t.Fatalf("initial claim = outcome=%v err=%v", outcome, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{Path: path, Now: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, outcome, err := NewIdempotencyStore(reopened).BeginCommand("admin-1", "restart-in-progress-01", "manual_replay", fingerprint, now.Add(time.Minute)); !errors.Is(err, idempotency.ErrInProgress) || outcome != idempotency.OutcomeInProgress {
		t.Fatalf("restart claim = outcome=%v err=%v, want durable in_progress", outcome, err)
	}
}

func TestDurableIdempotencyPruneUsesFirstClaimExpiry(t *testing.T) {
	store, durable, _ := durableIdempotencyStore(t, time.Hour)
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	fingerprint, err := idempotency.NewFingerprint("post", "/api/v2/test", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := durable.BeginCommand("admin-1", "prune-key-000001", "manual_retry", fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if got := durable.Prune(now.Add(time.Hour)); got != 1 {
		t.Fatalf("pruned = %d, want 1", got)
	}
	if _, found, err := durable.Lookup("admin-1", "prune-key-000001", now.Add(time.Hour)); err != nil || found {
		t.Fatalf("lookup after prune = found=%v err=%v", found, err)
	}
}

func TestDurableIdempotencyConcurrentBeginHasOneReservation(t *testing.T) {
	store, durable, _ := durableIdempotencyStore(t)
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	fingerprint, err := idempotency.NewFingerprint("POST", "/api/v2/replay?route=one", []byte(`{"confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	reserved, inProgress := 0, 0
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, outcome, beginErr := durable.BeginCommand("admin-1", "concurrent-claim-01", "manual_replay", fingerprint, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case beginErr == nil && outcome == idempotency.OutcomeReserved:
				reserved++
			case errors.Is(beginErr, idempotency.ErrInProgress) && outcome == idempotency.OutcomeInProgress:
				inProgress++
			default:
				t.Errorf("concurrent begin = outcome=%v err=%v", outcome, beginErr)
			}
		}()
	}
	close(start)
	wg.Wait()
	if reserved != 1 || inProgress != attempts-1 {
		t.Fatalf("concurrent claims = reserved=%d in_progress=%d, want 1/%d", reserved, inProgress, attempts-1)
	}
}

func countIdempotencyRows(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM idempotency_records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
