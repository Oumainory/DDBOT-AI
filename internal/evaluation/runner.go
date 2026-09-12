// Package evaluation runs bounded, explicit Phase 4 evaluation jobs against a
// frozen NormalizedEvent snapshot. It is separate from Shadow scheduling: an
// evaluation never changes Legacy delivery and never retries a provider call.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/classifier"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/policy"
	"github.com/cnxysoft/DDBOT-WSa/internal/provider"
)

var (
	ErrUnavailable = errors.New("evaluation: unavailable")
	ErrRunActive   = errors.New("evaluation: run is already active")
)

type Runner struct {
	Repository *platformdb.AIRepository
	Provider   provider.Provider
	Now        func() time.Time
	// Audit is an optional safe-metadata sink owned by the API layer. It is
	// called only for explicit evaluation run start/finish transitions; raw
	// snapshots, prompts and provider responses never cross this seam.
	Audit      func(context.Context, string, string, string, map[string]any)
}

func New(repository *platformdb.AIRepository, p provider.Provider, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	return &Runner{Repository: repository, Provider: p, Now: now}
}

// Run executes one explicitly created evaluation run. Cases are read from
// their durable, public normalized snapshots; no raw provider response or
// prompt is persisted. A provider error is a case-level PASS/error result and
// never causes an automatic retry.
func (r *Runner) Run(ctx context.Context, runID string) (platformdb.EvaluationRunRecord, error) {
	if r == nil || r.Repository == nil {
		return platformdb.EvaluationRunRecord{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	run, err := r.Repository.EvaluationRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return platformdb.EvaluationRunRecord{}, err
	}
	if run.Status == "completed" || run.Status == "failed" {
		return run, nil
	}
	if run.Status != "queued" && run.Status != "running" {
		return platformdb.EvaluationRunRecord{}, ErrRunActive
	}
	if err := r.Repository.StartEvaluationRun(ctx, run.ID); err != nil {
		if errors.Is(err, platformdb.ErrEvaluationRunActive) {
			return platformdb.EvaluationRunRecord{}, ErrRunActive
		}
		return platformdb.EvaluationRunRecord{}, err
	}
	r.audit(ctx, "evaluation.run_started", run.ID, "success", map[string]any{"classifier_release_id": run.ClassifierReleaseID, "case_count": len(run.CaseIDs)})
	release, err := r.Repository.Release(ctx, run.ClassifierReleaseID)
	if err != nil {
		_ = r.Repository.FinishEvaluationRun(ctx, run.ID, "failed", map[string]any{"error": "release_unavailable"}, 0, 0, r.now())
		r.audit(ctx, "evaluation.run_finished", run.ID, "failed", map[string]any{"status": "failed", "error": "release_unavailable"})
		return platformdb.EvaluationRunRecord{}, err
	}
	cases, err := r.Repository.EvaluationCases(ctx)
	if err != nil {
		_ = r.Repository.FinishEvaluationRun(ctx, run.ID, "failed", map[string]any{"error": "cases_unavailable"}, 0, 0, r.now())
		r.audit(ctx, "evaluation.run_finished", run.ID, "failed", map[string]any{"status": "failed", "error": "cases_unavailable"})
		return platformdb.EvaluationRunRecord{}, err
	}
	cases = selectCases(cases, run.CaseIDs)
	metrics := map[string]any{
		"total_cases":          len(cases),
		"parsed_cases":         0,
		"provider_errors":      0,
		"important_false_drop": 0,
		"drop_total":           0,
		"drop_correct":         0,
	}
	var totalCost, totalLatency int64
	for _, item := range cases {
		started := r.now()
		classification := classifier.Classification{}
		suggested := "pass"
		comparison := "unknown"
		cost := int64(0)

		var event domain.NormalizedEvent
		if unmarshalErr := json.Unmarshal(item.NormalizedInputSnapshot, &event); unmarshalErr != nil || event.Validate() != nil {
			comparison = "invalid_input"
		} else if r.Provider == nil {
			metrics["provider_errors"] = metricsInt(metrics, "provider_errors") + 1
			comparison = "provider_unavailable"
		} else {
			result, usage, classifyErr := r.Provider.Classify(ctx, event)
			if classifyErr == nil {
				if usageErr := usage.Validate(); usageErr != nil {
					classifyErr = provider.ErrInvalidResponse
					usage = classifier.Usage{}
				}
			}
			costPtr := costFor(release, usage)
			if costPtr != nil {
				cost = *costPtr
			}
			if classifyErr != nil {
				metrics["provider_errors"] = metricsInt(metrics, "provider_errors") + 1
				comparison = provider.StableErrorCode(classifyErr)
			} else {
				classification = result
				metrics["parsed_cases"] = metricsInt(metrics, "parsed_cases") + 1
				resolved := policy.ResolvePolicy(result, policy.PolicyContext{Profile: policy.OfficialGameProfile()})
				suggested = string(resolved.SuggestedAction)
				comparison = compare(item.ExpectedAction, suggested)
			}
		}
		latency := r.now().Sub(started).Milliseconds()
		if latency < 0 {
			latency = 0
		}
		totalLatency += latency
		totalCost += cost
		if item.Critical && item.ExpectedAction == "pass" && suggested == "drop" {
			metrics["important_false_drop"] = metricsInt(metrics, "important_false_drop") + 1
		}
		if suggested == "drop" {
			metrics["drop_total"] = metricsInt(metrics, "drop_total") + 1
			if item.ExpectedAction == "drop" {
				metrics["drop_correct"] = metricsInt(metrics, "drop_correct") + 1
			}
		}
		raw, marshalErr := json.Marshal(classification)
		if marshalErr != nil {
			raw = []byte(`{}`)
		}
		if err := r.Repository.PutEvaluationResult(ctx, platformdb.EvaluationResultRecord{
			RunID: run.ID, CaseID: item.ID, Classification: raw, SuggestedAction: suggested,
			ExpectedAction: item.ExpectedAction, Comparison: comparison, CostMicros: cost,
			LatencyMS: latency, CreatedAt: r.now(),
		}); err != nil {
			_ = r.Repository.FinishEvaluationRun(ctx, run.ID, "failed", map[string]any{"error": "result_persist_failed"}, totalCost, totalLatency, r.now())
			r.audit(ctx, "evaluation.run_finished", run.ID, "failed", map[string]any{"status": "failed", "error": "result_persist_failed"})
			return platformdb.EvaluationRunRecord{}, err
		}
	}
	totalCases := int64(len(cases))
	parsed := int64(metricsInt(metrics, "parsed_cases"))
	dropTotal := int64(metricsInt(metrics, "drop_total"))
	dropCorrect := int64(metricsInt(metrics, "drop_correct"))
	parseRate := float64(1)
	if totalCases > 0 {
		parseRate = float64(parsed) / float64(totalCases)
	}
	dropPrecision := float64(1)
	if dropTotal > 0 {
		dropPrecision = float64(dropCorrect) / float64(dropTotal)
	}
	metrics["parse_success"] = parseRate
	metrics["drop_precision"] = dropPrecision
	metrics["total_cost_micros"] = totalCost
	metrics["latency_ms"] = totalLatency
	if err := r.Repository.FinishEvaluationRun(ctx, run.ID, "completed", metrics, totalCost, totalLatency, r.now()); err != nil {
		return platformdb.EvaluationRunRecord{}, err
	}
	r.audit(ctx, "evaluation.run_finished", run.ID, "success", map[string]any{"status": "completed", "total_cases": len(cases), "parsed_cases": metrics["parsed_cases"]})
	return r.Repository.EvaluationRun(ctx, run.ID)
}

func (r *Runner) audit(ctx context.Context, action, resourceID, outcome string, metadata map[string]any) {
	if r != nil && r.Audit != nil {
		r.Audit(ctx, action, resourceID, outcome, metadata)
	}
}

func (r *Runner) now() time.Time {
	if r == nil || r.Now == nil {
		return time.Now().UTC()
	}
	return r.Now().UTC()
}

func selectCases(values []platformdb.EvaluationCaseRecord, ids []string) []platformdb.EvaluationCaseRecord {
	if len(ids) == 0 {
		return values
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			wanted[id] = struct{}{}
		}
	}
	selected := make([]platformdb.EvaluationCaseRecord, 0, len(wanted))
	for _, value := range values {
		if _, ok := wanted[value.ID]; ok {
			selected = append(selected, value)
		}
	}
	return selected
}

func compare(expected, suggested string) string {
	if expected == "" {
		return "unlabeled"
	}
	if expected == suggested {
		return "match"
	}
	if expected == "pass" && suggested == "drop" {
		return "false_drop"
	}
	if expected == "drop" && suggested == "pass" {
		return "missed_drop"
	}
	return "mismatch"
}

func metricsInt(metrics map[string]any, key string) int {
	if value, ok := metrics[key].(int); ok {
		return value
	}
	return 0
}

func costFor(release classifier.Release, usage classifier.Usage) *int64 {
	if usage.Validate() != nil {
		return nil
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil {
		return nil
	}
	var in, out int64
	if usage.InputTokens != nil {
		in = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		out = *usage.OutputTokens
	}
	value := release.RequestPriceMicros
	if release.InputPriceMicrosPerMillion > 0 {
		value += in * release.InputPriceMicrosPerMillion / 1_000_000
	}
	if release.OutputPriceMicrosPerMillion > 0 {
		value += out * release.OutputPriceMicrosPerMillion / 1_000_000
	}
	return &value
}
