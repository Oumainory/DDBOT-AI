package policy

import (
	"testing"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

func TestResolvePolicyHardSafetyAndTagPriority(t *testing.T) {
	profile := OfficialGameProfile()
	result := domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryPromotion, Importance: domain.ImportanceLow, Tags: []string{"promo", "important"}, Confidence: .99}
	decision := ResolvePolicy(result, PolicyContext{Profile: profile})
	if decision.SuggestedAction != ActionDrop || decision.EffectiveAction != RoutePass {
		t.Fatalf("drop suggestion = %#v", decision)
	}
	result.Tags = []string{"promo", "must-pass"}
	profile.TagActions["must-pass"] = ActionPass
	decision = ResolvePolicy(result, PolicyContext{Profile: profile})
	if decision.SuggestedAction != ActionPass || decision.HardPassReason != "" {
		t.Fatalf("tag pass did not win = %#v", decision)
	}
	result.Importance = domain.ImportanceHigh
	decision = ResolvePolicy(result, PolicyContext{Profile: profile})
	if decision.SuggestedAction != ActionPass || decision.HardPassReason != "important_content" {
		t.Fatalf("important hard pass = %#v", decision)
	}
}

func TestResolveAIModeOverlayAndEnforceLock(t *testing.T) {
	resolved := ResolveAIMode(ModeLayer{Name: "global", Mode: ModeOff}, ModeLayer{Name: "target", Mode: ModeShadow})
	if resolved.Mode != ModeShadow || resolved.Provenance["mode"] != "target" {
		t.Fatalf("mode overlay = %#v", resolved)
	}
	if ValidatePhase4Mode(ModeEnforce) != ErrEnforceNotAvailable {
		t.Fatal("enforce was not locked")
	}
}

func TestUnknownTaxonomyCannotDrop(t *testing.T) {
	profile := Profile{ID: "custom", Name: "custom", DefaultAction: ActionPass, CategoryActions: map[domain.Category]Action{"vendor_new": ActionDrop}, TagActions: map[string]Action{"vendor_new": ActionDrop}}
	result := domain.SemanticResult{SchemaVersion: 1, Category: domain.Category("vendor_new"), Importance: domain.ImportanceLow, Tags: []string{"vendor_new"}, Confidence: .99}
	decision := ResolvePolicy(result, PolicyContext{Profile: profile})
	if decision.SuggestedAction != ActionPass || decision.EffectiveAction != RoutePass || decision.HardPassReason != "unknown_category" {
		t.Fatalf("unknown taxonomy decision = %#v", decision)
	}
	result.Category = domain.CategoryOther
	decision = ResolvePolicy(result, PolicyContext{Profile: profile})
	if decision.SuggestedAction == ActionDrop {
		t.Fatalf("unknown tag caused drop = %#v", decision)
	}
}

func TestHardSafetyFloorAndTruncatedInputOverridePolicy(t *testing.T) {
	profile := Profile{ID: "drop-all", Name: "drop-all", DefaultAction: ActionDrop}
	lowConfidence := domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryOther, Importance: domain.ImportanceLow, Confidence: .80}
	decision := ResolvePolicy(lowConfidence, PolicyContext{Profile: profile, Threshold: .10})
	if decision.SuggestedAction != ActionPass || decision.HardPassReason != "confidence_below_threshold" {
		t.Fatalf("low confidence bypassed frozen floor = %#v", decision)
	}
	valid := domain.SemanticResult{SchemaVersion: 1, Category: domain.CategoryOther, Importance: domain.ImportanceLow, Confidence: .99}
	decision = ResolvePolicy(valid, PolicyContext{Profile: profile, Threshold: .99, Truncated: true})
	if decision.SuggestedAction != ActionPass || decision.HardPassReason != "truncated_input" {
		t.Fatalf("truncated input was not hard-pass = %#v", decision)
	}
}

func TestResolvePolicyContextOverlaysSparseLayers(t *testing.T) {
	threshold := 0.95
	context := ResolvePolicyContext(
		PolicyLayer{Name: "global", DefaultAction: ActionDrop, CategoryActions: map[domain.Category]Action{domain.CategoryPromotion: ActionDrop}},
		PolicyLayer{Name: "target", Threshold: &threshold, TagActions: map[string]Action{"maintenance": ActionPass}},
	)
	if context.Threshold != threshold || context.Profile.DefaultAction != ActionDrop || context.Profile.CategoryActions[domain.CategoryPromotion] != ActionDrop || context.Profile.TagActions["maintenance"] != ActionPass {
		t.Fatalf("resolved policy context = %#v", context)
	}
}
