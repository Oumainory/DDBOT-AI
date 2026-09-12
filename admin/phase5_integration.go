package admin

// This file contains the narrow Legacy-to-Phase-5 bridge.  The bridge is
// deliberately built around observation.RouteTrace's allowlisted public
// snapshot: it never reaches into a source adapter, BuntDB subscription, or
// Messenger implementation while an AI provider is running.

import (
	"context"
	"strconv"
	"strings"

	"github.com/Oumainory/DDBOT-AI/adapter"
	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/enforce"
	"github.com/Oumainory/DDBOT-AI/internal/normalizer"
	"github.com/Oumainory/DDBOT-AI/internal/observation"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
	"github.com/Oumainory/DDBOT-AI/lsp/mmsg"
)

func eventFromObserved(record platformdb.ObservedEventRecord) (domain.NormalizedEvent, error) {
	// All provider input must pass through the same bounded, public-only
	// normalizer used by the Phase 4/5 evaluation contracts.  Decoding the
	// observation JSON directly here would preserve unbounded text, unknown
	// fields, or unsafe URLs and would make the pre-send hook a second, weaker
	// normalization path. Unsupported or malformed observations fail open at
	// the caller, so they can never become an authoritative DROP.
	return normalizer.NormalizeObserved(record)
}

func targetForLegacyGroup(ctx context.Context, repository *platformdb.DomainRepository, externalID string) (domain.Target, bool) {
	if repository == nil || strings.TrimSpace(externalID) == "" {
		return domain.Target{}, false
	}
	values, err := repository.ListTargets(ctx)
	if err != nil {
		return domain.Target{}, false
	}
	var match domain.Target
	found := 0
	for _, value := range values {
		if value.TargetType != domain.TargetGroup || strings.TrimSpace(value.ExternalID) != strings.TrimSpace(externalID) {
			continue
		}
		if value.Status != "" && value.Status != domain.TargetResolved {
			continue
		}
		match = value
		found++
	}
	return match, found == 1
}

func resolvePhase5Layers(ctx context.Context, aiRepository *platformdb.AIRepository, sourceID, targetExternal string) (policy.Mode, policy.PolicyContext) {
	modeLayers := []policy.ModeLayer{{Name: "system", Mode: policy.ModeShadow}}
	baseProfile := policy.OfficialGameProfile()
	policyLayers := []policy.PolicyLayer{{Name: "system", Profile: &baseProfile}}
	if aiRepository == nil {
		return policy.ResolveAIMode(modeLayers...).Mode, policy.ResolvePolicyContext(policyLayers...)
	}
	load := func(scopeType, scopeID, name string) {
		if scopeType != "global" && strings.TrimSpace(scopeID) == "" {
			return
		}
		override, err := aiRepository.Policy(ctx, scopeType, scopeID)
		if err != nil {
			return
		}
		if override.Mode != "" && override.Mode != policy.ModeInherit {
			modeLayers = append(modeLayers, policy.ModeLayer{Name: name, Mode: override.Mode})
		}
		layer := policy.PolicyLayer{Name: name, Threshold: override.Threshold, DefaultAction: override.DefaultAction, CategoryActions: override.CategoryActions, TagActions: override.TagActions}
		if override.ProfileID != "" {
			if profile, profileErr := aiRepository.Profile(ctx, override.ProfileID); profileErr == nil {
				layer.Profile = &profile
			}
		}
		policyLayers = append(policyLayers, layer)
	}
	load("global", "", "global")
	load("source", sourceID, "source")
	load("target", targetExternal, "target")
	return policy.ResolveAIMode(modeLayers...).Mode, policy.ResolvePolicyContext(policyLayers...)
}

// evaluateEnforceRoute is invoked synchronously immediately before the
// historical Messenger call. It returns true only when Runtime has committed
// both RouteDecision(DROP) and its replay snapshot. Every other condition is
// fail-open and therefore lets Legacy send normally.
func evaluateEnforceRoute(ctx context.Context, _ *adapter.SendingMessage, target mmsg.Target, trace observation.RouteTrace, observations *platformdb.ObservationRepository, domains *platformdb.DomainRepository, ai *platformdb.AIRepository, runtime *enforce.Runtime) (drop bool, reason string, err error) {
	if runtime == nil || !trace.Valid() || target == nil || !target.TargetType().IsGroup() {
		return false, "phase5_unavailable", nil
	}
	observed, ok := trace.EventSnapshot()
	if !ok && observations != nil {
		observed, err = observations.GetObservedEvent(ctx, trace.EventObservationID())
		ok = err == nil
	}
	if !ok {
		return false, "observation_unavailable", nil
	}
	event, eventErr := eventFromObserved(observed)
	if eventErr != nil {
		return false, "invalid_public_snapshot", nil
	}
	groupID := strconv.FormatInt(target.TargetCode(), 10)
	resolvedTarget, _ := targetForLegacyGroup(ctx, domains, groupID)
	mode, policyContext := resolvePhase5Layers(ctx, ai, event.SourceID, groupID)
	if mode != policy.ModeEnforce {
		// The pre-send hook is intentionally inert for OFF/SHADOW. Shadow's
		// existing asynchronous hook remains the diagnostic path.
		return false, "ai_mode_" + string(mode), nil
	}
	release := classifier.Release{}
	if ai != nil {
		release, _ = ai.ActiveRelease(ctx)
	}
	routes := []enforce.Route{{RouteObservationID: trace.RouteObservationID(), ObservedEventID: trace.EventObservationID(), SourceID: event.SourceID, Target: enforce.Target{ID: resolvedTarget.ID, Type: string(domain.TargetGroup), ExternalID: groupID, ConnectorID: resolvedTarget.ConnectorID}, Mode: mode, Policy: policyContext, Release: release}}
	results, evalErr := runtime.Evaluate(ctx, event, routes)
	if evalErr != nil || len(results) != 1 {
		return false, "enforce_evaluation_unavailable", nil
	}
	result := results[0]
	if result.SuppressSend && result.Persisted {
		return true, result.Reason, nil
	}
	if result.Reason == "" {
		return false, "pass", nil
	}
	return false, result.Reason, nil
}
