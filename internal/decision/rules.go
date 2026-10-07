package decision

import (
	"context"
	"strings"
	"sync"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// Rule is a single deterministic classification rule. It inspects an event
// and returns a SemanticResult if the rule matches, or nil if it does not.
type Rule func(event domain.NormalizedEvent) *domain.SemanticResult

// RulesProvider is a deterministic, rule-based Decision Provider. It requires
// no external API, no model, and no network access. Rules are evaluated in
// order; the first matching rule produces the evidence.
//
// RulesProvider is the simplest and cheapest Decision Provider. It is ideal
// for well-known patterns (e.g. maintenance announcements, giveaway posts)
// that do not need an LLM to classify.
type RulesProvider struct {
	id    string
	rules []Rule
	mu    sync.RWMutex
}

// NewRulesProvider creates a RulesProvider with the given stable identifier
// and initial rule set.
func NewRulesProvider(id string, rules ...Rule) *RulesProvider {
	if id == "" {
		id = "rules-default"
	}
	return &RulesProvider{id: id, rules: rules}
}

// AddRule appends a rule to the end of the evaluation chain.
func (p *RulesProvider) AddRule(rule Rule) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = append(p.rules, rule)
}

// SetRules replaces the entire rule set atomically.
func (p *RulesProvider) SetRules(rules []Rule) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = rules
}

