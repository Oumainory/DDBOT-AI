// Package enforce owns the Phase 5 authoritative route decision boundary.
// It is an optional, fail-open layer: callers retain the Legacy renderer and
// Messenger and suppress them only after PersistDrop has committed.
package enforce

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
	"github.com/Oumainory/DDBOT-AI/internal/provider"
	"github.com/Oumainory/DDBOT-AI/internal/replay"
)

var (
	ErrClosed    = errors.New("enforce: runtime closed")
	ErrQueueFull = errors.New("enforce: queue full")
)

type Target struct {
	ID          string
	Type        string
	ExternalID  string
	ConnectorID string
}

type Route struct {
	RouteObservationID string
	ObservedEventID    string
	SourceID           string
	Target             Target
	SubscriptionID     string
	Mode               policy.Mode
	Policy             policy.PolicyContext
	Release            classifier.Release
}

type Decision struct {
	RouteDecisionID   string
	AIDecisionID      string
	EnforceApprovalID string
	Action            policy.RouteAction
	Reason            string
	HardPassReason    string
	Classification    classifier.Classification
	SuppressSend      bool
	Persisted         bool
}

type Config struct {
	AIRepository   *platformdb.AIRepository
	Repository     *platformdb.Phase5Repository
	Provider       provider.Provider
	QueueCapacity  int
	MaxConcurrency int
	Timeout        time.Duration
	Now            func() time.Time
	// Readiness is injectable for deterministic tests, but production defaults
	// to the durable AIRepository gate. A true result never bypasses approval.
	Readiness func(context.Context, classifier.Release) (bool, error)
	// DecisionRecoveryError is set by the platform bootstrap when the durable
	// AI decision recovery pass could not complete.  ENFORCE must remain
	// fail-open until a later process restart can establish that invariant; a
	// transient recovery failure must never allow new provider calls.
	DecisionRecoveryError error
	// CacheMedia is an optional, bounded best-effort enhancement invoked only
	// after a DROP transaction has committed. Its failure is deliberately
	// ignored so media availability can never change the authoritative action.
	CacheMedia            func(context.Context, replay.Snapshot) error
	MediaCacheConcurrency int
	MediaCacheTimeout     time.Duration
}

type flightResult struct {
	decision classifier.Decision
	err      error
}
type flight struct {
	done   chan struct{}
	result flightResult
}

type Runtime struct {
	ai           *platformdb.AIRepository
	repo         *platformdb.Phase5Repository
	provider     provider.Provider
	now          func() time.Time
	timeout      time.Duration
	gate         chan struct{}
	queueCap     int
	readinessFn  func(context.Context, classifier.Release) (bool, error)
	recoveryErr  bool
	cacheMedia   func(context.Context, replay.Snapshot) error
	mediaGate    chan struct{}
	mediaTimeout time.Duration
	closed       atomic.Bool
	emergency    atomic.Bool
	mu           sync.Mutex
	flights      map[string]*flight
}

