package platformdb

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
)

func aiTestEvent() domain.NormalizedEvent {
	return domain.NormalizedEvent{ID: "norm-test-event", NormalizedEventID: "norm-test-event", ObservedEventID: "obs-test-event", SchemaVersion: 1, Platform: domain.PlatformBilibili, SourceID: "source-1", ExternalID: "event-1", EventType: domain.EventDynamic, Body: "public", NormalizerVersion: "bilibili-v1", PreprocessorVersion: "text-v1", ObservedAt: time.Unix(1700000000, 0).UTC(), CreatedAt: time.Unix(1700000000, 0).UTC(), ReplayPayload: json.RawMessage(`{"public":true}`)}
}

func aiTestRelease() classifier.Release {
	return classifier.Release{ID: "release-test", Fingerprint: "fp-test", ProviderType: "openai-compatible", BaseURL: "https://provider.test/v1", Model: "model", PromptVersion: classifier.PromptVersion, PromptDigest: classifier.PromptDigest(), SchemaVersion: classifier.ClassificationSchemaVersion, SchemaDigest: classifier.SchemaDigest(), StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-v1", CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func TestAIRepositoryClaimIsAtMostOnceAndRecoversUncertainCall(t *testing.T) {
	store, err := Open(context.Background(), Config{Path: "file::memory:?cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewAIRepository(store)
	if err := repo.PutNormalizedEvent(context.Background(), aiTestEvent()); err != nil {
		t.Fatal(err)
	}
	release := aiTestRelease()
	if err := repo.SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	var claimedDecision classifier.Decision
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision, claimed, claimErr := repo.EnsureDecisionClaim(context.Background(), "norm-test-event", "release-test", "shadow", time.Unix(1700000000, 0))
			if claimErr != nil {
				t.Errorf("claim = %v", claimErr)
				return
			}
			if claimed {
				mu.Lock()
				claims++
				claimedDecision = decision
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("claims = %d, want 1", claims)
	}
	if claimedDecision.ID == "" {
		t.Fatal("no claimed decision")
	}
	if _, err := repo.Decision(context.Background(), ""); err == nil || !errors.Is(err, ErrDecisionNotFound) {
		t.Fatalf("empty decision lookup = %v", err)
	}
	if err := repo.MarkDecisionCallStarted(context.Background(), claimedDecision.ID, time.Unix(1700000001, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecoverDecisions(context.Background(), time.Unix(1700000002, 0)); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.Decision(context.Background(), claimedDecision.ID)
	if err != nil || claimed.Status != classifier.StatusUncertainCall || claimed.EffectiveAction != "pass" {
		t.Fatalf("recovered decision = %#v %v", claimed, err)
	}
}

func TestAIRepositoryRouteEvaluationAndProfilesAreDurable(t *testing.T) {
	store, err := Open(context.Background(), Config{Path: "file::memory:?cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewAIRepository(store)
	if err := repo.SaveProfile(context.Background(), policy.OfficialGameProfile(), time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Profile(context.Background(), "builtin.official_game"); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutRouteEvaluation(context.Background(), RouteEvaluationRecord{RouteObservationID: "route-off", EffectiveMode: "off", EffectiveAction: "pass", CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutRouteEvaluation(context.Background(), RouteEvaluationRecord{RouteObservationID: "route-off", EffectiveMode: "off", EffectiveAction: "pass", CreatedAt: time.Unix(2, 0)}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM ai_route_evaluations WHERE route_observation_id='route-off'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("off route evaluations = %d", count)
	}
	if _, err := repo.EnforceReadiness(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAIRepositoryDecisionReviewAndSummaryAreDurable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: "file::memory:?cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewAIRepository(store)
	if err := repo.PutNormalizedEvent(ctx, aiTestEvent()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveRelease(ctx, aiTestRelease()); err != nil {
		t.Fatal(err)
	}
	decision, claimed, err := repo.EnsureDecisionClaim(ctx, "norm-test-event", "release-test", "shadow", time.Unix(1700000000, 0))
	if err != nil || !claimed {
		t.Fatalf("claim = %#v, %v, claimed=%v", decision, err, claimed)
	}
	if err := repo.MarkDecisionCallStarted(ctx, decision.ID, time.Unix(1700000001, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishDecision(ctx, decision.ID, classifier.StatusCompleted, classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, "drop", "", "provider", "model", "", classifier.Usage{}, nil, "USD", nil, time.Unix(1700000002, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReviewDecision(ctx, decision.ID, true, time.Unix(1700000003, 0)); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Decision(ctx, decision.ID)
	if err != nil || !loaded.Reviewed || loaded.ReviewedAt == nil || loaded.ReviewedAt.Unix() != 1700000003 {
		t.Fatalf("reviewed decision = %#v, %v", loaded, err)
	}
	summary, err := repo.ShadowSummary(ctx)
	if err != nil || summary.ReviewedDrop != 1 {
		t.Fatalf("summary = %#v, %v", summary, err)
	}
	if err := repo.ReviewDecision(ctx, decision.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	loaded, err = repo.Decision(ctx, decision.ID)
	if err != nil || loaded.Reviewed || loaded.ReviewedAt != nil {
		t.Fatalf("unreviewed decision = %#v, %v", loaded, err)
	}
}

func TestAIRepositoryReadinessIsolatedToActiveReleaseAndLatestCaseResult(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: "file::memory:?cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewAIRepository(store)
	at := time.Unix(1700000000, 0).UTC()
	caseRaw, _ := json.Marshal(aiTestEvent())
	if err := repo.CreateEvaluationCase(ctx, EvaluationCaseRecord{ID: "case-release-isolation", NormalizedInputSnapshot: caseRaw, ExpectedImportance: string(domain.ImportanceHigh), ExpectedAction: "pass", LabelKind: "real_reviewed", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	releaseA := aiTestRelease()
	releaseA.ID, releaseA.Fingerprint, releaseA.Active = "release-readiness-a", "fp-readiness-a", true
	if err := repo.SaveRelease(ctx, releaseA); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateEvaluationRun(ctx, EvaluationRunRecord{ID: "run-readiness-a", ClassifierReleaseID: releaseA.ID, CaseIDs: []string{"case-release-isolation"}, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishEvaluationRun(ctx, "run-readiness-a", "completed", nil, 0, 0, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutEvaluationResult(ctx, EvaluationResultRecord{ID: "result-readiness-a", RunID: "run-readiness-a", CaseID: "case-release-isolation", SuggestedAction: "pass", ExpectedAction: "pass", Comparison: "match", CreatedAt: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	aReadiness, err := repo.EnforceReadiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if aReadiness.CurrentReleaseID != releaseA.ID || aReadiness.RegressionCases != 1 || aReadiness.ImportantPassCases != 1 {
		t.Fatalf("release A readiness = %#v", aReadiness)
	}

	releaseB := aiTestRelease()
	releaseB.ID, releaseB.Fingerprint, releaseB.Active = "release-readiness-b", "fp-readiness-b", true
	if err := repo.SaveRelease(ctx, releaseB); err != nil {
		t.Fatal(err)
	}
	bEmpty, err := repo.EnforceReadiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bEmpty.CurrentReleaseID != releaseB.ID || bEmpty.RegressionCases != 0 || bEmpty.ImportantPassCases != 0 || bEmpty.KnownImportantFalseDrops != 0 {
		t.Fatalf("new release inherited old evidence = %#v", bEmpty)
	}

	// Two completed runs for the same case must count once, using the latest
	// deterministic run result. The first result is a false DROP; the latest
	// result is a PASS and therefore clears the metric for release B without
	// importing release A's evidence.
	if err := repo.CreateEvaluationRun(ctx, EvaluationRunRecord{ID: "run-readiness-b-old", ClassifierReleaseID: releaseB.ID, CaseIDs: []string{"case-release-isolation"}, CreatedAt: at.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishEvaluationRun(ctx, "run-readiness-b-old", "completed", nil, 0, 0, at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutEvaluationResult(ctx, EvaluationResultRecord{ID: "result-readiness-b-old", RunID: "run-readiness-b-old", CaseID: "case-release-isolation", SuggestedAction: "drop", ExpectedAction: "pass", Comparison: "mismatch", CreatedAt: at.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateEvaluationRun(ctx, EvaluationRunRecord{ID: "run-readiness-b-latest", ClassifierReleaseID: releaseB.ID, CaseIDs: []string{"case-release-isolation"}, CreatedAt: at.Add(4 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishEvaluationRun(ctx, "run-readiness-b-latest", "completed", nil, 0, 0, at.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutEvaluationResult(ctx, EvaluationResultRecord{ID: "result-readiness-b-latest", RunID: "run-readiness-b-latest", CaseID: "case-release-isolation", SuggestedAction: "pass", ExpectedAction: "pass", Comparison: "match", CreatedAt: at.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	bReadiness, err := repo.EnforceReadiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bReadiness.RegressionCases != 1 || bReadiness.ImportantPassCases != 1 || bReadiness.KnownImportantFalseDrops != 0 || bReadiness.DropPrecision != 1 {
		t.Fatalf("release B duplicate/latest metrics = %#v", bReadiness)
	}
}

func TestAIRepositoryReadinessReturnsFeedbackStorageError(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: "file::memory:?cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, "DROP TABLE feedback"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAIRepository(store).EnforceReadiness(ctx); err == nil {
		t.Fatal("readiness unexpectedly succeeded after feedback storage was removed")
	}
}

func TestAIRepositoryEvaluationImportIsAtomicAndStrict(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: "file::memory:?cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewAIRepository(store)
	event := aiTestEvent()
	snapshot, _ := json.Marshal(event)
	valid := EvaluationCaseRecord{ID: "case-import", NormalizedInputSnapshot: snapshot, LabelKind: "synthetic", Source: "test", CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}
	invalid := valid
	invalid.ID = "case-invalid"
	invalid.NormalizedInputSnapshot = json.RawMessage(`{"schema_version":1,"platform":"bilibili","source_id":"source","external_id":"event","event_type":"dynamic","replay_payload":{},"created_at":"2023-11-14T22:13:20Z","unknown_secret":"do-not-store"}`)
	if err := repo.ImportEvaluationCases(ctx, []EvaluationCaseRecord{valid, invalid}); !errors.Is(err, ErrEvaluationInvalid) {
		t.Fatalf("invalid import = %v", err)
	}
	if values, err := repo.EvaluationCases(ctx); err != nil || len(values) != 0 {
		t.Fatalf("partial import values = %#v, err=%v", values, err)
	}
	if err := repo.ImportEvaluationCases(ctx, []EvaluationCaseRecord{valid}); err != nil {
		t.Fatal(err)
	}
	values, err := repo.EvaluationCases(ctx)
	if err != nil || len(values) != 1 || string(values[0].NormalizedInputSnapshot) != string(snapshot) {
		t.Fatalf("imported values = %#v, err=%v", values, err)
	}
}
