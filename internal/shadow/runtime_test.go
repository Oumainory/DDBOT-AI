package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/normalizer"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
)

func shadowEvent(id string) domain.NormalizedEvent {
	return domain.NormalizedEvent{ID: id, NormalizedEventID: id, ObservedEventID: "obs-" + id, SchemaVersion: 1, Platform: domain.PlatformBilibili, SourceID: "source", ExternalID: "event-" + id, EventType: domain.EventDynamic, Body: "public event", NormalizerVersion: "bilibili-v1", PreprocessorVersion: "text-v1", ObservedAt: time.Unix(1700000000, 0).UTC(), CreatedAt: time.Unix(1700000000, 0).UTC(), ReplayPayload: json.RawMessage(`{"public":true}`)}
}

func shadowRelease() classifier.Release {
	return classifier.Release{ID: "release-shadow", Fingerprint: "sha256:shadow", ProviderType: "openai-compatible", BaseURL: "https://llm.example.test/v1", Model: "model", PromptVersion: classifier.PromptVersion, PromptDigest: classifier.PromptDigest(), SchemaVersion: classifier.ClassificationSchemaVersion, SchemaDigest: classifier.SchemaDigest(), StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-twitter-v1", PricingCurrency: "USD", Active: true, CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func shadowRepository(t *testing.T) *platformdb.AIRepository {
	t.Helper()
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := platformdb.NewAIRepository(store)
	if err := repository.SaveRelease(context.Background(), shadowRelease()); err != nil {
		t.Fatal(err)
	}
	return repository
}

func waitShadow(t *testing.T, runtime *Runtime) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.Stats().Completed > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("shadow runtime did not complete: %#v", runtime.Stats())
}

func TestSharedClassificationAcrossRoutes(t *testing.T) {
	repository := shadowRepository(t)
	input, output := int64(12), int64(4)
	fake := &provider.FakeOpenAICompatibleProvider{ClassifyFunc: func(_ context.Context, _ domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{InputTokens: &input, OutputTokens: &output}, nil
	}}
	runtime := New(Config{Repository: repository, Provider: fake, QueueCapacity: 2, MaxConcurrency: 2, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	routes := make([]Route, 10)
	for i := range routes {
		routes[i] = Route{RouteObservationID: "route-" + string(rune('a'+i)), Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeShadow}, Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile()}}
		if i%2 == 1 {
			routes[i].Policy.Profile = policy.Profile{ID: "pass-profile", Name: "pass", DefaultAction: policy.ActionPass}
		}
	}
	if err := runtime.Schedule(context.Background(), shadowEvent("event-shared"), shadowRelease(), routes); err != nil {
		t.Fatal(err)
	}
	waitShadow(t, runtime)
	if got := fake.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	decisions, _, err := repository.ListDecisions(context.Background(), 10, nil)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %#v, err = %v", decisions, err)
	}
	evaluations, err := repository.RouteEvaluationsForDecision(context.Background(), decisions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != len(routes) {
		t.Fatalf("route evaluations = %d, want %d", len(evaluations), len(routes))
	}
	var drops, passes int
	for _, evaluation := range evaluations {
		if evaluation.EffectiveAction != "pass" {
			t.Fatalf("effective action = %q", evaluation.EffectiveAction)
		}
		if evaluation.PolicySuggestedAction == "drop" {
			drops++
		} else {
			passes++
		}
	}
	if drops == 0 || passes == 0 {
		t.Fatalf("policy overlay did not diverge: drops=%d passes=%d", drops, passes)
	}
	if evaluations[0].PolicyProvenance["mode"] == "" {
		t.Fatalf("route evaluation lost mode provenance: %#v", evaluations[0].PolicyProvenance)
	}
}