func New(config Config) *Runtime {
	if config.QueueCapacity <= 0 {
		config.QueueCapacity = 64
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = 2
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.MediaCacheConcurrency <= 0 {
		config.MediaCacheConcurrency = 2
	}
	if config.MediaCacheTimeout <= 0 {
		config.MediaCacheTimeout = 10 * time.Second
	}
	return &Runtime{ai: config.AIRepository, repo: config.Repository, provider: config.Provider, now: config.Now, timeout: config.Timeout, gate: make(chan struct{}, config.MaxConcurrency), queueCap: config.QueueCapacity, readinessFn: config.Readiness, recoveryErr: config.DecisionRecoveryError != nil, cacheMedia: config.CacheMedia, mediaGate: make(chan struct{}, config.MediaCacheConcurrency), mediaTimeout: config.MediaCacheTimeout, flights: make(map[string]*flight)}
}

func (r *Runtime) SetEmergencyDisabled(disabled bool) {
	if r != nil {
		r.emergency.Store(disabled)
	}
}
func (r *Runtime) EmergencyDisabled() bool { return r != nil && r.emergency.Load() }
func (r *Runtime) Close() {
	if r != nil {
		r.closed.Store(true)
	}
}

func (r *Runtime) Ready(ctx context.Context, release classifier.Release) (bool, error) {
	if r == nil || r.repo == nil {
		return false, platformdb.ErrPhase5Unavailable
	}
	if r.recoveryErr {
		return false, platformdb.ErrEnforceNotReady
	}
	if r.emergency.Load() || r.durableEmergency(ctx) {
		return false, nil
	}
	if r.readinessFn == nil && r.ai == nil {
		return false, platformdb.ErrEnforceNotReady
	}
	if r.readinessFn != nil {
		return r.readinessFn(ctx, release)
	}
	readiness, err := r.ai.EnforceReadiness(ctx)
	if err != nil {
		return false, err
	}
	return readiness.Ready, nil
}

func (r *Runtime) durableEmergency(ctx context.Context) bool {
	if r == nil || r.repo == nil {
		return false
	}
	value, err := r.repo.EnforceEmergencyDisabled(ctx)
	return err == nil && value
}

// Evaluate resolves all routes for one event. OFF routes are immediately PASS,
// SHADOW routes are PASS (the existing Shadow runtime remains asynchronous),
// and ENFORCE routes share one bounded event/release classification.
func (r *Runtime) Evaluate(ctx context.Context, event domain.NormalizedEvent, routes []Route) ([]Decision, error) {
	if r == nil || r.closed.Load() {
		return nil, ErrClosed
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]Decision, len(routes))
	enforceIndexes := make([]int, 0)
	for i, route := range routes {
		results[i] = Decision{Action: policy.RoutePass, Reason: "ai_mode_off_or_shadow"}
		if route.Mode == policy.ModeEnforce {
			if !platformdb.ValidRouteIdentity(route.Target.ID, route.Target.Type, route.Target.ExternalID, route.Target.ConnectorID) {
				results[i].Reason = "ambiguous_route"
				r.persistPassWithDecision(ctx, event, route, &results[i], results[i].Reason, "")
				continue
			}
			enforceIndexes = append(enforceIndexes, i)
		}
	}
	if len(enforceIndexes) == 0 {
		for i, route := range routes {
			// Invalid ENFORCE identities were already recorded as an
			// authoritative PASS above. Do not write a second decision when
			// an event contains only ambiguous routes.
			if route.Mode == policy.ModeEnforce && results[i].Reason == "ambiguous_route" {
				continue
			}
			r.persistPass(ctx, event, route, &results[i], "ai_mode_"+string(route.Mode))
		}
		return results, nil
	}
	if r.recoveryErr {
		for _, i := range enforceIndexes {
			results[i].Reason = "enforce_recovery_unavailable"
			r.persistPass(ctx, event, routes[i], &results[i], results[i].Reason)
		}
		return results, nil
	}
	if r.emergency.Load() || r.durableEmergency(ctx) {
		for i, route := range routes {
			if route.Mode == policy.ModeEnforce {
				results[i].Reason = "emergency_enforce_disabled"
				r.persistPass(ctx, event, route, &results[i], results[i].Reason)
			}
		}
		return results, nil
	}
	if !r.tryAcquire() {
		for i, route := range routes {
			if route.Mode == policy.ModeEnforce {
				results[i].Reason = "fail_open_queue_full"
				r.persistPass(ctx, event, route, &results[i], results[i].Reason)
			}
		}
		return results, nil
	}
	defer r.release()
	// Resolve the durable active release for every ENFORCE evaluation.  A
	// caller-supplied release is only an observation of the route snapshot; it
	// must never be allowed to make a stale classifier release authoritative.
	// Resolving once also preserves the event-level sharing guarantee.
	sharedRelease := routes[enforceIndexes[0]].Release
	if r.ai == nil {
		for _, i := range enforceIndexes {
			results[i].Reason = "enforce_storage_error"
			r.persistPass(ctx, event, routes[i], &results[i], results[i].Reason)
		}
		return results, nil
	}
	activeRelease, releaseErr := r.ai.ActiveRelease(ctx)
	if releaseErr != nil || activeRelease.ID == "" {
		for _, i := range enforceIndexes {
			results[i].Reason = "enforce_storage_error"
			r.persistPass(ctx, event, routes[i], &results[i], results[i].Reason)
		}
		return results, nil
	}
	if sharedRelease.ID != "" && sharedRelease.ID != activeRelease.ID {
		for _, i := range enforceIndexes {
			results[i].Reason = "classifier_release_stale"
			r.persistPass(ctx, event, routes[i], &results[i], results[i].Reason)
		}
		return results, nil
	}
	sharedRelease = activeRelease
	ready, readyErr := r.readiness(ctx, sharedRelease)
	if readyErr != nil || !ready {
		reason := "enforce_not_ready"
		if readyErr != nil {
			reason = "enforce_storage_error"
		}
		for _, i := range enforceIndexes {
			results[i].Reason = reason
			r.persistPass(ctx, event, routes[i], &results[i], reason)
		}
		return results, nil
	}
	decision, err := r.classify(ctx, event, sharedRelease)
	if err != nil {
		for _, i := range enforceIndexes {
			results[i].Reason = "ai_error"
			if errors.Is(err, ErrQueueFull) {
				results[i].Reason = "fail_open_queue_full"
			}
			r.persistPass(ctx, event, routes[i], &results[i], results[i].Reason)
		}
		return results, nil
	}
	for _, i := range enforceIndexes {
		route := routes[i]
		route.Release = sharedRelease
		result := decision.Classification
		policyCtx := route.Policy
		// Normalizer safety flags are part of the public event contract. Carry
		// them into the route policy even when a caller supplied a sparse policy
		// context; truncation or an equivalent unsafe flag must never be turned
		// into DROP by an otherwise permissive profile.
		if event.Truncated || hasUnsafeContextFlag(event.NormalizationFlags) {
			policyCtx.Truncated = true
		}
		policyDecision := policy.ResolvePolicy(result, policyCtx)
		out := Decision{AIDecisionID: decision.ID, Action: policy.RoutePass, Reason: policyDecision.Reason, HardPassReason: policyDecision.HardPassReason, Classification: result}
		if decision.Status != classifier.StatusCompleted {
			out.Reason = "ai_error"
			out.HardPassReason = "ai_error"
		} else if policyDecision.SuggestedAction == policy.ActionDrop && policyDecision.HardPassReason == "" {
			approval, err := r.approval(ctx, route, sharedRelease)
			if err != nil {
				out.Reason = "enforce_approval_invalid"
				out.HardPassReason = "approval_invalid"
			} else {
				out.EnforceApprovalID = approval.ID
				out.Action = policy.RouteDrop
				out.SuppressSend = true
				out.Reason = policyDecision.Reason
				out.Persisted = r.persistDrop(ctx, event, route, decision, &out)
			}
		}
		if !out.Persisted {
			if out.Action == policy.RouteDrop {
				out.Action = policy.RoutePass
				out.SuppressSend = false
				// The policy recommendation was DROP, but the durable final
				// boundary rejected it (for example because an operator disabled
				// ENFORCE or revoked the approval while the provider was running).
				// Do not persist a misleading enforce/category-drop reason for the
				// resulting PASS; make the fail-open transition explicit so the
				// durable route is recorded with effective_mode=shadow.
				out.Reason = "fail_open_storage"
			}
			r.persistPassWithDecision(ctx, event, route, &out, out.Reason, decision.ID)
		}
		results[i] = out
	}
	// Non-Enforce routes are never held by this runtime and stay PASS.
	for i, route := range routes {
		if route.Mode != policy.ModeEnforce {
			r.persistPass(ctx, event, route, &results[i], "ai_mode_"+string(route.Mode))
		}
	}
	return results, nil
}

