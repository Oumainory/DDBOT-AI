package replay

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

func TestReplayBypassesAIAndIsIdempotent(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "replay.sqlite"), Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := platformdb.NewPhase5Repository(store)
	event := domain.NormalizedEvent{ID: "event-replay-1", NormalizedEventID: "event-replay-1", ObservedEventID: "obs-replay-1", Platform: domain.PlatformTwitter, SourceID: "source-1", ExternalID: "tweet-1", EventType: domain.EventTweet, Body: "public", NormalizerVersion: "n1", CreatedAt: at, ReplayPayload: json.RawMessage(`{"public":true}`)}
	snapshot, err := FromNormalizedEvent(event, "route-replay-1", TargetIdentity{TargetID: "target-1", TargetType: "group", ExternalID: "123", ConnectorID: "connector-1"}, "sub-1", "ai-1", at)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := snapshot.Marshal()
	if err := repo.PersistDrop(context.Background(), platformdb.RouteDecisionRecord{ID: "route-replay-1", EventID: event.ID, SourceID: event.SourceID, TargetID: "target-1", ConfiguredMode: "enforce", EffectiveMode: "enforce", SuggestedAction: "drop", EffectiveAction: "drop", CreatedAt: at, DecidedAt: at}, platformdb.ReplayableEventRecord{ID: "replay-1", SchemaVersion: 1, RouteDecisionID: "route-replay-1", EventID: event.ID, SourceID: event.SourceID, TargetID: "target-1", EventType: string(event.EventType), SnapshotJSON: raw, CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	service := NewService(Config{Repository: repo, Idempotency: idempotency.NewMemoryStore(time.Hour), Render: func(context.Context, Snapshot) ([]byte, error) { return []byte("current-template"), nil }, Send: func(context.Context, TargetIdentity, []byte) (SendResult, error) {
		sends.Add(1)
		return SendResult{Status: domain.DeliverySent, ResultCode: "sent"}, nil
	}, Now: func() time.Time { return at.Add(time.Minute) }})
	fingerprint, _ := idempotency.NewFingerprint("POST", "/api/v2/route-decisions/route-replay-1/replay", []byte(`{}`))
	first, err := service.Replay(context.Background(), "route-replay-1", "admin", "replay-idempotency-1", fingerprint)
	if err != nil || first.Status != domain.DeliverySent {
		t.Fatalf("first replay = %#v, err=%v", first, err)
	}
	second, err := service.Replay(context.Background(), "route-replay-1", "admin", "replay-idempotency-1", fingerprint)
	if err != nil || second.ID != first.ID {
		t.Fatalf("idempotent replay = %#v/%#v, err=%v", first, second, err)
	}
	if sends.Load() != 1 {
		t.Fatalf("send count = %d, want one", sends.Load())
	}
}

func TestReplayDurableIdempotencySurvivesRestartWithoutResend(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	path := filepath.Join(t.TempDir(), "replay-durable.sqlite")
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: path, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	repo := platformdb.NewPhase5Repository(store)
	event := domain.NormalizedEvent{ID: "event-replay-restart", NormalizedEventID: "event-replay-restart", ObservedEventID: "obs-replay-restart", Platform: domain.PlatformTwitter, SourceID: "source-restart", ExternalID: "tweet-restart", EventType: domain.EventTweet, Body: "public", NormalizerVersion: "n1", CreatedAt: at, ReplayPayload: json.RawMessage(`{"public":true}`)}
	snapshot, err := FromNormalizedEvent(event, "route-replay-restart", TargetIdentity{TargetID: "target-restart", TargetType: "group", ExternalID: "123", ConnectorID: "connector-restart"}, "sub-restart", "ai-restart", at)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	raw, err := snapshot.Marshal()
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := repo.PersistDrop(context.Background(), platformdb.RouteDecisionRecord{ID: "route-replay-restart", EventID: event.ID, SourceID: event.SourceID, TargetID: "target-restart", ConfiguredMode: "enforce", EffectiveMode: "enforce", SuggestedAction: "drop", EffectiveAction: "drop", CreatedAt: at, DecidedAt: at}, platformdb.ReplayableEventRecord{ID: "replay-restart", SchemaVersion: 1, RouteDecisionID: "route-replay-restart", EventID: event.ID, SourceID: event.SourceID, TargetID: "target-restart", EventType: string(event.EventType), SnapshotJSON: raw, CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	fingerprint, err := idempotency.NewFingerprint("POST", "/api/v2/route-decisions/route-replay-restart/replay", []byte(`{}`))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	var sends atomic.Int32
	firstService := NewService(Config{Repository: repo, Idempotency: platformdb.NewIdempotencyStore(store), Render: func(context.Context, Snapshot) ([]byte, error) { return []byte("current-template"), nil }, Send: func(context.Context, TargetIdentity, []byte) (SendResult, error) {
		sends.Add(1)
		return SendResult{Status: domain.DeliverySent, ResultCode: "sent"}, nil
	}, Now: func() time.Time { return at.Add(time.Minute) }})
	first, err := firstService.Replay(context.Background(), "route-replay-restart", "admin", "replay-restart-key", fingerprint)
	if err != nil || first.Status != domain.DeliverySent {
		_ = store.Close()
		t.Fatalf("first replay = %#v, err=%v", first, err)
	}
	if sends.Load() != 1 {
		_ = store.Close()
		t.Fatalf("first send count = %d, want one", sends.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := platformdb.Open(context.Background(), platformdb.Config{Path: path, Now: func() time.Time { return at.Add(2 * time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	secondService := NewService(Config{Repository: platformdb.NewPhase5Repository(reopened), Idempotency: platformdb.NewIdempotencyStore(reopened), Render: func(context.Context, Snapshot) ([]byte, error) {
		t.Fatal("renderer called during completed replay")
		return nil, nil
	}, Send: func(context.Context, TargetIdentity, []byte) (SendResult, error) {
		t.Fatal("sender called during completed replay")
		return SendResult{}, nil
	}, Now: func() time.Time { return at.Add(2 * time.Minute) }})
	second, err := secondService.Replay(context.Background(), "route-replay-restart", "admin", "replay-restart-key", fingerprint)
	if err != nil || second.ID != first.ID || second.Status != domain.DeliverySent {
		t.Fatalf("restart replay = %#v, want original delivery %#v, err=%v", second, first, err)
	}
	if sends.Load() != 1 {
		t.Fatalf("restart send count = %d, want exactly one", sends.Load())
	}
}

func TestReplayRejectsExpiredSnapshotWithoutSourceFetch(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "replay-expired.sqlite"), Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := platformdb.NewPhase5Repository(store)
	if err := repo.PutRouteDecision(context.Background(), platformdb.RouteDecisionRecord{ID: "route-expired", EventID: "event-expired", ConfiguredMode: "enforce", EffectiveMode: "enforce", SuggestedAction: "drop", EffectiveAction: "drop", CreatedAt: at, DecidedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutReplayableEvent(context.Background(), platformdb.ReplayableEventRecord{ID: "replay-expired", SchemaVersion: 1, RouteDecisionID: "route-expired", EventID: "event-expired", EventType: "tweet", SnapshotJSON: json.RawMessage(`{"schema_version":1}`), CreatedAt: at.Add(-2 * time.Hour), ExpiresAt: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	service := NewService(Config{Repository: repo, Idempotency: idempotency.NewMemoryStore(idempotency.DefaultRetention), Render: func(context.Context, Snapshot) ([]byte, error) {
		t.Fatal("renderer called for expired snapshot")
		return nil, nil
	}, Send: func(context.Context, TargetIdentity, []byte) (SendResult, error) {
		t.Fatal("sender called for expired snapshot")
		return SendResult{}, nil
	}, Now: func() time.Time { return at }})
	_, err = service.Replay(context.Background(), "route-expired", "admin", "replay-expired-key", idempotency.Fingerprint{Method: "POST", NormalizedPath: "/api/v2/route-decisions/route-expired/replay", BodySHA256: "x"})
	if err != platformdb.ErrReplayExpired {
		t.Fatalf("expired replay err = %v, want %v", err, platformdb.ErrReplayExpired)
	}
}