func (p *RulesProvider) Evaluate(_ context.Context, event domain.NormalizedEvent) (DecisionEvidence, error) {
	if p == nil {
		return uncertainEvidence(ProviderRules, ""), nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, rule := range p.rules {
		if result := rule(event); result != nil {
			return DecisionEvidence{
				ProviderType:        ProviderRules,
				ProviderID:          p.id,
				SemanticResult:      *result,
				ProviderConfidence:  1.0,
				Uncertain:           false,
				InsufficientContext: false,
			}, nil
		}
	}

	// No rule matched: return uncertain evidence so the Strategy can
	// escalate to the next provider in the pipeline.
	return uncertainEvidence(ProviderRules, p.id), nil
}

func (p *RulesProvider) Type() ProviderType { return ProviderRules }

func (p *RulesProvider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *RulesProvider) HealthCheck(_ context.Context) error { return nil }

// uncertainEvidence returns a valid DecisionEvidence with Uncertain=true.
func uncertainEvidence(pt ProviderType, id string) DecisionEvidence {
	return DecisionEvidence{
		ProviderType: pt,
		ProviderID:   id,
		SemanticResult: domain.SemanticResult{
			SchemaVersion: 1,
			Category:      domain.CategoryUnknown,
			Importance:    domain.ImportanceLow,
			Confidence:    0,
			Uncertain:     true,
		},
		ProviderConfidence:  0,
		Uncertain:           true,
		InsufficientContext: true,
	}
}

// BuiltInRules returns the default set of deterministic classification rules
// that ship with DDBOT-AI. These cover common, well-understood patterns that
// do not require an LLM to classify.
func BuiltInRules() []Rule {
	return []Rule{
		// Maintenance announcements
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			body := strings.ToLower(event.Body)
			title := strings.ToLower(event.Title)
			if strings.Contains(title, "维护") || strings.Contains(title, "maintenance") ||
				strings.Contains(body, "维护公告") || strings.Contains(body, "maintenance notice") ||
				strings.Contains(title, "停机") || strings.Contains(title, "shutdown") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryMaintenance,
					Importance:    domain.ImportanceHigh,
					Tags:          []string{string(domain.TagMaintenance)},
					Confidence:    0.95,
					Summary:       "维护公告",
					Reason:        "规则匹配：维护关键词",
					ReasonCode:    "rule_maintenance_keyword",
				}
			}
			return nil
		},

		// Giveaway / promotion posts
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			body := strings.ToLower(event.Body)
			title := strings.ToLower(event.Title)
			if strings.Contains(title, "抽奖") || strings.Contains(title, "giveaway") ||
				strings.Contains(body, "转发抽奖") || strings.Contains(body, "retweet giveaway") ||
				strings.Contains(title, "福利") || strings.Contains(title, "活动") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryGiveaway,
					Importance:    domain.ImportanceLow,
					Tags:          []string{string(domain.TagGacha)},
					Confidence:    0.90,
					Summary:       "抽奖/福利活动",
					Reason:        "规则匹配：抽奖关键词",
					ReasonCode:    "rule_giveaway_keyword",
				}
			}
			return nil
		},

		// Version update / new content
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			title := strings.ToLower(event.Title)
			if strings.Contains(title, "更新") || strings.Contains(title, "update") ||
				strings.Contains(title, "版本") || strings.Contains(title, "version") ||
				strings.Contains(title, "新角色") || strings.Contains(title, "new character") ||
				strings.Contains(title, "新内容") || strings.Contains(title, "new content") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryUpdate,
					Importance:    domain.ImportanceMedium,
					Tags:          []string{string(domain.TagGameVersion), string(domain.TagNewContent)},
					Confidence:    0.85,
					Summary:       "版本/内容更新",
					Reason:        "规则匹配：更新关键词",
					ReasonCode:    "rule_update_keyword",
				}
			}
			return nil
		},

		// Service outage / incident
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			body := strings.ToLower(event.Body)
			title := strings.ToLower(event.Title)
			if strings.Contains(title, "异常") || strings.Contains(title, "故障") ||
				strings.Contains(title, "outage") || strings.Contains(title, "incident") ||
				strings.Contains(body, "服务中断") || strings.Contains(body, "service disruption") ||
				strings.Contains(title, "紧急") || strings.Contains(title, "urgent") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryIncident,
					Importance:    domain.ImportanceCritical,
					Tags:          []string{string(domain.TagServiceOutage), string(domain.TagBug)},
					Flags:         []string{string(domain.FlagTimeSensitive), string(domain.FlagServiceImpact)},
					Confidence:    0.95,
					Summary:       "服务异常/故障",
					Reason:        "规则匹配：故障关键词",
					ReasonCode:    "rule_incident_keyword",
				}
			}
			return nil
		},

		// Livestream announcement
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			body := strings.ToLower(event.Body)
			title := strings.ToLower(event.Title)
			if strings.Contains(title, "直播") || strings.Contains(title, "live") ||
				strings.Contains(title, "生放送") || strings.Contains(title, "livestream") ||
				strings.Contains(body, "直播预告") || strings.Contains(body, "live schedule") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryEvent,
					Importance:    domain.ImportanceMedium,
					Tags:          []string{string(domain.TagLivestream)},
					Confidence:    0.90,
					Summary:       "直播预告",
					Reason:        "规则匹配：直播关键词",
					ReasonCode:    "rule_livestream_keyword",
				}
			}
			return nil
		},

		// Collaboration / merchandise
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			title := strings.ToLower(event.Title)
			if strings.Contains(title, "联动") || strings.Contains(title, "collaboration") ||
				strings.Contains(title, "周边") || strings.Contains(title, "merchandise") ||
				strings.Contains(title, "合作") || strings.Contains(title, "partnership") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryPromotion,
					Importance:    domain.ImportanceLow,
					Tags:          []string{string(domain.TagCollaboration), string(domain.TagMerchandise)},
					Flags:         []string{string(domain.FlagCommercial)},
					Confidence:    0.85,
					Summary:       "联动/周边推广",
					Reason:        "规则匹配：联动关键词",
					ReasonCode:    "rule_collaboration_keyword",
				}
			}
			return nil
		},

		// Repost detection
		func(event domain.NormalizedEvent) *domain.SemanticResult {
			title := strings.ToLower(event.Title)
			if strings.HasPrefix(title, "转发") ||
				strings.HasPrefix(title, "repost") ||
				strings.HasPrefix(title, "rt ") {
				return &domain.SemanticResult{
					SchemaVersion: 1,
					Category:      domain.CategoryRepost,
					Importance:    domain.ImportanceLow,
					Flags:         []string{domain.FlagRepost},
					Confidence:    0.95,
					Summary:       "转发内容",
					Reason:        "规则匹配：转发检测",
					ReasonCode:    "rule_repost_detection",
				}
			}
			return nil
		},
	}
}
