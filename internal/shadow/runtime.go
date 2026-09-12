// Package shadow is the asynchronous, fail-open Phase 4 classifier runtime.
// It never participates in the Legacy send decision and never changes a
// rendered message.
package shadow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/normalizer"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
)

var (
	ErrQueueFull = errors.New("shadow: queue full")
	ErrClosed    = errors.New("shadow: runtime closed")
)

type Route struct {
	RouteObservationID string
	Eligible           bool
	Mode               policy.ModeResolution
	Policy             policy.PolicyContext
}

type Job struct {
	Event   domain.NormalizedEvent
	Release classifier.Release
	Routes  []Route
}

type Stats struct {
	QueueCapacity int    `json:"queue_capacity"`
	QueueDepth    int    `json:"queue_depth"`
	Scheduled     uint64 `json:"scheduled"`
	Completed     uint64 `json:"completed"`
	Failed        uint64 `json:"failed"`
	QueueDropped  uint64 `json:"queue_dropped"`
	ProviderCalls uint64 `json:"provider_calls"`
}

type Config struct {
	Repository     *platformdb.AIRepository
	Provider       provider.Provider
	QueueCapacity  int
	MaxConcurrency int
	Now            func() time.Time
}

type Runtime struct {
	repository    *platformdb.AIRepository
	provider      provider.Provider
	queue         chan Job
	now           func() time.Time
	stop          chan struct{}
	done          chan struct{}
	workers       sync.WaitGroup
	closed        atomic.Bool
	scheduled     atomic.Uint64
	completed     atomic.Uint64
	failed        atomic.Uint64
	queueDropped  atomic.Uint64
	providerCalls atomic.Uint64
}

