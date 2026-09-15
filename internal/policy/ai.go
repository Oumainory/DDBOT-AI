package policy

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// ModeInherit is an overlay value. It is intentionally distinct from the
// resolved default (Shadow).
const ModeInherit Mode = "inherit"

var (
	ErrEnforceNotAvailable     = errors.New("policy: enforce is not available in phase 4")
	ErrInvalidEnforceThreshold = errors.New("policy: enforce threshold is outside the safe range")
)

var knownCategories = map[domain.Category]struct{}{
	domain.CategoryAnnouncement: {}, domain.CategoryUpdate: {}, domain.CategoryMaintenance: {},
	domain.CategoryIncident: {}, domain.CategoryEvent: {}, domain.CategoryPromotion: {},
	domain.CategoryGiveaway: {}, domain.CategoryCommunity: {}, domain.CategoryRepost: {},
	domain.CategoryContentPublish: {}, domain.CategoryPersonalUpdate: {}, domain.CategorySchedule: {},
	domain.CategoryPolicyChange: {}, domain.CategoryOther: {}, domain.CategoryUnknown: {},
}

var knownTags = map[string]struct{}{
	"promo": {}, "promotion": {}, "giveaway": {}, "community": {}, "personal_update": {},
	"no_new_information": {}, "repost": {}, "game_version": {}, "version_preview": {},
	"new_character": {}, "new_content": {}, "game_event": {}, "gacha": {}, "maintenance": {},
	"bug": {}, "compensation": {}, "service_outage": {}, "shutdown": {}, "delay": {},
	"livestream": {}, "collaboration": {}, "merchandise": {}, "security": {}, "breaking_change": {},
}

type ModeLayer struct {
	Name string
	Mode Mode
}

type ModeResolution struct {
	Mode       Mode              `json:"mode"`
	Provenance map[string]string `json:"provenance"`
}

// ResolveAIMode applies the frozen System → Global → Source → Target →
// Subscription overlay without touching storage. Enforce is representable in
// the value model but callers must reject activation in Phase 4 APIs.
func ResolveAIMode(layers ...ModeLayer) ModeResolution {
	resolved := ModeResolution{Mode: ModeShadow, Provenance: map[string]string{"mode": "default"}}
	for _, layer := range layers {
		mode := Mode(strings.ToLower(strings.TrimSpace(string(layer.Mode))))
		if layer.Name == "" || mode == "" || mode == ModeInherit {
			continue
		}
		switch mode {
		case ModeOff, ModeShadow, ModeEnforce:
			resolved.Mode = mode
			resolved.Provenance["mode"] = layer.Name
		}
	}
	return resolved
}

// ValidatePhase4Mode keeps ENFORCE representable in durable policy records
// while rejecting any attempt to activate it in the Shadow-only runtime.
func ValidatePhase4Mode(mode Mode) error {
	mode = Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	if mode == ModeEnforce {
		return ErrEnforceNotAvailable
	}
	if mode != "" && mode != ModeInherit && mode != ModeOff && mode != ModeShadow {
		return fmt.Errorf("policy: invalid mode %q", mode)
	}
	return nil
}

type Profile struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Description     string                     `json:"description,omitempty"`
	DefaultAction   Action                     `json:"default_action"`
	CategoryActions map[domain.Category]Action `json:"category_actions,omitempty"`
	TagActions      map[string]Action          `json:"tag_actions,omitempty"`
	Safety          map[string]bool            `json:"safety,omitempty"`
	Builtin         bool                       `json:"builtin,omitempty"`
}

func (p Profile) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Name) == "" {
		return errors.New("policy: profile id and name are required")
	}
	if !validAction(p.DefaultAction) {
		return errors.New("policy: invalid profile default action")
	}
	for category, action := range p.CategoryActions {
		if category == "" || !validAction(action) {
			return errors.New("policy: invalid profile category action")
		}
	}
	for tag, action := range p.TagActions {
		if strings.TrimSpace(tag) == "" || !validAction(action) {
			return errors.New("policy: invalid profile tag action")
		}
	}
	return nil
}

func OfficialGameProfile() Profile {
	return Profile{
		ID:            "builtin.official_game",
		Name:          "Official Game / 官方游戏资讯",
		Description:   "面向官方游戏账号的低噪声语义过滤模板",
		DefaultAction: ActionPass,
		CategoryActions: map[domain.Category]Action{
			domain.CategoryPromotion:      ActionDrop,
			domain.CategoryGiveaway:       ActionDrop,
			domain.CategoryCommunity:      ActionDrop,
			domain.CategoryPersonalUpdate: ActionDrop,
			domain.CategoryRepost:         ActionDrop,
			domain.CategoryEvent:          ActionDrop,
		},
		TagActions: map[string]Action{
			"promo": ActionDrop, "giveaway": ActionDrop, "community": ActionDrop,
			"personal_update": ActionDrop, "no_new_information": ActionDrop,
		},
		Builtin: true,
	}
}

