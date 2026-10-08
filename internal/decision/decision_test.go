package decision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
)

func testEvent() domain.NormalizedEvent {
	return domain.NormalizedEvent{
		ID:                "test-event-001",
		NormalizedEventID: "test-event-001",
		SchemaVersion:     1,
		Platform:          domain.PlatformBilibili,
		SourceID:          "source-001",
		ExternalID:        "ext-001",
		EventType:         domain.EventDynamic,
		Title:             "测试标题",
		Body:              "测试正文内容",
		NormalizerVersion: "test",
		CreatedAt:         time.Now().UTC(),
		ReplayPayload:     []byte(`{"test":true}`),
	}
}

func TestNoneProviderAlwaysReturnsUncertain(t *testing.T) {
	p := NewNoneProvider("none-test")
	evidence, err := p.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatalf("NoneProvider returned error: %v", err)
	}
	if !evidence.Uncertain {
		t.Fatal("NoneProvider must return Uncertain=true")
	}
	if evidence.ProviderType != ProviderNone {
		t.Fatalf("expected ProviderNone, got %s", evidence.ProviderType)
	}
	if p.Type() != ProviderNone {
		t.Fatalf("expected Type()=ProviderNone, got %s", p.Type())
	}
	if p.ID() != "none-test" {
		t.Fatalf("expected ID()=none-test, got %s", p.ID())
	}
	if err := p.HealthCheck(context.Background()); err != nil {
		t.Fatalf("NoneProvider HealthCheck must never fail: %v", err)
	}
}

func TestNoneProviderDefaultID(t *testing.T) {
	p := NewNoneProvider("")
	if p.ID() != "none-default" {
		t.Fatalf("expected default ID, got %s", p.ID())
	}
}

func TestRulesProviderMatchesMaintenance(t *testing.T) {
	p := NewRulesProvider("rules-test", BuiltInRules()...)
	event := testEvent()
	event.Title = "服务器维护公告"
	evidence, err := p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("RulesProvider returned error: %v", err)
	}
	if evidence.Uncertain {
		t.Fatal("maintenance rule should match")
	}
	if evidence.SemanticResult.Category != domain.CategoryMaintenance {
		t.Fatalf("expected CategoryMaintenance, got %s", evidence.SemanticResult.Category)
	}
	if evidence.SemanticResult.Importance != domain.ImportanceHigh {
		t.Fatalf("expected ImportanceHigh, got %s", evidence.SemanticResult.Importance)
	}
}

func TestRulesProviderMatchesGiveaway(t *testing.T) {
	p := NewRulesProvider("rules-test", BuiltInRules()...)
	event := testEvent()
	event.Title = "转发抽奖活动"
	evidence, err := p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("RulesProvider returned error: %v", err)
	}
	if evidence.Uncertain {
		t.Fatal("giveaway rule should match")
	}
	if evidence.SemanticResult.Category != domain.CategoryGiveaway {
		t.Fatalf("expected CategoryGiveaway, got %s", evidence.SemanticResult.Category)
	}
	if evidence.SemanticResult.Importance != domain.ImportanceLow {
		t.Fatalf("expected ImportanceLow, got %s", evidence.SemanticResult.Importance)
	}
}

func TestRulesProviderMatchesIncident(t *testing.T) {
	p := NewRulesProvider("rules-test", BuiltInRules()...)
	event := testEvent()
	event.Title = "紧急故障通知"
	evidence, err := p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("RulesProvider returned error: %v", err)
	}
	if evidence.Uncertain {
		t.Fatal("incident rule should match")
	}
	if evidence.SemanticResult.Category != domain.CategoryIncident {
		t.Fatalf("expected CategoryIncident, got %s", evidence.SemanticResult.Category)
	}
	if evidence.SemanticResult.Importance != domain.ImportanceCritical {
		t.Fatalf("expected ImportanceCritical, got %s", evidence.SemanticResult.Importance)
	}
}

func TestRulesProviderNoMatchReturnsUncertain(t *testing.T) {
	p := NewRulesProvider("rules-test", BuiltInRules()...)
	event := testEvent()
	event.Title = "今天天气真好"
	event.Body = "没有任何关键词匹配"
	evidence, err := p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("RulesProvider returned error: %v", err)
	}
	if !evidence.Uncertain {
		t.Fatal("no-match should return Uncertain=true")
	}
}

