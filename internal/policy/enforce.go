package policy

import (
	"math"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

// ResolveEnforcePolicy applies the request-scoped policy values used by the
// approval command to a durable profile snapshot.  Keeping this calculation in
// the policy package lets the HTTP presentation path and the durable approval
// transaction use exactly the same digest semantics.
func ResolveEnforcePolicy(base Profile, threshold *float64, defaultAction Action, categoryActions map[domain.Category]Action, tagActions map[string]Action) (EffectivePolicy, Profile, error) {
	profile := base
	if defaultAction != "" && defaultAction != ActionInherit {
		profile.DefaultAction = defaultAction
	}
	if profile.CategoryActions == nil {
		profile.CategoryActions = make(map[domain.Category]Action)
	}
	for key, action := range categoryActions {
		if action != ActionInherit {
			profile.CategoryActions[key] = action
		}
	}
	if profile.TagActions == nil {
		profile.TagActions = make(map[string]Action)
	}
	for key, action := range tagActions {
		if action != ActionInherit {
			profile.TagActions[key] = action
		}
	}
	if err := profile.Validate(); err != nil {
		return EffectivePolicy{}, Profile{}, err
	}
	value := 0.90
	if threshold != nil {
		value = *threshold
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0.90 || value > 1 {
		return EffectivePolicy{}, Profile{}, ErrInvalidEnforceThreshold
	}
	return EffectivePolicy{Mode: ModeEnforce, Threshold: value, CategoryActions: profile.CategoryActions, TagActions: profile.TagActions}, profile, nil
}
