package enforce

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
	"github.com/Oumainory/DDBOT-AI/internal/replay"
)

func TestEnforceDecisionRecoveryFailureFailsOpen(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-recovery-failure.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := platformdb.NewPhase5Repository(store)
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		t.Fatal("provider call reached while decision recovery is unavailable")
		return classifier.Classification{}, classifier.Usage{}, nil
	}}
	runtime := New(Config{Repository: repo, Provider: fake, DecisionRecoveryError: errors.New("recovery failed")})
	ready, readyErr := runtime.Ready(context.Background(), enforceRelease())
	if ready || !errors.Is(readyErr, platformdb.ErrEnforceNotReady) {
		t.Fatalf("recovery failure readiness = ready=%v err=%v", ready, readyErr)
	}
	decisions, evalErr := runtime.Evaluate(context.Background(), enforceEvent(), []Route{{
		Mode:   policy.ModeEnforce,
		Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile(), Threshold: .90},
		Target: Target{ID: "target-recovery", Type: "group", ExternalID: "123", ConnectorID: "connector-recovery"},
	}})
	if evalErr != nil || len(decisions) != 1 || decisions[0].Action != policy.RoutePass || decisions[0].Reason != "enforce_recovery_unavailable" {
		t.Fatalf("recovery failure evaluation = %#v err=%v", decisions, evalErr)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("provider calls = %d, want zero", fake.CallCount())
	}
}

func enforceEvent() domain.NormalizedEvent {
	at := time.Unix(1700000000, 0).UTC()
	return domain.NormalizedEvent{ID: "event-enforce-1", NormalizedEventID: "event-enforce-1", ObservedEventID: "observed-enforce-1", SchemaVersion: 1, Platform: domain.PlatformBilibili, SourceID: "source-1", ExternalID: "upstream-1", EventType: domain.EventDynamic, Title: "routine", Body: "public body", NormalizerVersion: "normalizer-v1", PreprocessorVersion: "text-v1", ObservedAt: at, CreatedAt: at, ReplayPayload: json.RawMessage(`{"public":true}`)}
}

func enforceRelease() classifier.Release {
	return classifier.Release{ID: "release-enforce-1", Fingerprint: "fingerprint-enforce-1", ProviderType: "openai-compatible", BaseURL: "https://provider.example.test/v1", Model: "model", PromptVersion: "prompt-v1", PromptDigest: "prompt-digest", SchemaVersion: "schema-v1", SchemaDigest: "schema-digest", StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "normalizer-v1", PricingCurrency: "USD", CreatedAt: time.Unix(1700000000, 0).UTC(), Active: true}
}

func TestEnforceRuntimeDropsOnlyAfterDurableApprovalAndReplaySnapshot(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce.sqlite"), Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	release := enforceRelease()
	if err := ai.SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	profile := policy.OfficialGameProfile()
	ctx := policy.PolicyContext{Profile: profile, Threshold: .90}
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: .90, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}
	if err := repo.SaveEnforceApproval(context.Background(), platformdb.EnforceApprovalRecord{ID: "approval-1", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(profile), ReadinessEvidenceJSON: json.RawMessage(`{"real_reviewed":true}`), ApprovedAt: time.Unix(1700000001, 0).UTC(), ApprovedBy: "admin", CreatedAt: time.Unix(1700000001, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
	}}
	cached := make(chan replay.Snapshot, 1)
	runtime := New(Config{AIRepository: ai, Repository: repo, Provider: fake, Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }, Now: func() time.Time { return time.Unix(1700000002, 0).UTC() }, CacheMedia: func(_ context.Context, snapshot replay.Snapshot) error {
		cached <- snapshot
		return nil
	}})
	decisions, err := runtime.Evaluate(context.Background(), enforceEvent(), []Route{{Mode: policy.ModeEnforce, Policy: ctx, Release: release, Target: Target{ID: "target-1", Type: "group", ExternalID: "123", ConnectorID: "connector-1"}, SubscriptionID: "sub-1"}})
	if err != nil || len(decisions) != 1 || decisions[0].Action != policy.RouteDrop || !decisions[0].SuppressSend || !decisions[0].Persisted {
		t.Fatalf("decisions = %#v, err=%v", decisions, err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("provider calls = %d, want one", fake.CallCount())
	}
	if _, err := repo.ReplayableEvent(context.Background(), decisions[0].RouteDecisionID); err != nil {
		t.Fatalf("replay snapshot missing: %v", err)
	}
	select {
	case snapshot := <-cached:
		if snapshot.RouteDecisionID != decisions[0].RouteDecisionID || snapshot.EventID != enforceEvent().ID {
			t.Fatalf("cached snapshot identity = %#v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("post-drop media cache callback was not scheduled")
	}
}

func TestEnforceRuntimeSharesClassificationAcrossRoutes(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-shared.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	release := enforceRelease()
	if err := ai.SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	profile := policy.OfficialGameProfile()
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: .90, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}
	if err := repo.SaveEnforceApproval(context.Background(), platformdb.EnforceApprovalRecord{ID: "approval-shared", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(profile), ApprovedAt: time.Now().Add(-time.Minute), ApprovedBy: "admin", CreatedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
	}}
	runtime := New(Config{AIRepository: ai, Repository: repo, Provider: fake, Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }})
	base := Route{Mode: policy.ModeEnforce, Policy: policy.PolicyContext{Profile: profile, Threshold: .90}, Release: release, SubscriptionID: "sub-1"}
	base.Target = Target{ID: "target-1", Type: "group", ExternalID: "1", ConnectorID: "connector-1"}
	routes := []Route{base, base}
	routes[1].Target.ID, routes[1].Target.ExternalID = "target-2", "2"
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = runtime.Evaluate(context.Background(), enforceEvent(), routes) }()
	}
	wg.Wait()
	if fake.CallCount() != 1 {
		t.Fatalf("shared provider calls = %d, want one", fake.CallCount())
	}
}