type PolicyContext struct {
	Profile        Profile
	Threshold      float64
	MissingContext bool
	// Truncated is supplied by the normalizer/runtime when the public input
	// had to be bounded.  A policy threshold can never override this safety
	// signal; incomplete input is always a PASS recommendation.
	Truncated bool
}

// PolicyLayer is an optional override in the same hierarchy as ModeLayer.
// Zero fields mean inherit; the resolver never writes a resolved copy back to
// storage, so callers can retain only the sparse override at each scope.
type PolicyLayer struct {
	Name            string
	Profile         *Profile
	Threshold       *float64
	DefaultAction   Action
	CategoryActions map[domain.Category]Action
	TagActions      map[string]Action
}

// ResolvePolicyContext overlays sparse policy values from System through
// Subscription. It is pure and deterministic; maps are copied so a caller
// cannot mutate an earlier layer through the resolved context.
func ResolvePolicyContext(layers ...PolicyLayer) PolicyContext {
	result := PolicyContext{Profile: Profile{DefaultAction: ActionPass}, Threshold: 0.90}
	for _, layer := range layers {
		if layer.Profile != nil {
			profile := cloneProfile(*layer.Profile)
			if profile.ID != "" {
				result.Profile.ID = profile.ID
			}
			if profile.Name != "" {
				result.Profile.Name = profile.Name
			}
			if profile.Description != "" {
				result.Profile.Description = profile.Description
			}
			if profile.DefaultAction != "" && profile.DefaultAction != ActionInherit {
				result.Profile.DefaultAction = profile.DefaultAction
			}
			if profile.CategoryActions != nil {
				if result.Profile.CategoryActions == nil {
					result.Profile.CategoryActions = make(map[domain.Category]Action)
				}
				for category, action := range profile.CategoryActions {
					if action != ActionInherit {
						result.Profile.CategoryActions[category] = action
					}
				}
			}
			if profile.TagActions != nil {
				if result.Profile.TagActions == nil {
					result.Profile.TagActions = make(map[string]Action)
				}
				for tag, action := range profile.TagActions {
					if action != ActionInherit {
						result.Profile.TagActions[tag] = action
					}
				}
			}
		}
		if layer.Threshold != nil {
			result.Threshold = *layer.Threshold
		}
		if layer.DefaultAction != "" && layer.DefaultAction != ActionInherit {
			result.Profile.DefaultAction = layer.DefaultAction
		}
		if layer.CategoryActions != nil {
			if result.Profile.CategoryActions == nil {
				result.Profile.CategoryActions = make(map[domain.Category]Action)
			}
			for category, action := range layer.CategoryActions {
				if action != ActionInherit {
					result.Profile.CategoryActions[category] = action
				}
			}
		}
		if layer.TagActions != nil {
			if result.Profile.TagActions == nil {
				result.Profile.TagActions = make(map[string]Action)
			}
			for tag, action := range layer.TagActions {
				if action != ActionInherit {
					result.Profile.TagActions[tag] = action
				}
			}
		}
	}
	return result
}

func cloneProfile(value Profile) Profile {
	value.CategoryActions = cloneCategoryActions(value.CategoryActions)
	value.TagActions = cloneTagActions(value.TagActions)
	value.Safety = cloneBoolMap(value.Safety)
	return value
}

func cloneCategoryActions(value map[domain.Category]Action) map[domain.Category]Action {
	if value == nil {
		return nil
	}
	result := make(map[domain.Category]Action, len(value))
	for key, action := range value {
		result[key] = action
	}
	return result
}

func cloneTagActions(value map[string]Action) map[string]Action {
	if value == nil {
		return nil
	}
	result := make(map[string]Action, len(value))
	for key, action := range value {
		result[key] = action
	}
	return result
}