func TestRulesProviderCustomRule(t *testing.T) {
	customRule := func(event domain.NormalizedEvent) *domain.SemanticResult {
		if event.Title == "custom-match" {
			return &domain.SemanticResult{
				SchemaVersion: 1,
				Category:      domain.CategoryAnnouncement,
				Importance:    domain.ImportanceHigh,
				Confidence:    1.0,
			}
		}
		return nil
	}
	p := NewRulesProvider("custom", customRule)
	event := testEvent()
	event.Title = "custom-match"
	evidence, err := p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("custom rule returned error: %v", err)
	}
	if evidence.Uncertain {
		t.Fatal("custom rule should match")
	}
	if evidence.SemanticResult.Category != domain.CategoryAnnouncement {
		t.Fatalf("expected CategoryAnnouncement, got %s", evidence.SemanticResult.Category)
	}
}

func TestRulesProviderAddRule(t *testing.T) {
	p := NewRulesProvider("add-rule-test")
	event := testEvent()
	event.Title = "no-match"
	evidence, err := p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Uncertain {
		t.Fatal("empty rules should return uncertain")
	}

	p.AddRule(func(event domain.NormalizedEvent) *domain.SemanticResult {
		return &domain.SemanticResult{
			SchemaVersion: 1,
			Category:      domain.CategoryOther,
			Importance:    domain.ImportanceLow,
			Confidence:    1.0,
		}
	})
	evidence, err = p.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Uncertain {
		t.Fatal("added rule should match")
	}
}

func TestFirstAvailableStrategyStopsAtFirstMatch(t *testing.T) {
	rules := NewRulesProvider("rules", BuiltInRules()...)
	none := NewNoneProvider("none")

	strategy := NewFirstAvailableStrategy(rules, none)
	event := testEvent()
	event.Title = "服务器维护公告"

	result, err := strategy.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("strategy returned error: %v", err)
	}
	if result.Escalated {
		t.Fatal("strategy should not escalate when first provider matches")
	}
	if result.ProviderCount != 1 {
		t.Fatalf("expected 1 provider, got %d", result.ProviderCount)
	}
	if result.TotalProviders != 1 {
		t.Fatalf("expected 1 total provider consulted, got %d", result.TotalProviders)
	}
	if result.SemanticDecision.Category != domain.CategoryMaintenance {
		t.Fatalf("expected CategoryMaintenance, got %s", result.SemanticDecision.Category)
	}
}

func TestFirstAvailableStrategyEscalatesWhenAllUncertain(t *testing.T) {
	none1 := NewNoneProvider("none-1")
	none2 := NewNoneProvider("none-2")

	strategy := NewFirstAvailableStrategy(none1, none2)
	event := testEvent()

	result, err := strategy.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("strategy returned error: %v", err)
	}
	if !result.Escalated {
		t.Fatal("strategy should escalate when all providers are uncertain")
	}
	if result.ProviderCount != 0 {
		t.Fatalf("expected 0 confident providers, got %d", result.ProviderCount)
	}
	if result.TotalProviders != 2 {
		t.Fatalf("expected 2 total providers, got %d", result.TotalProviders)
	}
}

func TestFirstAvailableStrategyEmptyProviders(t *testing.T) {
	strategy := NewFirstAvailableStrategy()
	_, err := strategy.Evaluate(context.Background(), testEvent())
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("expected ErrNoProvider, got %v", err)
	}
}

func TestFirstAvailableStrategyNilProvider(t *testing.T) {
	rules := NewRulesProvider("rules", BuiltInRules()...)
	strategy := NewFirstAvailableStrategy(nil, rules)
	event := testEvent()
	event.Title = "服务器维护公告"

	result, err := strategy.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatalf("strategy returned error: %v", err)
	}
	if result.Escalated {
		t.Fatal("strategy should skip nil provider and use next")
	}
}

func TestFirstAvailableStrategyMode(t *testing.T) {
	strategy := NewFirstAvailableStrategy()
	if strategy.Mode() != StrategyFirstAvailable {
		t.Fatalf("expected StrategyFirstAvailable, got %s", strategy.Mode())
	}
}

func TestLLMProviderDelegatesToUnderlyingProvider(t *testing.T) {
	fake := &fakeDecisionProvider{
		classifyFunc: func(ctx context.Context, event domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
			return classifier.Classification{
				SchemaVersion: 1,
				Category:      domain.CategoryAnnouncement,
				Importance:    domain.ImportanceHigh,
				Confidence:    0.95,
			}, classifier.Usage{}, nil
		},
	}
	p := NewLLMProvider("llm-test", fake)
	evidence, err := p.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatalf("LLMProvider returned error: %v", err)
	}
	if evidence.Uncertain {
		t.Fatal("LLMProvider should not be uncertain on success")
	}
	if evidence.ProviderType != ProviderLLM {
		t.Fatalf("expected ProviderLLM, got %s", evidence.ProviderType)
	}
	if evidence.SemanticResult.Category != domain.CategoryAnnouncement {
		t.Fatalf("expected CategoryAnnouncement, got %s", evidence.SemanticResult.Category)
	}
}

