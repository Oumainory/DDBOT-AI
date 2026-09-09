package idempotency

import (
	"errors"
	"testing"
	"time"
)

func testFingerprint(t *testing.T, path string, body string) Fingerprint {
	t.Helper()
	fingerprint, err := NewFingerprint("POST", path, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestMemoryStoreReplaysOnlyIdenticalRequests(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	fingerprint := testFingerprint(t, "/api/v2/subscriptions", `{"source_id":"src-1","target_id":"tgt-1"}`)
	key := "0123456789abcdef"

	reserved, outcome, err := store.Begin("admin-1", key, fingerprint, now)
	if err != nil || outcome != OutcomeReserved || reserved.Completed() {
		t.Fatalf("first begin = %#v, %v, %v", reserved, outcome, err)
	}
	if _, err := store.Complete("admin-1", key, fingerprint, 201, map[string]string{"Location": "/api/v2/subscriptions/sub-1"}, []byte(`{"id":"sub-1"}`), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	replayed, outcome, err := store.Begin("admin-1", key, fingerprint, now.Add(2*time.Minute))
	if err != nil || outcome != OutcomeReplay || replayed.StatusCode != 201 || string(replayed.Body) != `{"id":"sub-1"}` {
		t.Fatalf("replay = %#v, %v, %v", replayed, outcome, err)
	}

	conflicting := testFingerprint(t, "/api/v2/subscriptions", `{"source_id":"src-2","target_id":"tgt-1"}`)
	if _, outcome, err := store.Begin("admin-1", key, conflicting, now.Add(3*time.Minute)); !errors.Is(err, ErrConflict) || outcome != OutcomeConflict {
		t.Fatalf("conflicting request error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreRetentionAllowsKeyReuseAfterExpiry(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	fingerprint := testFingerprint(t, "/api/v2/events/event-1/replay", `{}`)
	key := "0123456789abcdef"
	if _, _, err := store.Begin("admin-1", key, fingerprint, now); err != nil {
		t.Fatal(err)
	}

	if _, outcome, err := store.Begin("admin-1", key, fingerprint, now.Add(time.Hour)); err != nil || outcome != OutcomeReserved {
		t.Fatalf("expired key begin = %v, %v, want new reservation", outcome, err)
	}
	if removed := store.Prune(now.Add(2 * time.Hour)); removed != 1 {
		t.Fatalf("pruned = %d, want 1", removed)
	}
}