func hasUnsafeContextFlag(flags []string) bool {
	for _, flag := range flags {
		switch strings.TrimSpace(strings.ToLower(flag)) {
		case domain.FlagTruncated, domain.FlagInsufficientContext, domain.FlagPromptInjectionSuspect:
			return true
		}
	}
	return false
}

func (r *Runtime) tryAcquire() bool {
	select {
	case r.gate <- struct{}{}:
		return true
	default:
		return false
	}
}
func (r *Runtime) release() {
	select {
	case <-r.gate:
	default:
	}
}

func (r *Runtime) readiness(ctx context.Context, release classifier.Release) (bool, error) {
	if r != nil && r.readinessFn != nil {
		return r.readinessFn(ctx, release)
	}
	if r.ai == nil {
		return false, platformdb.ErrEnforceNotReady
	}
	readiness, err := r.ai.EnforceReadiness(ctx)
	if err != nil {
		return false, err
	}
	return readiness.Ready, nil
}

func (r *Runtime) approval(ctx context.Context, route Route, release classifier.Release) (platformdb.EnforceApprovalRecord, error) {
	if r.repo == nil {
		return platformdb.EnforceApprovalRecord{}, platformdb.ErrApprovalNotFound
	}
	effective := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: route.Policy.Threshold, CategoryActions: route.Policy.Profile.CategoryActions, TagActions: route.Policy.Profile.TagActions}
	return r.repo.ValidEnforceApproval(ctx, release.ID, policy.Digest(effective), policy.ProfileDigest(route.Policy.Profile), r.now())
}