func TestLLMProviderErrorReturnsUncertain(t *testing.T) {
	fake := &fakeDecisionProvider{
		classifyErr: errors.New("provider down"),
	}
	p := NewLLMProvider("llm-test", fake)
	evidence, err := p.Evaluate(context.Background(), testEvent())
	if err == nil {
		t.Fatal("expected error from LLMProvider")
	}
	if !evidence.Uncertain {
		t.Fatal("LLMProvider must return Uncertain on error")
	}
}

func TestLLMProviderNilProvider(t *testing.T) {
	p := NewLLMProvider("llm-test", nil)
	evidence, err := p.Evaluate(context.Background(), testEvent())
	if err == nil {
		t.Fatal("expected error from nil provider")
	}
	if !evidence.Uncertain {
		t.Fatal("nil provider must return Uncertain")
	}
}

func TestLLMProviderHealthCheck(t *testing.T) {
	fake := &fakeDecisionProvider{}
	p := NewLLMProvider("llm-test", fake)
	if err := p.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck should pass: %v", err)
	}

	pNil := NewLLMProvider("llm-nil", nil)
	if err := pNil.HealthCheck(context.Background()); err == nil {
		t.Fatal("HealthCheck should fail for nil provider")
	}
}

func TestLLMProviderTypeAndID(t *testing.T) {
	p := NewLLMProvider("llm-custom", &fakeDecisionProvider{})
	if p.Type() != ProviderLLM {
		t.Fatalf("expected ProviderLLM, got %s", p.Type())
	}
	if p.ID() != "llm-custom" {
		t.Fatalf("expected llm-custom, got %s", p.ID())
	}
}

func TestLLMProviderDefaultID(t *testing.T) {
	p := NewLLMProvider("", &fakeDecisionProvider{})
	if p.ID() != "llm-default" {
		t.Fatalf("expected llm-default, got %s", p.ID())
	}
}

func TestStrategyResultWithCascade(t *testing.T) {
	rules := NewRulesProvider("rules")
	none := NewNoneProvider("none")

	strategy := NewFirstAvailableStrategy(rules, none)
	event := testEvent()
	event.Title = "今天天气真好"

	result, err := strategy.Evaluate(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Escalated {
		t.Fatal("no-match should escalate")
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("expected 2 evidence entries, got %d", len(result.Evidence))
	}
	if result.Mode != StrategyFirstAvailable {
		t.Fatalf("expected StrategyFirstAvailable, got %s", result.Mode)
	}
}

// fakeDecisionProvider implements provider.Provider for testing LLMProvider.
type fakeDecisionProvider struct {
	classifyFunc func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error)
	classifyErr  error
	testResult   provider.TestResult
	testErr      error
}

func (f *fakeDecisionProvider) Classify(ctx context.Context, event domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
	if f.classifyFunc != nil {
		return f.classifyFunc(ctx, event)
	}
	if f.classifyErr != nil {
		return classifier.Classification{}, classifier.Usage{}, f.classifyErr
	}
	return classifier.Classification{
		SchemaVersion: 1,
		Category:      domain.CategoryUnknown,
		Importance:    domain.ImportanceLow,
		Confidence:    0,
		Uncertain:     true,
	}, classifier.Usage{}, nil
}

func (f *fakeDecisionProvider) TestConnection(ctx context.Context) (provider.TestResult, error) {
	return f.testResult, f.testErr
}

// fakeProvider is a minimal DecisionProvider for strategy tests.
type fakeProvider struct {
	id       string
	ptype    ProviderType
	result   domain.SemanticResult
	uncertain bool
	err      error
}

func (p *fakeProvider) Evaluate(_ context.Context, _ domain.NormalizedEvent) (DecisionEvidence, error) {
	return DecisionEvidence{
		ProviderType:        p.ptype,
		ProviderID:          p.id,
		SemanticResult:      p.result,
		ProviderConfidence:  p.result.Confidence,
		Uncertain:           p.uncertain,
		InsufficientContext: p.uncertain,
		Error:               p.err,
	}, p.err
}

