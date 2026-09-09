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

func TestMemoryStorePersistsExplicitInProgressAndCompletedState(t *testing.T) {
	store := NewMemoryStore(7 * 24 * time.Hour)
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	fingerprint := testFingerprint(t, "/api/v2/subscriptions?source=src-1", `{"target_id":"tgt-1"}`)
	key := "0123456789abcdef"

	claimed, outcome, err := store.BeginCommand("admin-1", key, "create_subscription", fingerprint, now)
	if err != nil || outcome != OutcomeReserved {
		t.Fatalf("claim = %#v, %v, %v", claimed, outcome, err)
	}
	if claimed.ExecutionStatus != ExecutionInProgress || claimed.Completed() || !claimed.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("claim state = %#v", claimed)
	}
	inProgress, outcome, err := store.BeginCommand("admin-1", key, "create_subscription", fingerprint, now.Add(time.Minute))
	if !errors.Is(err, ErrInProgress) || outcome != OutcomeInProgress || inProgress.ExecutionStatus != ExecutionInProgress {
		t.Fatalf("in-progress replay = %#v, %v, %v", inProgress, outcome, err)
	}

	completedAt := now.Add(2 * time.Minute)
	completed, err := store.CompleteCommand("admin-1", key, "create_subscription", fingerprint, 201, map[string]string{"Location": "/api/v2/subscriptions/sub-1"}, []byte(`{"id":"sub-1"}`), completedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ExecutionStatus != ExecutionCompleted || !completed.Completed() || completed.CompletedAt == nil || !completed.CompletedAt.Equal(completedAt) {
		t.Fatalf("completed state = %#v", completed)
	}
	if !completed.ExpiresAt.Equal(claimed.ExpiresAt) {
		t.Fatalf("completion extended expiry from %s to %s", claimed.ExpiresAt, completed.ExpiresAt)
	}
	replayed, outcome, err := store.BeginCommand("admin-1", key, "create_subscription", fingerprint, now.Add(3*time.Minute))
	if err != nil || outcome != OutcomeReplay || replayed.ExecutionStatus != ExecutionCompleted {
		t.Fatalf("completed replay = %#v, %v, %v", replayed, outcome, err)
	}
}

func TestMemoryStoreRejectsEveryRequestFingerprintConflict(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	key := "0123456789abcdef"
	base := testFingerprint(t, "/api/v2/subscriptions?source=src-1", `{"target_id":"tgt-1"}`)
	if _, _, err := store.BeginCommand("admin-1", key, "create_subscription", base, now); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		fp   Fingerprint
	}{
		{name: "method", fp: fingerprintMust(t, "PUT", "/api/v2/subscriptions?source=src-1", `{"target_id":"tgt-1"}`)},
		{name: "path", fp: fingerprintMust(t, "POST", "/api/v2/subscriptions/sub-1?source=src-1", `{"target_id":"tgt-1"}`)},
		{name: "query", fp: fingerprintMust(t, "POST", "/api/v2/subscriptions?source=src-2", `{"target_id":"tgt-1"}`)},
		{name: "body", fp: fingerprintMust(t, "POST", "/api/v2/subscriptions?source=src-1", `{"target_id":"tgt-2"}`)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, outcome, err := store.BeginCommand("admin-1", key, "create_subscription", test.fp, now.Add(time.Minute)); !errors.Is(err, ErrConflict) || outcome != OutcomeConflict {
				t.Fatalf("conflict = %v, %v", outcome, err)
			}
		})
	}
	otherCommand := testFingerprint(t, "/api/v2/subscriptions?source=src-1", `{"target_id":"tgt-1"}`)
	if _, outcome, err := store.BeginCommand("admin-1", key, "delete_subscription", otherCommand, now.Add(time.Minute)); !errors.Is(err, ErrConflict) || outcome != OutcomeConflict {
		t.Fatalf("command type conflict = %v, %v", outcome, err)
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

func fingerprintMust(t *testing.T, method, path, body string) Fingerprint {
	t.Helper()
	fingerprint, err := NewFingerprint(method, path, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