func cloneBoolMap(value map[string]bool) map[string]bool {
	if value == nil {
		return nil
	}
	result := make(map[string]bool, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

type ShadowDecision struct {
	SuggestedAction Action            `json:"suggested_action"`
	EffectiveAction RouteAction       `json:"effective_action"`
	Reason          string            `json:"reason"`
	HardPassReason  string            `json:"hard_pass_reason,omitempty"`
	Provenance      map[string]string `json:"provenance,omitempty"`
}

// ClassificationSuggestedAction is the event-level, profile-independent
// hint recorded with a decision. Profiles still own the route-specific policy
// evaluation; this helper only captures the obvious low-value taxonomy signal
// so the Dashboard can distinguish model classification from policy overlay.
func ClassificationSuggestedAction(result domain.SemanticResult) Action {
	if err := result.Validate(); err != nil || result.Importance == domain.ImportanceHigh || result.Importance == domain.ImportanceCritical || result.Category == domain.CategoryUnknown || result.Confidence < 0.90 || result.Uncertain || result.InsufficientContext || result.PromptInjectionSuspected {
		return ActionPass
	}
	for _, flag := range result.Flags {
		if flag == domain.FlagTruncated || flag == domain.FlagInsufficientContext || flag == domain.FlagPromptInjectionSuspect {
			return ActionPass
		}
	}
	switch result.Category {
	case domain.CategoryPromotion, domain.CategoryGiveaway, domain.CategoryCommunity, domain.CategoryPersonalUpdate, domain.CategoryRepost:
		return ActionDrop
	default:
		return ActionPass
	}
}

// ResolvePolicy is the pure local profile evaluator. It never returns DROP as
// an effective action; Phase 4 only records what Enforce would have done.
func ResolvePolicy(result domain.SemanticResult, ctx PolicyContext) ShadowDecision {
	decision := ShadowDecision{SuggestedAction: ActionPass, EffectiveAction: RoutePass, Provenance: map[string]string{}}
	threshold := ctx.Threshold
	if threshold == 0 {
		threshold = 0.90
	}
	if ctx.MissingContext {
		return hardPass(decision, "missing_policy_context")
	}
	if ctx.Truncated {
		return hardPass(decision, "truncated_input")
	}
	if err := ctx.Profile.Validate(); err != nil {
		return hardPass(decision, "invalid_policy_context")
	}
	if err := result.Validate(); err != nil {
		return hardPass(decision, "invalid_classification")
	}
	if _, ok := knownCategories[result.Category]; !ok {
		return hardPass(decision, "unknown_category")
	}
	if result.Importance != domain.ImportanceLow && result.Importance != domain.ImportanceMedium && result.Importance != domain.ImportanceHigh && result.Importance != domain.ImportanceCritical {
		return hardPass(decision, "unknown_importance")
	}
	if threshold < 0 || threshold > 1 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return hardPass(decision, "invalid_policy_threshold")
	}
	// 0.90 is a frozen safety floor, not merely the default policy value. A
	// sparse policy may raise the threshold, but it must never lower the floor
	// and thereby turn an uncertain low-confidence classification into DROP.
	if threshold < 0.90 {
		threshold = 0.90
	}
	if result.Importance == domain.ImportanceHigh || result.Importance == domain.ImportanceCritical {
		return hardPass(decision, "important_content")
	}
	if result.Category == domain.CategoryUnknown || result.Confidence < threshold || result.Uncertain || result.InsufficientContext || result.PromptInjectionSuspected {
		reason := "unknown_category"
		switch {
		case result.Confidence < threshold:
			reason = "confidence_below_threshold"
		case result.Uncertain:
			reason = "uncertain"
		case result.InsufficientContext:
			reason = "insufficient_context"
		case result.PromptInjectionSuspected:
			reason = "prompt_injection_suspected"
		}
		return hardPass(decision, reason)
	}
	for _, flag := range result.Flags {
		if flag == domain.FlagTruncated || flag == domain.FlagInsufficientContext || flag == domain.FlagPromptInjectionSuspect {
			return hardPass(decision, "unsafe_context")
		}
	}
	if action := ctx.Profile.TagActions; action != nil {
		for _, tag := range result.Tags {
			if action[tag] == ActionPass {
				decision.Provenance["action"] = "tag_pass:" + tag
				decision.Reason = "tag_pass:" + tag
				return decision
			}
		}
		for _, tag := range result.Tags {
			if _, known := knownTags[tag]; !known {
				continue
			}
			if action[tag] == ActionDrop {
				decision.SuggestedAction = ActionDrop
				decision.Provenance["action"] = "tag_drop:" + tag
				decision.Reason = "tag_drop:" + tag
				return decision
			}
		}
	}
	if action := ctx.Profile.CategoryActions[result.Category]; action == ActionDrop {
		decision.SuggestedAction = ActionDrop
		decision.Provenance["action"] = "category_drop:" + string(result.Category)
		decision.Reason = "category_drop:" + string(result.Category)
		return decision
	} else if action == ActionPass {
		decision.Provenance["action"] = "category_pass:" + string(result.Category)
		decision.Reason = "category_pass:" + string(result.Category)
		return decision
	}
	if ctx.Profile.DefaultAction == ActionDrop {
		decision.SuggestedAction = ActionDrop
		decision.Provenance["action"] = "profile_default_drop"
		decision.Reason = "profile_default_drop"
		return decision
	}
	decision.Provenance["action"] = "default_pass"
	decision.Reason = "default_pass"
	return decision
}

func hardPass(decision ShadowDecision, reason string) ShadowDecision {
	decision.SuggestedAction = ActionPass
	decision.EffectiveAction = RoutePass
	decision.HardPassReason = reason
	decision.Reason = reason
	decision.Provenance["action"] = "hard_safety_pass"
	return decision
}

func validAction(action Action) bool {
	return action == ActionInherit || action == ActionPass || action == ActionDrop
}

func (d ShadowDecision) Validate() error {
	if d.SuggestedAction != ActionPass && d.SuggestedAction != ActionDrop {
		return fmt.Errorf("policy: invalid suggested action %q", d.SuggestedAction)
	}
	if d.EffectiveAction != RoutePass {
		return errors.New("policy: phase 4 effective action must be pass")
	}
	return nil
}
