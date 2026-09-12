package observation

import (
	"context"
	"fmt"
	"io"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

// Repository is implemented by platformdb. Keeping this interface here lets
// recorder tests use a blocked or failing fake without opening SQLite.
type Repository interface {
	InsertObservedEvent(context.Context, platformdb.ObservedEventRecord) error
	InsertRouteObservation(context.Context, platformdb.RouteObservationRecord) error
	InsertDeliveryObservation(context.Context, platformdb.DeliveryObservationRecord) error
	PruneObservationsBefore(context.Context, time.Time, int) (int, error)
}

type Config struct {
	QueueSize         int
	Random            io.Reader
	Now               func() time.Time
	Retention         time.Duration
	BatchSize         int
	PruneInterval     time.Duration
	PruneInitialDelay time.Duration
	Log               func(component, class string, count uint64)
	// RouteHook runs only after a route observation has been durably accepted.
	// It is an auxiliary Phase 4 seam: implementations must remain bounded and
	// must never be used to decide whether Legacy sends a message.
	RouteHook         RouteHook
}

type RouteHook func(context.Context, platformdb.RouteObservationRecord)

func (c Config) normalized() Config {
	if c.QueueSize <= 0 {
		c.QueueSize = DefaultQueueSize
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Retention <= 0 {
		c.Retention = DefaultRetention
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 256
	}
	if c.PruneInterval <= 0 {
		c.PruneInterval = 24 * time.Hour
	}
	if c.PruneInitialDelay <= 0 {
		c.PruneInitialDelay = c.PruneInterval
	}
	if c.Log == nil {
		c.Log = func(component, class string, count uint64) {
			log.Printf("observation %s %s count=%d", component, class, count)
		}
	}
	return c
}

type Stats struct {
	QueueCapacity      int
	QueueDepth         int
	EventsAccepted     uint64
	RoutesAccepted     uint64
	DeliveriesAccepted uint64
	QueueDropped       uint64
	PersistenceErrors  uint64
	WorkerPanics       uint64
	PruneErrors        uint64
}

type counters struct {
	eventsAccepted     atomic.Uint64
	routesAccepted     atomic.Uint64
	deliveriesAccepted atomic.Uint64
	queueDropped       atomic.Uint64
	persistenceErrors  atomic.Uint64
	workerPanics       atomic.Uint64
	pruneErrors        atomic.Uint64
}

type queueItem struct {
	kind     string
	event    platformdb.ObservedEventRecord
	route    platformdb.RouteObservationRecord
	delivery platformdb.DeliveryObservationRecord
}

// Recorder is a bounded, single-worker best-effort persistence queue.
// TryObserve* never waits for SQLite and never returns a storage error to the
// Legacy caller; false means the observation was dropped.
type Recorder struct {
	repository  Repository
	queue       chan queueItem
	stop        chan struct{}
	done        chan struct{}
	workerCtx   context.Context
	cancel      context.CancelFunc
	janitorDone chan struct{}
	config      Config
	ids         idGenerator
	counters    counters

	acceptMu  sync.RWMutex
	accepting bool
	closeOnce sync.Once
	hookMu    sync.RWMutex
	routeHook RouteHook
}

func NewRecorder(repository Repository, config Config) *Recorder {
	config = config.normalized()
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &Recorder{
		repository:  repository,
		queue:       make(chan queueItem, config.QueueSize),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		janitorDone: make(chan struct{}),
		workerCtx:   ctx,
		cancel:      cancel,
		config:      config,
		ids:         idGenerator{random: config.Random},
		accepting:   repository != nil,
		routeHook:  config.RouteHook,
	}
	go recorder.worker()
	go recorder.retentionLoop()
	return recorder
}

// SetRouteHook installs the optional post-persistence observation seam. It is
// safe to call during platform wiring before Legacy starts accepting events;
// replacing it never changes the recorder's Legacy-facing behavior.
func (r *Recorder) SetRouteHook(hook RouteHook) {
	if r == nil {
		return
	}
	r.hookMu.Lock()
	r.routeHook = hook
	r.hookMu.Unlock()
}

func (r *Recorder) TryObserveEvent(input EventInput) (Trace, bool) {
	if r == nil || r.repository == nil {
		return Trace{}, false
	}
	input = cloneEventInput(input)
	input.Platform = strings.TrimSpace(input.Platform)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.EventType = strings.TrimSpace(input.EventType)
	if input.Platform == "" || input.SourceKind == "" || input.EventType == "" {
		return Trace{}, false
	}
	if input.ObservedAt.IsZero() {
		input.ObservedAt = r.config.Now().UTC()
	} else {
		input.ObservedAt = input.ObservedAt.UTC()
	}
	if input.SourceEventAt.IsZero() {
		// Zero remains NULL in the durable row. Do not invent a source time.
	} else {
		input.SourceEventAt = input.SourceEventAt.UTC()
	}
	snapshotJSON, fingerprint := makePublicSnapshot(input)
	if snapshotJSON == "" || fingerprint == "" {
		return Trace{}, false
	}
	id, err := r.ids.next("evt_")
	if err != nil {
		return Trace{}, false
	}
	record := platformdb.ObservedEventRecord{
		ID:                 id,
		SchemaVersion:      ObservationSchemaVersion,
		Platform:           input.Platform,
		SourceKind:         input.SourceKind,
		SourceExternalID:   strings.TrimSpace(input.SourceExternalID),
		UpstreamEventID:    strings.TrimSpace(input.UpstreamEventID),
		EventType:          input.EventType,
		ObservedAt:         input.ObservedAt,
		ContentFingerprint: fingerprint,
		PublicSnapshotJSON: snapshotJSON,
		CreatedAt:          r.config.Now().UTC(),
	}
	if !r.enqueue(queueItem{kind: "event", event: record}) {
		return Trace{}, false
	}
	r.counters.eventsAccepted.Add(1)
	return Trace{eventObservationID: id, valid: true}, true
}

func cloneEventInput(input EventInput) EventInput {
	input.PublicMediaURLs = cloneStrings(input.PublicMediaURLs)
	return input
}

func (r *Recorder) TryObserveRoute(trace Trace, input RouteInput) (RouteTrace, bool) {
	if r == nil || r.repository == nil || !trace.Valid() {
		return RouteTrace{}, false
	}
	input.DestinationKind = strings.TrimSpace(input.DestinationKind)
	input.Outcome = normalizeRouteOutcome(input.Outcome)
	input.ReasonCode = normalizeReason(input.ReasonCode)
	if input.RouteOrdinal < 0 || input.DestinationKind == "" || input.DestinationExternalID == "" || input.Outcome == "" {
		return RouteTrace{}, false
	}
	if input.ObservedAt.IsZero() {
		input.ObservedAt = r.config.Now().UTC()
	} else {
		input.ObservedAt = input.ObservedAt.UTC()
	}
	id, err := r.ids.next("route_")
	if err != nil {
		return RouteTrace{}, false
	}
	record := platformdb.RouteObservationRecord{
		ID:                    id,
		EventID:               trace.eventObservationID,
		RouteOrdinal:          input.RouteOrdinal,
		DestinationKind:       input.DestinationKind,
		DestinationExternalID: input.DestinationExternalID,
		Outcome:               input.Outcome,
		ReasonCode:            input.ReasonCode,
		ObservedAt:            input.ObservedAt,
		CreatedAt:             r.config.Now().UTC(),
	}
	if !r.enqueue(queueItem{kind: "route", route: record}) {
		return RouteTrace{}, false
	}
	r.counters.routesAccepted.Add(1)
	return RouteTrace{eventObservationID: trace.eventObservationID, routeObservationID: id, destinationID: input.DestinationExternalID, valid: true}, true
}

func (r *Recorder) ObserveDelivery(trace RouteTrace, input DeliveryInput) bool {
	if r == nil || r.repository == nil || !trace.Valid() {
		return false
	}
	input.ConnectorKind = strings.TrimSpace(input.ConnectorKind)
	input.Status = normalizeDeliveryStatus(input.Status)
	input.ResultCode = normalizeReason(input.ResultCode)
	if input.ConnectorKind == "" || input.DestinationExternalID == "" || input.Status == "" {
		return false
	}
	if input.ObservedAt.IsZero() {
		input.ObservedAt = r.config.Now().UTC()
	} else {
		input.ObservedAt = input.ObservedAt.UTC()
	}
	id, err := r.ids.next("del_")
	if err != nil {
		return false
	}
	record := platformdb.DeliveryObservationRecord{
		ID:                    id,
		EventID:               trace.eventObservationID,
		RouteObservationID:    trace.routeObservationID,
		ConnectorKind:         input.ConnectorKind,
		DestinationExternalID: input.DestinationExternalID,
		Status:                input.Status,
		ResultCode:            input.ResultCode,
		ObservedAt:            input.ObservedAt,
		CreatedAt:             r.config.Now().UTC(),
	}
	if !r.enqueue(queueItem{kind: "delivery", delivery: record}) {
		return false
	}
	r.counters.deliveriesAccepted.Add(1)
	return true
}

func (r *Recorder) enqueue(item queueItem) bool {
	r.acceptMu.RLock()
	defer r.acceptMu.RUnlock()
	if !r.accepting {
		return false
	}
	select {
	case r.queue <- item:
		return true
	default:
		r.counters.queueDropped.Add(1)
		return false
	}
}

func (r *Recorder) worker() {
	defer close(r.done)
	for {
		select {
		case item := <-r.queue:
			r.process(item)
		case <-r.stop:
			r.drain()
			return
		}
	}
}

// retentionLoop runs delayed, bounded maintenance so startup never blocks on
// a potentially large table. It is deliberately independent from readiness;
// errors are counted and reported through the existing sanitized log seam.
func (r *Recorder) retentionLoop() {
	defer close(r.janitorDone)
	timer := time.NewTimer(r.config.PruneInitialDelay)
	defer timer.Stop()
	select {
	case <-r.stop:
		return
	case <-timer.C:
	}
	r.pruneAndRecord()
	ticker := time.NewTicker(r.config.PruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.pruneAndRecord()
		}
	}
}

func (r *Recorder) pruneAndRecord() {
	if _, err := r.PruneOnce(r.workerCtx, r.config.Now()); err != nil {
		count := r.counters.pruneErrors.Add(1)
		r.log("retention", "prune_error", count)
	}
}

func (r *Recorder) drain() {
	for {
		select {
		case item := <-r.queue:
			r.process(item)
		default:
			return
		}
	}
}

func (r *Recorder) process(item queueItem) {
	defer func() {
		if recovered := recover(); recovered != nil {
			count := r.counters.workerPanics.Add(1)
			r.log("worker", "panic", count)
		}
	}()
	var err error
	switch item.kind {
	case "event":
		err = r.repository.InsertObservedEvent(r.workerCtx, item.event)
	case "route":
		err = r.repository.InsertRouteObservation(r.workerCtx, item.route)
	case "delivery":
		err = r.repository.InsertDeliveryObservation(r.workerCtx, item.delivery)
	default:
		return
	}
	if err != nil {
		count := r.counters.persistenceErrors.Add(1)
		r.log(item.kind, "persistence_error", count)
		return
	}
	if item.kind == "route" {
		r.hookMu.RLock()
		hook := r.routeHook
		r.hookMu.RUnlock()
		if hook != nil {
			// The hook is deliberately invoked after the durable route write. A
			// panic is contained by the worker guard above and never reaches the
			// Legacy caller.
			hook(r.workerCtx, item.route)
		}
	}
}

func (r *Recorder) log(component, class string, count uint64) {
	if r != nil && r.config.Log != nil {
		// The callback receives only stable component/class/count values; it is
		// never passed an event, target, SQL, or error string.
		r.config.Log(component, class, count)
	}
}

func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{
		QueueCapacity:      cap(r.queue),
		QueueDepth:         len(r.queue),
		EventsAccepted:     r.counters.eventsAccepted.Load(),
		RoutesAccepted:     r.counters.routesAccepted.Load(),
		DeliveriesAccepted: r.counters.deliveriesAccepted.Load(),
		QueueDropped:       r.counters.queueDropped.Load(),
		PersistenceErrors:  r.counters.persistenceErrors.Load(),
		WorkerPanics:       r.counters.workerPanics.Load(),
		PruneErrors:        r.counters.pruneErrors.Load(),
	}
}

