package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

func TestPlatformHTTPRecoversEnforceDecisionBeforeAcceptingTraffic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "startup-recovery.sqlite")
	now := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ai := platformdb.NewAIRepository(store)
	event := domain.NormalizedEvent{ID: "startup-recovery-event", NormalizedEventID: "startup-recovery-event", ObservedEventID: "startup-recovery-observed", SchemaVersion: 1, Platform: domain.PlatformBilibili, SourceID: "source-startup", ExternalID: "event-startup", EventType: domain.EventDynamic, Body: "public", NormalizerVersion: "bilibili-v1", PreprocessorVersion: "text-v1", ObservedAt: now, CreatedAt: now, ReplayPayload: json.RawMessage(`{"public":true}`)}
	if err := ai.PutNormalizedEvent(ctx, event); err != nil {
		store.Close()
		t.Fatal(err)
	}
	release := classifier.Release{ID: "startup-recovery-release", Fingerprint: "startup-recovery-fingerprint", ProviderType: "openai-compatible", BaseURL: "https://provider.example.test/v1", Model: "model", PromptVersion: classifier.PromptVersion, PromptDigest: classifier.PromptDigest(), SchemaVersion: classifier.ClassificationSchemaVersion, SchemaDigest: classifier.SchemaDigest(), StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-v1", Active: true, CreatedAt: now}
	if err := ai.SaveRelease(ctx, release); err != nil {
		store.Close()
		t.Fatal(err)
	}
	decision, claimed, err := ai.EnsureDecisionClaim(ctx, event.ID, release.ID, "enforce", now)
	if err != nil || !claimed {
		store.Close()
		t.Fatalf("decision claim = %#v claimed=%v err=%v", decision, claimed, err)
	}
	if err := ai.MarkDecisionCallStarted(ctx, decision.ID, now.Add(time.Second)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	value, reopened, err := newPlatformHTTP(PlatformConfig{DatabasePath: path, MasterKeyFile: filepath.Join(t.TempDir(), "master.key")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if value.restoreObservation != nil {
		defer value.restoreObservation()
	}
	if value.shadowRuntime != nil {
		defer value.shadowRuntime.Close(ctx)
	}
	if value.observationRecorder != nil {
		defer value.observationRecorder.Close(ctx)
	}
	defer reopened.Close()
	recovered, err := platformdb.NewAIRepository(reopened).Decision(ctx, decision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != classifier.StatusUncertainCall || recovered.EffectiveAction != "pass" {
		t.Fatalf("startup recovery decision = %#v, want uncertain_call/PASS", recovered)
	}
}
