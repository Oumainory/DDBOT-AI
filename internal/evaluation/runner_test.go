package evaluation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
)

func evaluationEvent(id string) domain.NormalizedEvent {
	return domain.NormalizedEvent{ID: id, NormalizedEventID: id, ObservedEventID: "obs-" + id, SchemaVersion: 1, Platform: domain.PlatformBilibili, SourceID: "source", ExternalID: "event-" + id, EventType: domain.EventDynamic, Body: "public event", NormalizerVersion: "bilibili-v1", PreprocessorVersion: "text-v1", ObservedAt: time.Unix(1700000000, 0).UTC(), CreatedAt: time.Unix(1700000000, 0).UTC(), ReplayPayload: json.RawMessage(`{"public":true}`)}
}

func evaluationRelease() classifier.Release {
	return classifier.Release{ID: "release-eval", Fingerprint: "sha256:eval", ProviderType: "openai-compatible", BaseURL: "https://llm.example.test/v1", Model: "model", PromptVersion: classifier.PromptVersion, PromptDigest: classifier.PromptDigest(), SchemaVersion: classifier.ClassificationSchemaVersion, SchemaDigest: classifier.SchemaDigest(), StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-twitter-v1", PricingCurrency: "USD", Active: true, CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func TestRunnerPersistsCaseResultsAndMetrics(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := platformdb.NewAIRepository(store)
	release := evaluationRelease()
	if err := repository.SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	event := evaluationEvent("norm-eval")
	raw, _ := json.Marshal(event)
	if err := repository.CreateEvaluationCase(context.Background(), platformdb.EvaluationCaseRecord{ID: "case-eval", NormalizedInputSnapshot: raw, ExpectedAction: "drop", Critical: false, LabelKind: "synthetic", CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateEvaluationRun(context.Background(), platformdb.EvaluationRunRecord{ID: "run-eval", ClassifierReleaseID: release.ID, CaseIDs: []string{"case-eval"}, CreatedAt: time.Unix(1700000000, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	input, output := int64(11), int64(3)
	fake := &provider.FakeOpenAICompatibleProvider{ClassifyFunc: func(_ context.Context, _ domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{InputTokens: &input, OutputTokens: &output}, nil
	}}
	runner := New(repository, fake, func() time.Time { return time.Unix(1700000010, 0).UTC() })
	result, err := runner.Run(context.Background(), "run-eval")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("run status = %q", result.Status)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", fake.CallCount())
	}
	results, err := repository.EvaluationResults(context.Background(), "run-eval")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SuggestedAction != "drop" || results[0].Comparison != "match" {
		t.Fatalf("evaluation result = %#v", results)
	}
	if result.TotalCostMicros != 0 {
		t.Fatalf("unexpected cost = %d", result.TotalCostMicros)
	}
}

func TestRunnerDoesNotRetryTerminalRun(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := platformdb.NewAIRepository(store)
	release := evaluationRelease()
	if err := repository.SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateEvaluationRun(context.Background(), platformdb.EvaluationRunRecord{ID: "run-empty", ClassifierReleaseID: release.ID, CreatedAt: time.Unix(1700000000, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	fake := &provider.FakeOpenAICompatibleProvider{}
	runner := New(repository, fake, nil)
	if _, err := runner.Run(context.Background(), "run-empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), "run-empty"); err != nil {
		t.Fatal(err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("empty run provider calls = %d", fake.CallCount())
	}
}