// PruneOnce is best-effort retention maintenance. It is intentionally not
// part of readiness and does not retry failed database operations.
func (r *Recorder) PruneOnce(ctx context.Context, now time.Time) (int, error) {
	if r == nil || r.repository == nil {
		return 0, nil
	}
	if now.IsZero() {
		now = r.config.Now()
	}
	return r.repository.PruneObservationsBefore(ctx, now.Add(-r.config.Retention), r.config.BatchSize)
}

// Close stops accepting facts and drains the bounded queue until ctx expires.
// A timeout cancels the worker context and intentionally drops remaining
// observations rather than delaying Legacy shutdown indefinitely.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.acceptMu.Lock()
		r.accepting = false
		r.acceptMu.Unlock()
		close(r.stop)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		r.cancel()
		select {
		case <-r.janitorDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	case <-ctx.Done():
		r.cancel()
		return ctx.Err()
	}
}

func normalizeRouteOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pass", "filtered", "skipped", "unknown":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizeDeliveryStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sent", "queued", "not_sent", "unknown", "rejected", "migration_held":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizeReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	// Reason/result codes are stable labels, never raw errors or payload text.
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "unknown"
		}
	}
	return value
}

// Noop is the default observer and has no side effects.
type Noop struct{}

func (Noop) TryObserveEvent(EventInput) (Trace, bool)             { return Trace{}, false }
func (Noop) TryObserveRoute(Trace, RouteInput) (RouteTrace, bool) { return RouteTrace{}, false }
func (Noop) ObserveDelivery(RouteTrace, DeliveryInput) bool       { return false }
func (Noop) Close(context.Context) error                          { return nil }