func (p *fakeProvider) Type() ProviderType { return p.ptype }
func (p *fakeProvider) ID() string         { return p.id }
func (p *fakeProvider) HealthCheck(_ context.Context) error { return nil }

func TestMajorityStrategyClearWinner(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.9}}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.85}}
	p3 := &fakeProvider{id: "p3", ptype: ProviderLLM, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryUpdate, Importance: domain.ImportanceMedium, Confidence: 0.7}}

	strategy := NewMajorityStrategy(p1, p2, p3)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if result.Escalated {
		t.Fatal("majority should not escalate with clear winner")
	}
	if result.SemanticDecision.Category != domain.CategoryMaintenance {
		t.Fatalf("expected CategoryMaintenance, got %s", result.SemanticDecision.Category)
	}
	if result.SemanticDecision.Importance != domain.ImportanceHigh {
		t.Fatalf("expected ImportanceHigh, got %s", result.SemanticDecision.Importance)
	}
	if result.ProviderCount != 3 {
		t.Fatalf("expected 3 confident providers, got %d", result.ProviderCount)
	}
}

func TestMajorityStrategyTieEscalates(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.9}}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryUpdate, Importance: domain.ImportanceMedium, Confidence: 0.85}}

	strategy := NewMajorityStrategy(p1, p2)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Escalated {
		t.Fatal("tie should escalate")
	}
}

func TestMajorityStrategyWithUncertainProviders(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.9}}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, uncertain: true}
	p3 := &fakeProvider{id: "p3", ptype: ProviderLLM, uncertain: true}

	strategy := NewMajorityStrategy(p1, p2, p3)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if result.Escalated {
		t.Fatal("should not escalate when one confident provider exceeds threshold")
	}
	if result.ProviderCount != 1 {
		t.Fatalf("expected 1 confident provider, got %d", result.ProviderCount)
	}
}

func TestMajorityStrategyAllUncertainEscalates(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, uncertain: true}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, uncertain: true}

	strategy := NewMajorityStrategy(p1, p2)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Escalated {
		t.Fatal("all uncertain should escalate")
	}
}

func TestMajorityStrategyMode(t *testing.T) {
	strategy := NewMajorityStrategy()
	if strategy.Mode() != StrategyMajority {
		t.Fatalf("expected StrategyMajority, got %s", strategy.Mode())
	}
}

func TestWeightedStrategyHigherWeightWins(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.9}}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryUpdate, Importance: domain.ImportanceMedium, Confidence: 0.85}}

	weights := map[string]float64{"p1": 3.0, "p2": 1.0}
	strategy := NewWeightedStrategy([]DecisionProvider{p1, p2}, weights)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if result.Escalated {
		t.Fatal("weighted should not escalate")
	}
	if result.SemanticDecision.Category != domain.CategoryMaintenance {
		t.Fatalf("expected CategoryMaintenance (higher weight), got %s", result.SemanticDecision.Category)
	}
}

func TestWeightedStrategyDefaultWeight(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.9}}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryUpdate, Importance: domain.ImportanceMedium, Confidence: 0.85}}

	strategy := NewWeightedStrategy([]DecisionProvider{p1, p2}, nil)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if result.Escalated {
		t.Fatal("equal weights should not escalate with 2 providers")
	}
}

func TestWeightedStrategyInsufficientWeightEscalates(t *testing.T) {
	p1 := &fakeProvider{id: "p1", ptype: ProviderRules, result: domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryMaintenance, Importance: domain.ImportanceHigh, Confidence: 0.9}}
	p2 := &fakeProvider{id: "p2", ptype: ProviderLLM, uncertain: true}
	p3 := &fakeProvider{id: "p3", ptype: ProviderLLM, uncertain: true}

	weights := map[string]float64{"p1": 1.0, "p2": 5.0, "p3": 5.0}
	strategy := NewWeightedStrategy([]DecisionProvider{p1, p2, p3}, weights)
	result, err := strategy.Evaluate(context.Background(), testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Escalated {
		t.Fatal("should escalate when confident weight < 50%")
	}
}

func TestWeightedStrategyMode(t *testing.T) {
	strategy := NewWeightedStrategy(nil, nil)
	if strategy.Mode() != StrategyWeighted {
		t.Fatalf("expected StrategyWeighted, got %s", strategy.Mode())
	}
}

func TestWeightedStrategyEmptyProviders(t *testing.T) {
	strategy := NewWeightedStrategy(nil, nil)
	_, err := strategy.Evaluate(context.Background(), testEvent())
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("expected ErrNoProvider, got %v", err)
	}
}