func TestSeparateRouteSchedulesShareDecisionAndPersistEveryEvaluation(t *testing.T) {
	repository := shadowRepository(t)
	input, output := int64(3), int64(2)
	fake := &provider.FakeOpenAICompatibleProvider{ClassifyFunc: func(_ context.Context, _ domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		// Make the ownership race observable without changing the provider
		// contract: separate route jobs may arrive while this call is running.
		time.Sleep(20 * time.Millisecond)
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{InputTokens: &input, OutputTokens: &output}, nil
	}}
	runtime := New(Config{Repository: repository, Provider: fake, QueueCapacity: 16, MaxConcurrency: 2, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	event := shadowEvent("event-separate-routes")
	release := shadowRelease()
	for i := 0; i < 10; i++ {
		if err := runtime.Schedule(context.Background(), event, release, []Route{{RouteObservationID: "route-separate-" + string(rune('a'+i)), Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeShadow}, Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile()}}}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		decisions, _, err := repository.ListDecisions(context.Background(), 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) == 1 {
			evaluations, evalErr := repository.RouteEvaluationsForDecision(context.Background(), decisions[0].ID)
			if evalErr != nil {
				t.Fatal(evalErr)
			}
			if len(evaluations) == 10 && fake.CallCount() == 1 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	decisions, _, _ := repository.ListDecisions(context.Background(), 10, nil)
	if len(decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(decisions))
	}
	evaluations, _ := repository.RouteEvaluationsForDecision(context.Background(), decisions[0].ID)
	t.Fatalf("provider calls=%d route evaluations=%d, want 1/10", fake.CallCount(), len(evaluations))
}

func TestOffRoutesDoNotCallProvider(t *testing.T) {
	repository := shadowRepository(t)
	fake := &provider.FakeOpenAICompatibleProvider{}
	runtime := New(Config{Repository: repository, Provider: fake, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	routes := []Route{{RouteObservationID: "route-off", Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeOff}}}
	if err := runtime.Schedule(context.Background(), shadowEvent("event-off"), shadowRelease(), routes); err != nil {
		t.Fatal(err)
	}
	waitShadow(t, runtime)
	if fake.CallCount() != 0 {
		t.Fatalf("provider calls = %d, want 0", fake.CallCount())
	}
	evaluation, err := repository.RouteEvaluation(context.Background(), "route-off")
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.DecisionID != "" || evaluation.EffectiveAction != "pass" || evaluation.HardPassReason == "" {
		t.Fatalf("off evaluation = %#v", evaluation)
	}
}

func TestUnconfiguredProviderRecordsFailOpenRouteEvaluation(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: t.TempDir() + "/platform.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := platformdb.NewAIRepository(store)
	runtime := New(Config{Repository: repository, Provider: nil, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	route := Route{RouteObservationID: "route-provider-unconfigured", Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeShadow}, Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile()}}
	if err := runtime.Schedule(context.Background(), shadowEvent("event-provider-unconfigured"), classifier.Release{}, []Route{route}); err != nil {
		t.Fatalf("Schedule() returned %v; an absent provider must remain fail-open", err)
	}
	waitShadow(t, runtime)
	if got := runtime.Stats().ProviderCalls; got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	evaluation, err := repository.RouteEvaluation(context.Background(), route.RouteObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.DecisionID != "" || evaluation.EffectiveAction != "pass" || evaluation.HardPassReason != "provider_unavailable" {
		t.Fatalf("unconfigured provider evaluation = %#v", evaluation)
	}
}

func TestUnsupportedObservedPlatformRecordsSafeRouteEvaluation(t *testing.T) {
	repository := shadowRepository(t)
	runtime := New(Config{Repository: repository, Provider: &provider.FakeOpenAICompatibleProvider{}, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	observed := platformdb.ObservedEventRecord{ID: "obs-unsupported", Platform: "weibo", SourceExternalID: "source", EventType: "post", ObservedAt: time.Unix(1700000000, 0).UTC(), PublicSnapshotJSON: `{"platform":"weibo","text":"public"}`}
	route := Route{RouteObservationID: "route-unsupported", Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeShadow}, Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile()}}
	if err := runtime.ScheduleObserved(context.Background(), observed, []Route{route}); !errors.Is(err, normalizer.ErrUnsupportedPlatform) {
		t.Fatalf("ScheduleObserved() error = %v, want unsupported platform", err)
	}
	evaluation, err := repository.RouteEvaluation(context.Background(), route.RouteObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.EffectiveAction != "pass" || evaluation.HardPassReason != "normalization_error" {
		t.Fatalf("unsupported platform evaluation = %#v", evaluation)
	}
	if got := runtime.Stats().ProviderCalls; got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestProviderErrorIsFailOpenAndDuplicateDoesNotRetry(t *testing.T) {
	repository := shadowRepository(t)
	fake := &provider.FakeOpenAICompatibleProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		return classifier.Classification{}, classifier.Usage{}, errors.New("provider down")
	}}
	runtime := New(Config{Repository: repository, Provider: fake, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	job := shadowEvent("event-error")
	routes := []Route{{RouteObservationID: "route-error", Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeShadow}}}
	if err := runtime.Schedule(context.Background(), job, shadowRelease(), routes); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Schedule(context.Background(), job, shadowRelease(), routes); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && fake.CallCount() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", fake.CallCount())
	}
	decisions, _, err := repository.ListDecisions(context.Background(), 10, nil)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %#v, err=%v", decisions, err)
	}
	if decisions[0].Status != classifier.StatusProviderError || decisions[0].EffectiveAction != "pass" {
		t.Fatalf("decision = %#v", decisions[0])
	}
}

func TestScheduleIsNonBlockingWhileProviderIsBusy(t *testing.T) {
	repository := shadowRepository(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &provider.FakeOpenAICompatibleProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		close(started)
		<-release
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryUpdate, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
	}}
	runtime := New(Config{Repository: repository, Provider: fake, Now: func() time.Time { return time.Unix(1700000010, 0).UTC() }})
	defer runtime.Close(context.Background())
	begin := time.Now()
	if err := runtime.Schedule(context.Background(), shadowEvent("event-busy"), shadowRelease(), []Route{{RouteObservationID: "route-busy", Eligible: true, Mode: policy.ModeResolution{Mode: policy.ModeShadow}}}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > 250*time.Millisecond {
		t.Fatalf("Schedule blocked for %s", elapsed)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	close(release)
}
