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
	service := NewService(Config{Repository: repo, Render: func(context.Context, Snapshot) ([]byte, error) {
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
