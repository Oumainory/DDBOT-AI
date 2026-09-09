package policy

import (
	"testing"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

func TestResolveUsesFieldLevelPrecedenceAndProvenance(t *testing.T) {
	enforce := ModeEnforce
	shadow := ModeShadow
	threshold := 0.95
	globalThreshold := 0.80

	resolved := Resolve(
		Layer{Name: "system", Mode: &shadow, Threshold: &globalThreshold},
		Layer{Name: "global", CategoryActions: map[domain.Category]Action{domain.CategoryPromotion: ActionDrop}},
		Layer{Name: "source", Mode: &enforce},
		Layer{Name: "target", Threshold: &threshold},
		Layer{Name: "subscription", CategoryActions: map[domain.Category]Action{domain.CategoryPromotion: ActionPass}},
	)

	if resolved.Mode != ModeEnforce || resolved.Threshold != threshold {
		t.Fatalf("resolved policy = %#v, want source mode and target threshold", resolved)
	}
	if resolved.CategoryActions[domain.CategoryPromotion] != ActionPass {
		t.Fatalf("subscription category action did not override global action")
	}
	if resolved.Provenance["mode"] != "source" || resolved.Provenance["threshold"] != "target" {
		t.Fatalf("unexpected provenance: %#v", resolved.Provenance)
	}
}

func TestEvaluateFailOpenRules(t *testing.T) {
	enforce := ModeEnforce
	policy := Resolve(Layer{Name: "system", Mode: &enforce}, Layer{}, Layer{}, Layer{}, Layer{
		Name:            "subscription",
		CategoryActions: map[domain.Category]Action{domain.CategoryPromotion: ActionDrop},
	})

	tests := []struct {
		name   string
		result domain.SemanticResult
		want   RouteAction
	}{
		{
			name:   "high importance passes",
			result: domain.SemanticResult{Category: domain.CategoryPromotion, Importance: domain.ImportanceHigh, Confidence: 0.99},
			want:   RoutePass,
		},
		{
			name:   "unknown passes",
			result: domain.SemanticResult{Category: domain.CategoryUnknown, Importance: domain.ImportanceLow, Confidence: 0.99},
			want:   RoutePass,
		},
		{
			name:   "low confidence passes",
			result: domain.SemanticResult{Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: 0.5},
			want:   RoutePass,
		},
		{
			name:   "qualified category drop",
			result: domain.SemanticResult{Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: 0.99},
			want:   RouteDrop,
		},
		{
			name:   "tag pass wins over tag drop",
			result: domain.SemanticResult{Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: 0.99, Tags: []string{"game_version", "commercial"}},
			want:   RoutePass,
		},
	}

	policy.TagActions["game_version"] = ActionPass
	policy.TagActions["commercial"] = ActionDrop
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Evaluate(tt.result, policy, true)
			if decision.Action != tt.want {
				t.Fatalf("Evaluate() = %#v, want action %q", decision, tt.want)
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("Decision.Validate() error = %v", err)
			}
		})
	}
}

func TestEvaluateDoesNotDropWhenReleaseIsNotEligible(t *testing.T) {
	enforce := ModeEnforce
	policy := Resolve(Layer{Name: "system", Mode: &enforce}, Layer{}, Layer{}, Layer{}, Layer{
		Name:            "subscription",
		CategoryActions: map[domain.Category]Action{domain.CategoryPromotion: ActionDrop},
	})
	result := domain.SemanticResult{Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Confidence: 0.99}
	decision := Evaluate(result, policy, false)
	if decision.Action != RoutePass || decision.Reason != "classifier_release_not_eligible" {
		t.Fatalf("Evaluate() = %#v, want fail-open release decision", decision)
	}
}

func TestEvaluateFailsOpenForInvalidPolicyOrSemanticResult(t *testing.T) {
	result := domain.SemanticResult{
		Category:   domain.CategoryPromotion,
		Importance: domain.ImportanceLow,
		Confidence: 0.99,
		Tags:       []string{"routine"},
	}
	policy := EffectivePolicy{
		Mode:            Mode("future_mode"),
		Threshold:       0.1,
		CategoryActions: map[domain.Category]Action{domain.CategoryPromotion: ActionDrop},
	}
	if decision := Evaluate(result, policy, true); decision.Action != RoutePass || decision.Reason != "invalid_policy_mode" {
		t.Fatalf("invalid mode decision = %#v", decision)
	}

	policy.Mode = ModeEnforce
	policy.Threshold = 2
	if decision := Evaluate(result, policy, true); decision.Action != RoutePass || decision.Reason != "invalid_policy_threshold" {
		t.Fatalf("invalid threshold decision = %#v", decision)
	}

	policy.Threshold = 0.9
	result.Confidence = 1.5
	if decision := Evaluate(result, policy, true); decision.Action != RoutePass || decision.Reason != "invalid_semantic_result" {
		t.Fatalf("invalid semantic result decision = %#v", decision)
	}
}
