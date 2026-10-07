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