func (r *Runtime) persistDrop(ctx context.Context, event domain.NormalizedEvent, route Route, aiDecision classifier.Decision, out *Decision) bool {
	if r.repo == nil {
		return false
	}
	id := newDecisionID()
	snap, err := replay.FromNormalizedEvent(event, id, replay.TargetIdentity{TargetID: route.Target.ID, TargetType: route.Target.Type, ExternalID: route.Target.ExternalID, ConnectorID: route.Target.ConnectorID}, route.SubscriptionID, aiDecision.ID, r.now())
	if err != nil {
		return false
	}
	// The normalized classifier input retains the upstream source external
	// identity, but the durable route/replay identity must use the same Domain
	// Source UUID that the production bridge resolved for policy lookup. Keep
	// the public template input untouched while aligning the snapshot envelope
	// with RouteDecision.SourceID so the final atomic DROP validation cannot
	// reject an otherwise valid durable route.
	if strings.TrimSpace(route.SourceID) != "" {
		snap.SourceID = strings.TrimSpace(route.SourceID)
	}
	raw, err := snap.Marshal()
	if err != nil {
		return false
	}
	effectivePolicy := policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: route.Policy.Threshold, CategoryActions: route.Policy.Profile.CategoryActions, TagActions: route.Policy.Profile.TagActions}
	policyDigest, profileDigest := policy.Digest(effectivePolicy), policy.ProfileDigest(route.Policy.Profile)
	record := platformdb.RouteDecisionRecord{ID: id, EventID: event.ID, ObservedEventID: first(route.ObservedEventID, event.ObservedEventID), RouteObservationID: route.RouteObservationID, SourceID: first(route.SourceID, event.SourceID), TargetID: route.Target.ID, SubscriptionID: route.SubscriptionID, ClassifierReleaseID: aiDecision.ClassifierReleaseID, AIDecisionID: aiDecision.ID, ConfiguredMode: string(route.Mode), EffectiveMode: string(route.Mode), ProfileID: route.Policy.Profile.ID, PolicyDigest: policyDigest, SuggestedAction: "drop", EffectiveAction: "drop", ReasonCode: out.Reason, HardPassReason: out.HardPassReason, EnforceApprovalID: out.EnforceApprovalID, CreatedAt: r.now(), DecidedAt: r.now()}
	if err := r.repo.PersistDropIfAllowed(ctx, record, platformdb.ReplayableEventRecord{ID: "", SchemaVersion: 1, RouteDecisionID: id, EventID: event.ID, ObservedEventID: record.ObservedEventID, SourceID: record.SourceID, TargetID: route.Target.ID, SubscriptionID: route.SubscriptionID, EventType: string(event.EventType), SnapshotJSON: raw, OriginalClassificationRef: aiDecision.ID, CreatedAt: r.now(), ExpiresAt: r.now().Add(platformdb.ReplayRetention)}, out.EnforceApprovalID, policyDigest, profileDigest, r.now()); err != nil {
		return false
	}
	// The durable DROP boundary is complete.  Cache only the process-independent
	// public snapshot after that commit; a bounded best-effort task cannot make
	// the route PASS or otherwise affect the already-authoritative decision.
	r.scheduleMediaCache(snap)
	if out != nil {
		out.RouteDecisionID = id
	}
	return true
}