// Sink is the tiny process-wide bridge used by Legacy hooks. It defaults to
// Noop. It contains no configuration and is swapped explicitly by the
// platform owner when a Store-backed Recorder is available.
type Sink interface {
	TryObserveEvent(EventInput) (Trace, bool)
	TryObserveRoute(Trace, RouteInput) (RouteTrace, bool)
	ObserveDelivery(RouteTrace, DeliveryInput) bool
}

var sinkBridge = struct {
	sync.RWMutex
	sink Sink
}{sink: Noop{}}

func currentSink() Sink {
	sinkBridge.RLock()
	sink := sinkBridge.sink
	sinkBridge.RUnlock()
	if sink == nil {
		return Noop{}
	}
	return sink
}

// Install replaces the current sink and returns a restore function for the
// owner. It is intended for one platform lifecycle at a time.
func Install(sink Sink) func() {
	if sink == nil {
		sink = Noop{}
	}
	sinkBridge.Lock()
	previous := sinkBridge.sink
	sinkBridge.sink = sink
	sinkBridge.Unlock()
	return func() {
		sinkBridge.Lock()
		sinkBridge.sink = previous
		sinkBridge.Unlock()
	}
}

func TryObserveEvent(input EventInput) (trace Trace, accepted bool) {
	defer func() {
		if recover() != nil {
			trace, accepted = Trace{}, false
		}
	}()
	return currentSink().TryObserveEvent(input)
}

