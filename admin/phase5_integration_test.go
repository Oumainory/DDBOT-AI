package admin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/enforce"
	"github.com/Oumainory/DDBOT-AI/internal/observation"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
	"github.com/Oumainory/DDBOT-AI/lsp/mmsg"
)

func bridgeTestRelease() classifier.Release {
	return classifier.Release{ID: "release-bridge", Fingerprint: "fingerprint-bridge", ProviderType: "openai-compatible", BaseURL: "https://provider.test/v1", Model: "model", PromptVersion: classifier.PromptVersion, PromptDigest: classifier.PromptDigest(), SchemaVersion: classifier.ClassificationSchemaVersion, SchemaDigest: classifier.SchemaDigest(), StructuredOutputMode: "json_schema", PreprocessorVersion: "text-v1", NormalizerVersion: "bilibili-v1", CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func TestPhase5ProductionBridgeUsesDurableFiveLayerPolicyIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "phase5-bridge.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	domainRepository := platformdb.NewDomainRepository(store)
	legacy := []domain.LegacySubscription{{
		Platform: "bilibili", ExternalID: "source-external-42", DisplayName: "source", SubscriptionType: "dynamic",
		TargetType: "group", TargetExternalID: "777", TargetDisplayName: "group", Enabled: true,
		LegacyKey: "bilibili:source-external-42:dynamic|group:777",
	}}
	if err := domainRepository.RebuildProjection(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	source, err := domainRepository.SourceByPlatformExternal(ctx, "bilibili", "source-external-42")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := domainRepository.ListProjections(ctx)
	if err != nil || len(projection) != 1 {
		t.Fatalf("projection = %#v, err=%v", projection, err)
	}
	target, err := domainRepository.Target(ctx, projection[0].TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if source.ID == source.ExternalID || target.ID == target.ExternalID || projection[0].ID == projection[0].LegacyKey {
		t.Fatalf("test did not establish durable identities: source=%#v target=%#v projection=%#v", source, target, projection[0])
	}

	aiRepository := platformdb.NewAIRepository(store)
	at := time.Unix(1700000000, 0).UTC()
	release := bridgeTestRelease()
	release.ID, release.Fingerprint, release.Active = "release-bridge", "fingerprint-bridge", true
	if err := aiRepository.SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	// Populate every durable overlay row. The bridge must resolve all five
	// scopes even though only the global/subscription rows select ENFORCE.
	for _, value := range []platformdb.AIPolicyOverrideRecord{
		{ID: "policy-system", ScopeType: "system", ScopeID: "", Mode: policy.ModeInherit, CreatedAt: at},
		{ID: "policy-global", ScopeType: "global", ScopeID: "", Mode: policy.ModeEnforce, CreatedAt: at},
		{ID: "policy-source", ScopeType: "source", ScopeID: source.ID, Mode: policy.ModeInherit, CreatedAt: at},
		{ID: "policy-target", ScopeType: "target", ScopeID: target.ID, Mode: policy.ModeInherit, CreatedAt: at},
		{ID: "policy-subscription", ScopeType: "subscription", ScopeID: projection[0].ID, Mode: policy.ModeEnforce, CreatedAt: at},
	} {
		if err := aiRepository.SavePolicy(ctx, value, at); err != nil {
			t.Fatalf("save %s policy: %v", value.ScopeType, err)
		}
	}
	mode, policyContext, ok := resolvePhase5Layers(ctx, aiRepository, source.ID, target.ID, projection[0].ID)
	if !ok || mode != policy.ModeEnforce || policyContext.Profile.ID != policy.OfficialGameProfile().ID {
		t.Fatalf("resolved five-layer context mode=%s profile=%#v ok=%v", mode, policyContext.Profile, ok)
	}
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: policyContext.Threshold, CategoryActions: policyContext.Profile.CategoryActions, TagActions: policyContext.Profile.TagActions}
	approval := platformdb.EnforceApprovalRecord{ID: "approval-bridge", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(policyContext.Profile), ReadinessEvidenceJSON: []byte(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "integration", CreatedAt: at}
	phase5Repository := platformdb.NewPhase5Repository(store)
	if err := phase5Repository.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := phase5Repository.ValidEnforceApproval(ctx, release.ID, approval.PolicyDigest, approval.ProfileDigest, at); err != nil {
		t.Fatalf("bridge approval did not validate before evaluation: %v", err)
	}
	fakeProvider := &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
	}}
	runtime := enforce.New(enforce.Config{
		AIRepository: aiRepository,
		Repository:   phase5Repository,
		Provider:     fakeProvider,
		Readiness: func(context.Context, classifier.Release) (bool, error) {
			return true, nil
		},
		Now: func() time.Time { return at },
	})
	defer runtime.Close()
	recorder := observation.NewRecorder(platformdb.NewObservationRepository(store), observation.Config{})
	defer recorder.Close(context.Background())
	eventTrace, accepted := recorder.TryObserveEvent(observation.EventInput{
		Platform: "bilibili", SourceKind: "account", SourceExternalID: "source-external-42", UpstreamEventID: "event-bridge-1",
		EventType: "dynamic", ObservedAt: at, PublicText: "important public update", PublicURL: "https://example.test/event-bridge-1",
	})
	if !accepted {
		t.Fatal("event was not accepted by the production observation boundary")
	}
	routeTrace, accepted := recorder.TryObserveRoute(eventTrace, observation.RouteInput{RouteOrdinal: 0, DestinationKind: "group", DestinationExternalID: "777", Outcome: "pass", ReasonCode: "legacy_pass", ObservedAt: at})
	if !accepted {
		t.Fatal("route was not accepted by the production observation boundary")
	}
	drop, reason, err := evaluateEnforceRoute(ctx, nil, mmsg.NewGroupTarget(777), routeTrace, platformdb.NewObservationRepository(store), domainRepository, aiRepository, runtime)
	if err != nil || !drop {
		t.Fatalf("production bridge drop=%v reason=%q err=%v, want durable overlay to authorize DROP", drop, reason, err)
	}
	decisions, _, err := phase5Repository.ListRouteDecisions(ctx, 20, nil, "")
	if err != nil || len(decisions) != 1 {
		t.Fatalf("durable route decisions = %#v, err=%v", decisions, err)
	}
	if decisions[0].SourceID != source.ID || decisions[0].TargetID != target.ID || decisions[0].SubscriptionID != projection[0].ID {
		t.Fatalf("production route identities source=%q target=%q subscription=%q, want durable IDs %q/%q/%q", decisions[0].SourceID, decisions[0].TargetID, decisions[0].SubscriptionID, source.ID, target.ID, projection[0].ID)
	}
}

func TestPhase5ProductionBridgePolicyPrecedenceUsesDurableScopes(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "phase5-precedence.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	domains := platformdb.NewDomainRepository(store)
	legacy := []domain.LegacySubscription{{Platform: "bilibili", ExternalID: "precedence-source", DisplayName: "source", SubscriptionType: "dynamic", TargetType: "group", TargetExternalID: "778", TargetDisplayName: "group", Enabled: true, LegacyKey: "legacy-precedence"}}
	if err := domains.RebuildProjection(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	source, err := domains.SourceByPlatformExternal(ctx, "bilibili", "precedence-source")
	if err != nil {
		t.Fatal(err)
	}
	projections, err := domains.ListProjections(ctx)
	if err != nil || len(projections) != 1 {
		t.Fatalf("projections=%#v err=%v", projections, err)
	}
	target, err := domains.Target(ctx, projections[0].TargetID)
	if err != nil {
		t.Fatal(err)
	}
	ai := platformdb.NewAIRepository(store)
	phase5 := platformdb.NewPhase5Repository(store)
	at := time.Unix(1700000000, 0).UTC()
	release := bridgeTestRelease()
	release.ID, release.Fingerprint, release.Active = "release-precedence", "fingerprint-precedence", true
	if err := ai.SaveRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	// Global selects ENFORCE and DROP, but Target overrides the same category
	// to PASS. The production bridge must resolve this exact five-layer order.
	policies := []platformdb.AIPolicyOverrideRecord{
		{ID: "precedence-system", ScopeType: "system", Mode: policy.ModeInherit, CreatedAt: at},
		{ID: "precedence-global", ScopeType: "global", Mode: policy.ModeEnforce, CategoryActions: map[domain.Category]policy.Action{domain.CategoryPromotion: policy.ActionDrop}, CreatedAt: at},
		{ID: "precedence-source", ScopeType: "source", ScopeID: source.ID, Mode: policy.ModeInherit, CreatedAt: at},
		{ID: "precedence-target", ScopeType: "target", ScopeID: target.ID, Mode: policy.ModeInherit, CategoryActions: map[domain.Category]policy.Action{domain.CategoryPromotion: policy.ActionPass}, CreatedAt: at},
		{ID: "precedence-subscription", ScopeType: "subscription", ScopeID: projections[0].ID, Mode: policy.ModeInherit, CreatedAt: at},
	}
	for _, value := range policies {
		if err := ai.SavePolicy(ctx, value, at); err != nil {
			t.Fatalf("save %s policy: %v", value.ScopeType, err)
		}
	}
	mode, resolved, ok := resolvePhase5Layers(ctx, ai, source.ID, target.ID, projections[0].ID)
	if !ok || mode != policy.ModeEnforce || resolved.Profile.ID == "" {
		t.Fatalf("resolved policy mode=%s context=%#v ok=%v", mode, resolved, ok)
	}
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: resolved.Threshold, CategoryActions: resolved.Profile.CategoryActions, TagActions: resolved.Profile.TagActions}
	approval := platformdb.EnforceApprovalRecord{ID: "approval-precedence", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(resolved.Profile), ReadinessEvidenceJSON: []byte(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "integration", CreatedAt: at}
	if err := phase5.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	runtime := enforce.New(enforce.Config{
		AIRepository: ai, Repository: phase5, Provider: &provider.FakeProvider{ClassifyFunc: func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
			return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: .99}, classifier.Usage{}, nil
		}},
		Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }, Now: func() time.Time { return at },
	})
	defer runtime.Close()
	recorder := observation.NewRecorder(platformdb.NewObservationRepository(store), observation.Config{})
	defer recorder.Close(context.Background())
	eventTrace, accepted := recorder.TryObserveEvent(observation.EventInput{Platform: "bilibili", SourceKind: "account", SourceExternalID: "precedence-source", UpstreamEventID: "event-precedence", EventType: "dynamic", ObservedAt: at, PublicText: "public", PublicURL: "https://example.test/precedence"})
	if !accepted {
		t.Fatal("event was not accepted")
	}
	routeTrace, accepted := recorder.TryObserveRoute(eventTrace, observation.RouteInput{RouteOrdinal: 0, DestinationKind: "group", DestinationExternalID: "778", Outcome: "pass", ReasonCode: "legacy_pass", ObservedAt: at})
	if !accepted {
		t.Fatal("route was not accepted")
	}
	drop, reason, err := evaluateEnforceRoute(ctx, nil, mmsg.NewGroupTarget(778), routeTrace, platformdb.NewObservationRepository(store), domains, ai, runtime)
	if err != nil || drop {
		t.Fatalf("target PASS precedence drop=%v reason=%q err=%v", drop, reason, err)
	}

	// Now let the most-specific Subscription layer select DROP while Global
	// selects PASS. The same production bridge must honor the subscription
	// durable projection ID and persist a DROP with durable route identities.
	for _, value := range []platformdb.AIPolicyOverrideRecord{
		{ID: "precedence-global", ScopeType: "global", Mode: policy.ModeEnforce, CategoryActions: map[domain.Category]policy.Action{domain.CategoryPromotion: policy.ActionPass}, CreatedAt: at},
		{ID: "precedence-target", ScopeType: "target", ScopeID: target.ID, Mode: policy.ModeInherit, CreatedAt: at},
		{ID: "precedence-subscription", ScopeType: "subscription", ScopeID: projections[0].ID, Mode: policy.ModeInherit, CategoryActions: map[domain.Category]policy.Action{domain.CategoryPromotion: policy.ActionDrop}, CreatedAt: at},
	} {
		if err := ai.SavePolicy(ctx, value, at.Add(time.Second)); err != nil {
			t.Fatalf("save changed %s policy: %v", value.ScopeType, err)
		}
	}
	mode, resolved, ok = resolvePhase5Layers(ctx, ai, source.ID, target.ID, projections[0].ID)
	if !ok || mode != policy.ModeEnforce {
		t.Fatalf("resolved subscription policy mode=%s context=%#v ok=%v", mode, resolved, ok)
	}
	effective = policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: resolved.Threshold, CategoryActions: resolved.Profile.CategoryActions, TagActions: resolved.Profile.TagActions}
	approval = platformdb.EnforceApprovalRecord{ID: "approval-subscription-drop", ClassifierReleaseID: release.ID, PolicyDigest: policy.Digest(effective), ProfileDigest: policy.ProfileDigest(resolved.Profile), ReadinessEvidenceJSON: []byte(`{"ready":true}`), ApprovedAt: at, ApprovedBy: "integration", CreatedAt: at}
	if err := phase5.SaveEnforceApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	eventTrace, accepted = recorder.TryObserveEvent(observation.EventInput{Platform: "bilibili", SourceKind: "account", SourceExternalID: "precedence-source", UpstreamEventID: "event-subscription-drop", EventType: "dynamic", ObservedAt: at.Add(3 * time.Second), PublicText: "public", PublicURL: "https://example.test/subscription-drop"})
	if !accepted {
		t.Fatal("second event was not accepted")
	}
	routeTrace, accepted = recorder.TryObserveRoute(eventTrace, observation.RouteInput{RouteOrdinal: 0, DestinationKind: "group", DestinationExternalID: "778", Outcome: "pass", ReasonCode: "legacy_pass", ObservedAt: at.Add(3 * time.Second)})
	if !accepted {
		t.Fatal("second route was not accepted")
	}
	drop, reason, err = evaluateEnforceRoute(ctx, nil, mmsg.NewGroupTarget(778), routeTrace, platformdb.NewObservationRepository(store), domains, ai, runtime)
	if err != nil || !drop {
		t.Fatalf("subscription DROP precedence drop=%v reason=%q err=%v", drop, reason, err)
	}
	decisions, _, err := phase5.ListRouteDecisions(ctx, 20, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if decision.RouteObservationID == routeTrace.RouteObservationID() {
			found = true
			if decision.SourceID != source.ID || decision.TargetID != target.ID || decision.SubscriptionID != projections[0].ID {
				t.Fatalf("subscription DROP identities=%#v", decision)
			}
		}
	}
	if !found {
		t.Fatal("subscription DROP route decision was not persisted")
	}
}