func TestEnforceKillSwitchForcesPass(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-kill.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	fake := &provider.FakeProvider{}
	runtime := New(Config{AIRepository: ai, Repository: repo, Provider: fake, Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }})
	runtime.SetEmergencyDisabled(true)
	result, err := runtime.Evaluate(context.Background(), enforceEvent(), []Route{{Mode: policy.ModeEnforce, Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile(), Threshold: .90}, Target: Target{ID: "target-1", Type: "group", ExternalID: "1", ConnectorID: "connector-1"}}})
	if err != nil || len(result) != 1 || result[0].Action != policy.RoutePass || result[0].SuppressSend {
		t.Fatalf("kill switch result = %#v, err=%v", result, err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("kill switch provider calls = %d, want zero", fake.CallCount())
	}
}

func TestEnforceProviderPanicFailsOpen(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-panic.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	release := enforceRelease()
	if err := ai.SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	profile := policy.OfficialGameProfile()
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: .90, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}
	if err := repo.SaveEnforceApproval(context.Background(), platformdb.EnforceApprovalRecord{ID: "approval-panic", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(profile), ApprovedAt: time.Unix(1699999999, 0).UTC(), ApprovedBy: "admin", CreatedAt: time.Unix(1699999999, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		panic("provider implementation failure")
	}}
	runtime := New(Config{AIRepository: ai, Repository: repo, Provider: fake, Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }, Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	decisions, err := runtime.Evaluate(context.Background(), enforceEvent(), []Route{{Mode: policy.ModeEnforce, Policy: policy.PolicyContext{Profile: profile, Threshold: .90}, Release: release, Target: Target{ID: "target-1", Type: "group", ExternalID: "123", ConnectorID: "connector-1"}}})
	if err != nil || len(decisions) != 1 || decisions[0].Action != policy.RoutePass || decisions[0].SuppressSend {
		t.Fatalf("panic result = %#v, err=%v", decisions, err)
	}
	decision, err := ai.Decision(context.Background(), decisions[0].AIDecisionID)
	if err != nil || decision.Status == classifier.StatusCompleted || decision.EffectiveAction != "pass" {
		t.Fatalf("panic AI decision = %#v, err=%v", decision, err)
	}
}

func TestEnforceStaleRouteReleaseFailsOpenWithoutProviderCall(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-stale.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	active := enforceRelease()
	active.ID = "release-active"
	stale := enforceRelease()
	stale.ID = "release-stale"
	if err := ai.SaveRelease(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		t.Fatal("stale route reached provider")
		return classifier.Classification{}, classifier.Usage{}, nil
	}}
	runtime := New(Config{AIRepository: ai, Repository: repo, Provider: fake, Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }})
	decisions, err := runtime.Evaluate(context.Background(), enforceEvent(), []Route{{Mode: policy.ModeEnforce, Policy: policy.PolicyContext{Profile: policy.OfficialGameProfile(), Threshold: .90}, Release: stale, Target: Target{ID: "target-1", Type: "group", ExternalID: "123", ConnectorID: "connector-1"}}})
	if err != nil || len(decisions) != 1 || decisions[0].Action != policy.RoutePass || decisions[0].Reason != "classifier_release_stale" {
		t.Fatalf("stale result = %#v, err=%v", decisions, err)
	}
}