func TryObserveRoute(trace Trace, input RouteInput) (route RouteTrace, accepted bool) {
	defer func() {
		if recover() != nil {
			route, accepted = RouteTrace{}, false
		}
	}()
	return currentSink().TryObserveRoute(trace, input)
}

func ObserveDelivery(trace RouteTrace, input DeliveryInput) (accepted bool) {
	defer func() {
		if recover() != nil {
			accepted = false
		}
	}()
	return currentSink().ObserveDelivery(trace, input)
}

type notifyKey struct {
	typeName string
	ptr      uintptr
}

type notifyBinding struct {
	eventTrace Trace
	routeTrace RouteTrace
	ordinal    int
}

var notifyBindings = struct {
	sync.Mutex
	values map[notifyKey]notifyBinding
}{values: make(map[notifyKey]notifyBinding)}

func makeNotifyKey(value any) (notifyKey, bool) {
	if value == nil {
		return notifyKey{}, false
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		if rv.IsNil() {
			return notifyKey{}, false
		}
		return notifyKey{typeName: rv.Type().String(), ptr: rv.Pointer()}, true
	default:
		return notifyKey{}, false
	}
}

// BindNotify associates an event trace with one concrete Legacy Notify while
// it travels through the existing dispatch/filter/send call chain.
func BindNotify(notify any, trace Trace, ordinal int) {
	key, ok := makeNotifyKey(notify)
	if !ok || !trace.Valid() {
		return
	}
	notifyBindings.Lock()
	notifyBindings.values[key] = notifyBinding{eventTrace: trace, ordinal: ordinal}
	notifyBindings.Unlock()
}

func ObserveRouteForNotify(notify any, input RouteInput) (RouteTrace, bool) {
	key, ok := makeNotifyKey(notify)
	if !ok {
		return RouteTrace{}, false
	}
	notifyBindings.Lock()
	binding, found := notifyBindings.values[key]
	notifyBindings.Unlock()
	if !found {
		return RouteTrace{}, false
	}
	input.RouteOrdinal = binding.ordinal
	route, accepted := TryObserveRoute(binding.eventTrace, input)
	notifyBindings.Lock()
	if accepted {
		binding.routeTrace = route
		notifyBindings.values[key] = binding
	} else {
		delete(notifyBindings.values, key)
	}
	notifyBindings.Unlock()
	return route, accepted
}

func RouteForNotify(notify any) (RouteTrace, bool) {
	key, ok := makeNotifyKey(notify)
	if !ok {
		return RouteTrace{}, false
	}
	notifyBindings.Lock()
	binding, found := notifyBindings.values[key]
	notifyBindings.Unlock()
	if !found || !binding.routeTrace.Valid() {
		return RouteTrace{}, false
	}
	return binding.routeTrace, true
}

func DetachNotify(notify any) {
	key, ok := makeNotifyKey(notify)
	if !ok {
		return
	}
	notifyBindings.Lock()
	delete(notifyBindings.values, key)
	notifyBindings.Unlock()
}

// String is useful for sanitized internal diagnostics and deliberately does
// not include snapshot contents.
func (t Trace) String() string {
	if !t.Valid() {
		return "invalid"
	}
	return fmt.Sprintf("trace:%s", t.eventObservationID)
}