func (r *Runtime) scheduleMediaCache(snapshot replay.Snapshot) {
	if r == nil || r.cacheMedia == nil || r.mediaGate == nil {
		return
	}
	// Re-decode the serialized bytes so the async closure does not retain any
	// mutable NormalizedEvent or caller-owned slice/map. Marshal is already the
	// exact durable representation written by PersistDrop.
	raw, err := snapshot.Marshal()
	if err != nil {
		return
	}
	isolated, err := replay.Unmarshal(raw)
	if err != nil {
		return
	}
	select {
	case r.mediaGate <- struct{}{}:
	default:
		// There is intentionally no retry queue. A saturated enhancement lane
		// is recorded by the caller's normal cache diagnostics and never blocks
		// Legacy delivery or changes the DROP decision.
		return
	}
	go func() {
		defer func() {
			<-r.mediaGate
			_ = recover()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), r.mediaTimeout)
		defer cancel()
		_ = r.cacheMedia(ctx, isolated)
	}()
}

func (r *Runtime) persistPass(ctx context.Context, event domain.NormalizedEvent, route Route, out *Decision, reason string) {
	r.persistPassWithDecision(ctx, event, route, out, reason, "")
}
func (r *Runtime) persistPassWithDecision(ctx context.Context, event domain.NormalizedEvent, route Route, out *Decision, reason, aiID string) {
	if r.repo == nil {
		return
	}
	id := newDecisionID()
	effectiveMode := route.Mode
	if route.Mode == policy.ModeEnforce && enforceSafeLockReason(reason) {
		effectiveMode = policy.ModeShadow
	}
	if err := r.repo.PutRouteDecision(ctx, platformdb.RouteDecisionRecord{ID: id, EventID: event.ID, ObservedEventID: first(route.ObservedEventID, event.ObservedEventID), RouteObservationID: route.RouteObservationID, SourceID: first(route.SourceID, event.SourceID), TargetID: route.Target.ID, SubscriptionID: route.SubscriptionID, ClassifierReleaseID: first(route.Release.ID, ""), AIDecisionID: aiID, ConfiguredMode: string(route.Mode), EffectiveMode: string(effectiveMode), ProfileID: route.Policy.Profile.ID, PolicyDigest: policy.Digest(policy.EffectivePolicy{Mode: effectiveMode, Threshold: route.Policy.Threshold, CategoryActions: route.Policy.Profile.CategoryActions, TagActions: route.Policy.Profile.TagActions}), SuggestedAction: "pass", EffectiveAction: "pass", ReasonCode: reason, CreatedAt: r.now(), DecidedAt: r.now()}); err == nil {
		out.RouteDecisionID = id
		out.Persisted = true
	}
}

// enforceSafeLockReason identifies failures where configured ENFORCE was not
// allowed to make a decision. Recording effective_mode=shadow keeps the
// durable audit trail honest while preserving configured_mode=enforce. A
// normal policy PASS, including a hard-safety PASS after successful
// classification, remains effective ENFORCE and is not listed here.
func enforceSafeLockReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "enforce_not_ready", "enforce_storage_error", "enforce_recovery_unavailable", "enforce_approval_invalid", "fail_open_queue_full", "emergency_enforce_disabled", "ai_error", "ambiguous_route", "classifier_release_stale", "fail_open_storage", "phase5_unavailable", "observation_unavailable", "invalid_public_snapshot":
		return true
	default:
		return false
	}
}