func TestEnforceEmergencyDisableDuringProviderFailsOpen(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-emergency-race.sqlite"), Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	release := enforceRelease()
	release.ID = "release-emergency-race"
	release.Fingerprint = "fingerprint-emergency-race"
	release.Active = true
	if err := ai.SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	profile := policy.OfficialGameProfile()
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: .90, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}
	approval := platformdb.EnforceApprovalRecord{ID: "approval-emergency-race", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(profile), ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	providerStarted := make(chan struct{})
	providerRelease := make(chan struct{})
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		close(providerStarted)
		<-providerRelease
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
	}}
	runtime := New(Config{
		AIRepository: ai,
		Repository:   repo,
		Provider:     fake,
		Readiness:    func(context.Context, classifier.Release) (bool, error) { return true, nil },
		Now:          func() time.Time { return at.Add(time.Second) },
	})
	resultCh := make(chan struct {
		decisions []Decision
		err       error
	}, 1)
	go func() {
		decisions, evaluateErr := runtime.Evaluate(ctx, enforceEvent(), []Route{{
			Mode:           policy.ModeEnforce,
			Policy:         policy.PolicyContext{Profile: profile, Threshold: .90},
			Release:        release,
			SourceID:       "source-1",
			SubscriptionID: "subscription-1",
			Target:         Target{ID: "target-1", Type: "group", ExternalID: "123", ConnectorID: "connector-1"},
		}})
		resultCh <- struct {
			decisions []Decision
			err       error
		}{decisions: decisions, err: evaluateErr}
	}()
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider did not enter the in-flight boundary")
	}
	if err := repo.SetEnforceEmergencyDisabled(ctx, true, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	close(providerRelease)
	select {
	case result := <-resultCh:
		if result.err != nil || len(result.decisions) != 1 || result.decisions[0].Action != policy.RoutePass || result.decisions[0].SuppressSend {
			t.Fatalf("emergency race result = %#v, err=%v", result.decisions, result.err)
		}
		if result.decisions[0].RouteDecisionID == "" {
			t.Fatal("fail-open PASS was not durably recorded")
		}
		stored, loadErr := repo.RouteDecision(ctx, result.decisions[0].RouteDecisionID)
		if loadErr != nil || stored.EffectiveAction != "pass" || stored.EffectiveMode != "shadow" {
			t.Fatalf("emergency race durable PASS = %#v, err=%v", stored, loadErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not finish after emergency disable")
	}
}

func TestEnforceApprovalRevokeDuringProviderFailsOpen(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "enforce-approval-race.sqlite"), Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ai := platformdb.NewAIRepository(store)
	repo := platformdb.NewPhase5Repository(store)
	release := enforceRelease()
	release.ID = "release-approval-race"
	release.Fingerprint = "fingerprint-approval-race"
	release.Active = true
	if err := ai.SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	profile := policy.OfficialGameProfile()
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: .90, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}
	approval := platformdb.EnforceApprovalRecord{ID: "approval-revoke-race", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(profile), ReadinessEvidenceJSON: json.RawMessage(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "admin", CreatedAt: at}
	if err := repo.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	providerStarted := make(chan struct{})
	providerRelease := make(chan struct{})
	fake := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		close(providerStarted)
		<-providerRelease
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
	}}
	runtime := New(Config{
		AIRepository: ai,
		Repository:   repo,
		Provider:     fake,
		Readiness:    func(context.Context, classifier.Release) (bool, error) { return true, nil },
		Now:          func() time.Time { return at.Add(time.Second) },
	})
	resultCh := make(chan []Decision, 1)
	go func() {
		decisions, _ := runtime.Evaluate(ctx, enforceEvent(), []Route{{
			Mode:           policy.ModeEnforce,
			Policy:         policy.PolicyContext{Profile: profile, Threshold: .90},
			Release:        release,
			SourceID:       "source-1",
			SubscriptionID: "subscription-1",
			Target:         Target{ID: "target-1", Type: "group", ExternalID: "123", ConnectorID: "connector-1"},
		}})
		resultCh <- decisions
	}()
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider did not enter the in-flight boundary")
	}
	if err := repo.RevokeEnforceApprovals(ctx, "operator review", at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	close(providerRelease)
	select {
	case decisions := <-resultCh:
		if len(decisions) != 1 || decisions[0].Action != policy.RoutePass || decisions[0].SuppressSend {
			t.Fatalf("approval revoke race result = %#v", decisions)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not finish after approval revoke")
	}
}