func TestPhase5ProductionBridgeMissingIdentityFailsOpen(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "phase5-missing-identity.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	domains := platformdb.NewDomainRepository(store)
	ai := platformdb.NewAIRepository(store)
	phase5 := platformdb.NewPhase5Repository(store)
	if err := domains.RebuildProjection(ctx, []domain.LegacySubscription{{Platform: "bilibili", ExternalID: "seed-source", DisplayName: "seed", SubscriptionType: "dynamic", TargetType: "group", TargetExternalID: "779", TargetDisplayName: "group", Enabled: true, LegacyKey: "seed-legacy"}}); err != nil {
		t.Fatal(err)
	}
	runtime := enforce.New(enforce.Config{AIRepository: ai, Repository: phase5, Provider: &provider.FakeProvider{}, Readiness: func(context.Context, classifier.Release) (bool, error) { return true, nil }})
	defer runtime.Close()
	at := time.Unix(1700000000, 0).UTC()
	recorder := observation.NewRecorder(platformdb.NewObservationRepository(store), observation.Config{})
	defer recorder.Close(context.Background())
	makeTrace := func(name, sourceExternalID, targetExternalID string) observation.RouteTrace {
		event, ok := recorder.TryObserveEvent(observation.EventInput{Platform: "bilibili", SourceKind: "account", SourceExternalID: sourceExternalID, UpstreamEventID: name, EventType: "dynamic", ObservedAt: at, PublicText: "public", PublicURL: "https://example.test/" + name})
		if !ok {
			t.Fatalf("event %s was not accepted", name)
		}
		route, ok := recorder.TryObserveRoute(event, observation.RouteInput{RouteOrdinal: 0, DestinationKind: "group", DestinationExternalID: targetExternalID, Outcome: "pass", ReasonCode: "legacy_pass", ObservedAt: at})
		if !ok {
			t.Fatalf("route %s was not accepted", name)
		}
		return route
	}
	assertPass := func(name string, group int64, trace observation.RouteTrace) {
		drop, reason, err := evaluateEnforceRoute(ctx, nil, mmsg.NewGroupTarget(group), trace, platformdb.NewObservationRepository(store), domains, ai, runtime)
		if err != nil || drop || reason != "ambiguous_route" {
			t.Fatalf("%s drop=%v reason=%q err=%v, want fail-open ambiguous_route", name, drop, reason, err)
		}
	}
	// A target is required before the bridge can resolve the source. Without a
	// matching source, the route must not fall back to a global DROP.
	assertPass("missing source", 779, makeTrace("missing-source-event", "missing-source", "779"))
	// A source without a target is also ambiguous and must fail open.
	sourceMissingTargetID, _ := domain.NewID()
	if _, err := domains.CreateSource(ctx, domain.Source{ID: sourceMissingTargetID, Platform: domain.PlatformBilibili, ExternalID: "missing-target-source", DisplayName: "source", Status: domain.SourceActive}); err != nil {
		t.Fatal(err)
	}
	trace := makeTrace("missing-target-event", "missing-target-source", "780")
	drop, reason, err := evaluateEnforceRoute(ctx, nil, mmsg.NewGroupTarget(780), trace, platformdb.NewObservationRepository(store), domains, ai, runtime)
	if err != nil || drop || reason != "ambiguous_route" {
		t.Fatalf("missing target drop=%v reason=%q err=%v", drop, reason, err)
	}
	// A source and target with no active subscription projection cannot safely
	// select Subscription policy and therefore cannot inherit a global DROP.
	sourceMissingSubscriptionID, _ := domain.NewID()
	if _, err := domains.CreateSource(ctx, domain.Source{ID: sourceMissingSubscriptionID, Platform: domain.PlatformBilibili, ExternalID: "missing-subscription-source", DisplayName: "source", Status: domain.SourceActive}); err != nil {
		t.Fatal(err)
	}
	trace = makeTrace("missing-subscription-event", "missing-subscription-source", "779")
	assertPass("missing subscription", 779, trace)
}