func New(config Config) *Runtime {
	if config.QueueCapacity <= 0 {
		config.QueueCapacity = 64
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = 2
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	r := &Runtime{repository: config.Repository, provider: config.Provider, queue: make(chan Job, config.QueueCapacity), now: config.Now, stop: make(chan struct{}), done: make(chan struct{})}
	if r.repository != nil {
		_ = r.repository.RecoverDecisions(context.Background(), r.now())
	}
	for i := 0; i < config.MaxConcurrency; i++ {
		r.workers.Add(1)
		go r.worker()
	}
	go func() { r.workers.Wait(); close(r.done) }()
	return r
}

// Schedule is deliberately non-blocking. It persists the public normalized
// input before enqueueing; a storage failure means no provider call is made.
func (r *Runtime) Schedule(ctx context.Context, event domain.NormalizedEvent, release classifier.Release, routes []Route) error {
	if r == nil || r.closed.Load() {
		return ErrClosed
	}
	if r.repository == nil {
		return platformdb.ErrAIUnavailable
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}
	if event.ID == "" {
		event.ID = event.NormalizedEventID
	}
	if event.NormalizedEventID == "" {
		event.NormalizedEventID = event.ID
	}
	if err := r.repository.PutNormalizedEvent(ctx, event); err != nil {
		return err
	}
	job := Job{Event: event, Release: release, Routes: cloneRoutes(routes)}
	needsRelease := false
	supportedPlatform := isSupportedPlatform(event.Platform)
	for index := range job.Routes {
		if job.Routes[index].Mode.Mode == "" || job.Routes[index].Mode.Mode == policy.ModeInherit {
			job.Routes[index].Mode = policy.ResolveAIMode()
		}
		if job.Routes[index].RouteObservationID == "" {
			// A route observation id is normally supplied by Phase 2.  The
			// deterministic fallback keeps route evaluations distinct in direct
			// callers/tests without inventing a second durable route identity.
			job.Routes[index].RouteObservationID = fmt.Sprintf("shadow:%s:%d", event.ID, index)
		}
		if supportedPlatform && job.Routes[index].Eligible && job.Routes[index].Mode.Mode == policy.ModeShadow {
			needsRelease = true
		}
	}
	// OFF and ineligible routes are still recorded as safe route evaluations and
	// must not require an active provider/release. Only an eligible SHADOW route
	// needs the durable classifier release used for the at-most-once claim.
	if needsRelease && job.Release.ID == "" {
		var err error
		job.Release, err = r.repository.ActiveRelease(ctx)
		if err != nil {
			// A provider may be intentionally unconfigured or disabled. Keep the
			// observation asynchronous and durable in that case: the worker will
			// record a provider_unavailable route evaluation without ever making a
			// model call. Other repository failures still stop before enqueueing so
			// the at-most-once claim cannot be bypassed.
			if !errors.Is(err, platformdb.ErrReleaseNotFound) {
				return err
			}
			job.Release = classifier.Release{}
		}
	}
	select {
	case <-r.stop:
		return ErrClosed
	case r.queue <- job:
		r.scheduled.Add(1)
		return nil
	default:
		r.queueDropped.Add(1)
		if needsRelease && job.Release.ID != "" {
			// Queue rejection is itself a durable, fail-open observation. It does
			// not claim a provider call and never overwrites an already running or
			// completed event/release decision.
			_ = r.repository.RecordQueueDropped(ctx, job.Event.ID, job.Release.ID, string(policy.ModeShadow), r.now())
		}
		return ErrQueueFull
	}
}

// ScheduleObserved is the integration seam from the Phase 2 public
// observation record into Shadow. Normalization is deterministic and has no
// network/credential access; a normalization failure simply prevents a model
// call and leaves Legacy delivery untouched.
func (r *Runtime) ScheduleObserved(ctx context.Context, observed platformdb.ObservedEventRecord, routes []Route) error {
	event, err := normalizer.NormalizeObserved(observed)
	if err != nil {
		// Unsupported or malformed public snapshots are still useful operational
		// facts. Persist only a safe PASS route evaluation; no normalized payload
		// or provider request is fabricated from an invalid source.
		if r != nil && r.repository != nil {
			for _, route := range routes {
				if route.Mode.Mode == "" || route.Mode.Mode == policy.ModeInherit {
					route.Mode = policy.ResolveAIMode()
				}
				_ = r.repository.PutRouteEvaluation(ctx, offRouteEvaluationRecord(Job{}, route, "", "pass", "pass", "normalization_error", route.Mode.Provenance, r.now()))
			}
		}
		return err
	}
	return r.Schedule(ctx, event, classifier.Release{}, routes)
}

func (r *Runtime) worker() {
	defer r.workers.Done()
	for {
		select {
		case job := <-r.queue:
			r.process(job)
		case <-r.stop:
			return
		}
	}
}

func (r *Runtime) process(job Job) {
	if !isSupportedPlatform(job.Event.Platform) {
		for _, route := range job.Routes {
			_ = r.repository.PutRouteEvaluation(context.Background(), offRouteEvaluationRecord(job, route, "", "pass", "pass", "unsupported_platform", route.Mode.Provenance, r.now()))
		}
		r.completed.Add(1)
		return
	}
	eligible := false
	for _, route := range job.Routes {
		if route.Eligible && route.Mode.Mode == policy.ModeShadow {
			eligible = true
			break
		}
	}
	if !eligible {
		for _, route := range job.Routes {
			reason := "ai_mode_off"
			if !route.Eligible {
				reason = "legacy_route_ineligible"
			}
			_ = r.repository.PutRouteEvaluation(context.Background(), offRouteEvaluationRecord(job, route, "", "pass", "pass", reason, nil, r.now()))
		}
		r.completed.Add(1)
		return
	}
	if job.Release.ID == "" {
		for _, route := range job.Routes {
			if !route.Eligible || route.Mode.Mode == policy.ModeOff || route.Mode.Mode == policy.ModeInherit {
				_ = r.repository.PutRouteEvaluation(context.Background(), offRouteEvaluationRecord(job, route, "", "pass", "pass", "ai_mode_off_or_ineligible", route.Mode.Provenance, r.now()))
				continue
			}
			_ = r.repository.PutRouteEvaluation(context.Background(), offRouteEvaluationRecord(job, route, "", "pass", "pass", "provider_unavailable", route.Mode.Provenance, r.now()))
		}
		r.completed.Add(1)
		return
	}
	decision, claimed, err := r.repository.EnsureDecisionClaim(context.Background(), job.Event.ID, job.Release.ID, "shadow", job.Event.ObservedAt)
	if err != nil {
		r.failed.Add(1)
		return
	}
	if claimed {
		if err := r.repository.MarkDecisionCallStarted(context.Background(), decision.ID, r.now()); err != nil {
			r.failed.Add(1)
			return
		}
		r.providerCalls.Add(1)
		started := r.now()
		if r.provider == nil {
			_ = r.repository.FinishDecision(context.Background(), decision.ID, classifier.StatusProviderUnavailable, classifier.Classification{}, "pass", "provider_unavailable", "", job.Release.Model, "provider_unavailable", classifier.Usage{}, nil, "", ptrInt64(r.now().Sub(started).Milliseconds()), r.now())
		} else {
			result, usage, callErr := r.provider.Classify(context.Background(), job.Event)
			if callErr == nil {
				if usageErr := usage.Validate(); usageErr != nil {
					callErr = provider.ErrInvalidResponse
					usage = classifier.Usage{}
				}
			}
			latency := ptrInt64(r.now().Sub(started).Milliseconds())
			if callErr != nil {
				status := classifier.StatusProviderError
				if errors.Is(callErr, context.DeadlineExceeded) {
					status = classifier.StatusTimeout
				}
				hardReason := "provider_error"
				if errors.Is(callErr, provider.ErrParse) {
					status = classifier.StatusParseError
					hardReason = "parse_error"
				}
				_ = r.repository.FinishDecision(context.Background(), decision.ID, status, classifier.Classification{}, "pass", hardReason, "openai_compatible", job.Release.Model, provider.StableErrorCode(callErr), usage, nil, job.Release.PricingCurrency, latency, r.now())
			} else {
				suggested := string(policy.ClassificationSuggestedAction(result))
				_ = r.repository.FinishDecision(context.Background(), decision.ID, classifier.StatusCompleted, result, suggested, "", "openai_compatible", job.Release.Model, "", usage, costFor(job.Release, usage), job.Release.PricingCurrency, latency, r.now())
			}
		}
	}
	decision, err = r.repository.Decision(context.Background(), decision.ID)
	if err != nil {
		r.failed.Add(1)
		return
	}
	if !claimed && (decision.Status == classifier.StatusQueued || decision.Status == classifier.StatusRunning) {
		// Another worker owns the irreversible call. Wait only for that durable
		// decision to reach a terminal state, then evaluate this job's routes
		// against the shared result. Without this bounded hand-off, route hooks
		// that schedule one target at a time could lose evaluations whenever a
		// second worker observes the in-flight row before the first finishes.
		decision, err = r.waitForTerminalDecision(decision.ID, 30*time.Second)
		if err != nil {
			r.failed.Add(1)
			return
		}
	}
	for _, route := range job.Routes {
		if !route.Eligible || route.Mode.Mode == policy.ModeOff || route.Mode.Mode == policy.ModeInherit {
			_ = r.repository.PutRouteEvaluation(context.Background(), offRouteEvaluationRecord(job, route, "", "pass", "pass", "ai_mode_off_or_ineligible", nil, r.now()))
			continue
		}
		policyContext := route.Policy
		policyContext.Truncated = job.Event.Truncated || hasNormalizationFlag(job.Event.NormalizationFlags, domain.FlagTruncated)
		resolved := policy.ResolvePolicy(decision.Classification, policyContext)
		if decision.Status != classifier.StatusCompleted {
			resolved = policy.ShadowDecision{SuggestedAction: policy.ActionPass, EffectiveAction: policy.RoutePass, Reason: "ai_error", HardPassReason: "ai_error", Provenance: map[string]string{"action": "ai_error"}}
		}
		_ = r.repository.PutRouteEvaluation(context.Background(), RouteEvaluationRecordFrom(job, route, decision, resolved, r.now()))
	}
	r.completed.Add(1)
}

func (r *Runtime) waitForTerminalDecision(id string, maxWait time.Duration) (classifier.Decision, error) {
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		decision, err := r.repository.Decision(context.Background(), id)
		if err != nil {
			return classifier.Decision{}, err
		}
		if decision.Status != classifier.StatusQueued && decision.Status != classifier.StatusRunning {
			return decision, nil
		}
		select {
		case <-deadline.C:
			return classifier.Decision{}, context.DeadlineExceeded
		case <-ticker.C:
		}
	}
}

func hasNormalizationFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if flag == want {
			return true
		}
	}
	return false
}

func (r *Runtime) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{QueueCapacity: cap(r.queue), QueueDepth: len(r.queue), Scheduled: r.scheduled.Load(), Completed: r.completed.Load(), Failed: r.failed.Load(), QueueDropped: r.queueDropped.Load(), ProviderCalls: r.providerCalls.Load()}
}
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if r.closed.CompareAndSwap(false, true) {
		close(r.stop)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cloneRoutes(values []Route) []Route {
	result := make([]Route, len(values))
	copy(result, values)
	return result
}
func ptrInt64(value int64) *int64 { return &value }
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
		value += in * release.InputPriceMicrosPerMillion / 1000000
	}
	if release.OutputPriceMicrosPerMillion > 0 {
		value += out * release.OutputPriceMicrosPerMillion / 1000000
	}
	return &value
}

func RouteEvaluationRecordFrom(job Job, route Route, decision classifier.Decision, resolved policy.ShadowDecision, createdAt time.Time) platformdb.RouteEvaluationRecord {
	provenance := mergeProvenance(route.Mode.Provenance, resolved.Provenance)
	if _, ok := provenance["mode"]; !ok {
		provenance["mode"] = string(route.Mode.Mode)
	}
	return platformdb.RouteEvaluationRecord{RouteObservationID: route.RouteObservationID, DecisionID: decision.ID, EffectiveMode: string(route.Mode.Mode), EffectiveProfileID: route.Policy.Profile.ID, ClassificationSuggestedAction: decision.SuggestedAction, PolicySuggestedAction: string(resolved.SuggestedAction), EffectiveAction: "pass", HardPassReason: resolved.HardPassReason, PolicyProvenance: provenance, CreatedAt: createdAt}
}
func offRouteEvaluationRecord(job Job, route Route, decisionID, classSuggested, policySuggested, reason string, provenance map[string]string, createdAt time.Time) platformdb.RouteEvaluationRecord {
	merged := mergeProvenance(route.Mode.Provenance, provenance)
	if _, ok := merged["mode"]; !ok {
		merged["mode"] = string(route.Mode.Mode)
	}
	return platformdb.RouteEvaluationRecord{RouteObservationID: route.RouteObservationID, DecisionID: decisionID, EffectiveMode: string(route.Mode.Mode), EffectiveProfileID: route.Policy.Profile.ID, ClassificationSuggestedAction: classSuggested, PolicySuggestedAction: policySuggested, EffectiveAction: "pass", HardPassReason: reason, PolicyProvenance: merged, CreatedAt: createdAt}
}

func isSupportedPlatform(value domain.Platform) bool {
	return value == domain.PlatformBilibili || value == domain.PlatformTwitter
}

func mergeProvenance(mode, action map[string]string) map[string]string {
	result := make(map[string]string, len(mode)+len(action)+1)
	for key, value := range mode {
		result[key] = value
	}
	for key, value := range action {
		result[key] = value
	}
	return result
}
