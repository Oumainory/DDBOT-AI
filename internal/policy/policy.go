// Package policy resolves field-level inheritance and performs the final
// deterministic AI Route decision. The classifier never returns PASS/DROP;
// this package owns that user-policy decision.
package policy

import (
	"fmt"
	"strings"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

type Mode string

const (
	ModeOff     Mode = "off"
	ModeShadow  Mode = "shadow"
	ModeEnforce Mode = "enforce"
)

type Action string

const (
	ActionInherit Action = "inherit"
	ActionPass    Action = "pass"
	ActionDrop    Action = "drop"
)

type RouteAction string

const (
	RoutePass RouteAction = "pass"
	RouteDrop RouteAction = "drop"
)

type Layer struct {
	Name            string
	Mode            *Mode
	Threshold       *float64
	CategoryActions map[domain.Category]Action
	TagActions      map[string]Action
}

type EffectivePolicy struct {
	Mode            Mode
	Threshold       float64
	CategoryActions map[domain.Category]Action
	TagActions      map[string]Action
	Provenance      map[string]string
}

func Resolve(system, global, source, target, subscription Layer) EffectivePolicy {
	result := EffectivePolicy{
		Mode:            ModeShadow,
		Threshold:       0.90,
		CategoryActions: make(map[domain.Category]Action),
		TagActions:      make(map[string]Action),
		Provenance:      map[string]string{"mode": "default", "threshold": "default"},
	}

	for _, layer := range []Layer{system, global, source, target, subscription} {
		if layer.Name == "" {
			continue
		}
		if layer.Mode != nil {
			result.Mode = *layer.Mode
			result.Provenance["mode"] = layer.Name
		}
		if layer.Threshold != nil {
			result.Threshold = *layer.Threshold
			result.Provenance["threshold"] = layer.Name
		}
		for category, action := range layer.CategoryActions {
			if action == ActionInherit {
				continue
			}
			result.CategoryActions[category] = action
			result.Provenance["category:"+string(category)] = layer.Name
		}
		for tag, action := range layer.TagActions {
			if action == ActionInherit {
				continue
			}
			result.TagActions[tag] = action
			result.Provenance["tag:"+tag] = layer.Name
		}
	}

	return result
}

type Decision struct {
	Action RouteAction
	Reason string
}

func Evaluate(result domain.SemanticResult, policy EffectivePolicy, eligible bool) Decision {
	if policy.Mode == ModeOff {
		return Decision{Action: RoutePass, Reason: "ai_mode_off"}
	}
	if policy.Mode == ModeShadow {
		return Decision{Action: RoutePass, Reason: "ai_mode_shadow"}
	}
	if policy.Mode != ModeEnforce {
		return Decision{Action: RoutePass, Reason: "invalid_policy_mode"}
	}
	if policy.Threshold < 0 || policy.Threshold > 1 {
		return Decision{Action: RoutePass, Reason: "invalid_policy_threshold"}
	}
	if !eligible {
		return Decision{Action: RoutePass, Reason: "classifier_release_not_eligible"}
	}
	if err := result.Validate(); err != nil {
		return Decision{Action: RoutePass, Reason: "invalid_semantic_result"}
	}
	if result.Category == domain.CategoryUnknown || result.Uncertain {
		return Decision{Action: RoutePass, Reason: "uncertain_semantic_result"}
	}
	if result.Importance == domain.ImportanceHigh || result.Importance == domain.ImportanceCritical {
		return Decision{Action: RoutePass, Reason: "important_content_hard_pass"}
	}
	if result.Confidence < policy.Threshold {
		return Decision{Action: RoutePass, Reason: "confidence_below_threshold"}
	}
	for _, flag := range result.Flags {
		switch flag {
		case domain.FlagTruncated, domain.FlagInsufficientContext, domain.FlagPromptInjectionSuspect:
			return Decision{Action: RoutePass, Reason: "unsafe_context_hard_pass"}
		}
	}

	for _, tag := range result.Tags {
		if policy.TagActions[tag] == ActionPass {
			return Decision{Action: RoutePass, Reason: "tag_pass:" + tag}
		}
	}
	for _, tag := range result.Tags {
		if policy.TagActions[tag] == ActionDrop {
			return Decision{Action: RouteDrop, Reason: "tag_drop:" + tag}
		}
	}
	if action := policy.CategoryActions[result.Category]; action == ActionDrop {
		return Decision{Action: RouteDrop, Reason: "category_drop:" + string(result.Category)}
	}
	if action := policy.CategoryActions[result.Category]; action == ActionPass {
		return Decision{Action: RoutePass, Reason: "category_pass:" + string(result.Category)}
	}
	return Decision{Action: RoutePass, Reason: "policy_default_pass"}
}

func (d Decision) Validate() error {
	if d.Action != RoutePass && d.Action != RouteDrop {
		return fmt.Errorf("policy: invalid route action %q", d.Action)
	}
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("policy: decision reason is required")
	}
	return nil
}