func newDecisionID() string {
	id, err := domain.NewID()
	if err != nil {
		return "route_" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000Z"), ".", "")
	}
	return "route_" + strings.TrimPrefix(id, "-")
}
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (r *Runtime) classify(ctx context.Context, event domain.NormalizedEvent, release classifier.Release) (classifier.Decision, error) {
	if r.ai == nil || release.ID == "" || r.provider == nil {
		return classifier.Decision{Status: classifier.StatusProviderUnavailable}, nil
	}
	if err := r.ai.PutNormalizedEvent(ctx, event); err != nil {
		return classifier.Decision{}, err
	}
	key := event.ID + "\x00" + release.ID
	r.mu.Lock()
	if existing := r.flights[key]; existing != nil {
		r.mu.Unlock()
		select {
		case <-existing.done:
			return existing.result.decision, existing.result.err
		case <-ctx.Done():
			return classifier.Decision{}, ctx.Err()
		}
	}
	current := &flight{done: make(chan struct{})}
	if r.queueCap > 0 && len(r.flights) >= r.queueCap {
		r.mu.Unlock()
		return classifier.Decision{}, ErrQueueFull
	}
	r.flights[key] = current
	r.mu.Unlock()
	decision, err := r.classifyOwner(ctx, event, release)
	current.result = flightResult{decision: decision, err: err}
	close(current.done)
	r.mu.Lock()
	delete(r.flights, key)
	r.mu.Unlock()
	return decision, err
}

func (r *Runtime) classifyOwner(ctx context.Context, event domain.NormalizedEvent, release classifier.Release) (classifier.Decision, error) {
	decision, claimed, err := r.ai.EnsureDecisionClaim(ctx, event.ID, release.ID, "enforce", event.ObservedAt)
	if err != nil {
		return classifier.Decision{}, err
	}
	if !claimed {
		if decision.Status == classifier.StatusCompleted {
			return decision, nil
		}
		deadline := time.NewTimer(r.timeout)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for decision.Status == classifier.StatusQueued || decision.Status == classifier.StatusRunning {
			select {
			case <-deadline.C:
				return classifier.Decision{}, context.DeadlineExceeded
			case <-ticker.C:
				decision, err = r.ai.Decision(ctx, decision.ID)
				if err != nil {
					return classifier.Decision{}, err
				}
			}
		}
		return decision, nil
	}
	if err := r.ai.MarkDecisionCallStarted(ctx, decision.ID, r.now()); err != nil {
		return classifier.Decision{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	started := r.now()
	result, usage, callErr := r.classifyProvider(callCtx, event)
	latency := r.now().Sub(started).Milliseconds()
	if callErr != nil {
		status := classifier.StatusProviderError
		if errors.Is(callErr, context.DeadlineExceeded) {
			status = classifier.StatusTimeout
		}
		_ = r.ai.FinishDecision(ctx, decision.ID, status, classifier.Classification{}, "pass", provider.StableErrorCode(callErr), "openai_compatible", release.Model, provider.StableErrorCode(callErr), usage, nil, release.PricingCurrency, &latency, r.now())
		return r.ai.Decision(ctx, decision.ID)
	}
	if usage.Validate() != nil {
		callErr = provider.ErrInvalidResponse
		_ = r.ai.FinishDecision(ctx, decision.ID, classifier.StatusProviderError, classifier.Classification{}, "pass", "invalid_usage", "openai_compatible", release.Model, "invalid_usage", classifier.Usage{}, nil, release.PricingCurrency, &latency, r.now())
		return r.ai.Decision(ctx, decision.ID)
	}
	suggested := string(policy.ClassificationSuggestedAction(result))
	_ = r.ai.FinishDecision(ctx, decision.ID, classifier.StatusCompleted, result, suggested, "", "openai_compatible", release.Model, "", usage, nil, release.PricingCurrency, &latency, r.now())
	return r.ai.Decision(ctx, decision.ID)
}

// classifyProvider converts a provider panic into an ordinary provider error.
// Provider implementations are optional extensions and are outside the
// authoritative runtime; a panic must therefore follow the same fail-open
// path as a timeout, transport error, or malformed response.
func (r *Runtime) classifyProvider(ctx context.Context, event domain.NormalizedEvent) (result classifier.Classification, usage classifier.Usage, err error) {
	if r == nil || r.provider == nil {
		return classifier.Classification{}, classifier.Usage{}, platformdb.ErrPhase5Unavailable
	}
	defer func() {
		if recover() != nil {
			result = classifier.Classification{}
			usage = classifier.Usage{}
			err = errors.New("classifier provider panic")
		}
	}()
	return r.provider.Classify(ctx, event)
}
