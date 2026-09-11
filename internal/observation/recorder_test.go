package observation

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

type failingRandom struct{}

func (failingRandom) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type panicSink struct{}

func (panicSink) TryObserveEvent(EventInput) (Trace, bool) { panic("test sink panic") }
func (panicSink) TryObserveRoute(Trace, RouteInput) (RouteTrace, bool) {
	panic("test sink panic")
}
func (panicSink) ObserveDelivery(RouteTrace, DeliveryInput) bool { panic("test sink panic") }

type recorderFakeRepository struct {
	mu           sync.Mutex
	events       []platformdb.ObservedEventRecord
	routes       []platformdb.RouteObservationRecord
	deliveries   []platformdb.DeliveryObservationRecord
	err          error
	panicOnce    atomic.Bool
	panicEnabled bool
	entered      chan struct{}
	release      chan struct{}
	enteredOnce  sync.Once
	pruned       int
	pruneErr     error
}

func (f *recorderFakeRepository) InsertObservedEvent(ctx context.Context, record platformdb.ObservedEventRecord) error {
	if f.entered != nil {
		f.enteredOnce.Do(func() { close(f.entered) })
		if f.release != nil {
			select {
			case <-f.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if f.panicEnabled && f.panicOnce.CompareAndSwap(false, true) {
		panic("test-only repository panic")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, record)
	return nil
}

func (f *recorderFakeRepository) InsertRouteObservation(_ context.Context, record platformdb.RouteObservationRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.routes = append(f.routes, record)
	return nil
}

func (f *recorderFakeRepository) InsertDeliveryObservation(_ context.Context, record platformdb.DeliveryObservationRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.deliveries = append(f.deliveries, record)
	return nil
}

func (f *recorderFakeRepository) PruneObservationsBefore(_ context.Context, _ time.Time, _ int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pruned, f.pruneErr
}

func (f *recorderFakeRepository) snapshot() (events []platformdb.ObservedEventRecord, routes []platformdb.RouteObservationRecord, deliveries []platformdb.DeliveryObservationRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]platformdb.ObservedEventRecord(nil), f.events...),
		append([]platformdb.RouteObservationRecord(nil), f.routes...),
		append([]platformdb.DeliveryObservationRecord(nil), f.deliveries...)
}

func testEventInput() EventInput {
	return EventInput{
		Platform:         "bilibili",
		SourceKind:       "account",
		SourceExternalID: "401742377",
		UpstreamEventID:  "dynamic-1",
		EventType:        "dynamic",
		ObservedAt:       time.Unix(1700000000, 0).UTC(),
		SourceEventAt:    time.Unix(1699999999, 0).UTC(),
		PublicText:       "public announcement",
		PublicURL:        "https://example.invalid/dynamic/1",
		PublicMediaURLs:  []string{"https://example.invalid/image.jpg"},
		PublicAuthorID:   "401742377",
		PublicAuthorName: "official",
	}
}

func TestRecorderPersistsAllowlistedSnapshotsAndCorrelatesFacts(t *testing.T) {
	repository := &recorderFakeRepository{}
	recorder := NewRecorder(repository, Config{QueueSize: 16})
	media := []string{"https://example.invalid/image.jpg"}
	input := testEventInput()
	input.PublicMediaURLs = media
	trace, accepted := recorder.TryObserveEvent(input)
	if !accepted || !trace.Valid() {
		t.Fatal("event was not accepted")
	}
	media[0] = "https://example.invalid/secret-token"
	route, accepted := recorder.TryObserveRoute(trace, RouteInput{
		RouteOrdinal:          0,
		DestinationKind:       "qq_group",
		DestinationExternalID: "123456",
		Outcome:               "pass",
		ReasonCode:            "legacy_pass",
	})
	if !accepted || !route.Valid() {
		t.Fatal("route was not accepted")
	}
	if !recorder.ObserveDelivery(route, DeliveryInput{
		ConnectorKind:         "onebot",
		DestinationExternalID: "123456",
		Status:                "sent",
		ResultCode:            "sent",
	}) {
		t.Fatal("delivery was not accepted")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, routes, deliveries := repository.snapshot()
	if len(events) != 1 || len(routes) != 1 || len(deliveries) != 1 {
		t.Fatalf("persisted facts = %d/%d/%d, want 1/1/1", len(events), len(routes), len(deliveries))
	}
	if !strings.HasPrefix(events[0].ID, "evt_") || len(events[0].ID) <= len("evt_") {
		t.Fatalf("event id = %q, want opaque prefixed id", events[0].ID)
	}
	if routes[0].EventID != events[0].ID || deliveries[0].EventID != events[0].ID || deliveries[0].RouteObservationID != routes[0].ID {
		t.Fatalf("correlation = event %q route %q delivery %q/%q", events[0].ID, routes[0].EventID, deliveries[0].EventID, deliveries[0].RouteObservationID)
	}
	if strings.Contains(events[0].PublicSnapshotJSON, "secret-token") || strings.Contains(events[0].PublicSnapshotJSON, "response") || strings.Contains(events[0].PublicSnapshotJSON, "headers") {
		t.Fatalf("snapshot contains non-allowlisted data: %s", events[0].PublicSnapshotJSON)
	}
	if len(events[0].ContentFingerprint) != 64 {
		t.Fatalf("fingerprint = %q, want SHA-256 hex", events[0].ContentFingerprint)
	}

	// The snapshot is deterministic for the same allowlisted projection.
	second := NewRecorder(&recorderFakeRepository{}, Config{QueueSize: 4})
	secondTrace, ok := second.TryObserveEvent(testEventInput())
	if !ok || !secondTrace.Valid() {
		t.Fatal("second event was not accepted")
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderQueueFullIsNonBlockingAndTraceBecomesInvalid(t *testing.T) {
	repository := &recorderFakeRepository{entered: make(chan struct{}), release: make(chan struct{})}
	recorder := NewRecorder(repository, Config{QueueSize: 1})
	first, ok := recorder.TryObserveEvent(testEventInput())
	if !ok || !first.Valid() {
		t.Fatal("first event was not accepted")
	}
	select {
	case <-repository.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start first persistence")
	}
	second, ok := recorder.TryObserveEvent(testEventInput())
	if !ok || !second.Valid() {
		t.Fatal("second event was not accepted into the bounded queue")
	}
	started := time.Now()
	third, ok := recorder.TryObserveEvent(testEventInput())
	if ok || third.Valid() {
		t.Fatal("third event unexpectedly bypassed full queue")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("queue-full observation blocked for %s", elapsed)
	}
	if _, ok := recorder.TryObserveRoute(third, RouteInput{DestinationKind: "qq_group", DestinationExternalID: "1", Outcome: "pass", ReasonCode: "legacy_pass"}); ok {
		t.Fatal("invalid event trace produced a route")
	}
	close(repository.release)
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Stats().QueueDropped; got == 0 {
		t.Fatal("queue drop counter was not incremented")
	}
}

func TestRecorderInjectableRandomFailureProducesInvalidTrace(t *testing.T) {
	repository := &recorderFakeRepository{}
	recorder := NewRecorder(repository, Config{Random: failingRandom{}})
	trace, ok := recorder.TryObserveEvent(testEventInput())
	if ok || trace.Valid() {
		t.Fatal("random failure unexpectedly produced a trace")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, _, _ := repository.snapshot()
	if len(events) != 0 {
		t.Fatalf("random failure persisted %d events", len(events))
	}
}

func TestRecorderWorkerRecoversFromPersistencePanicAndReportsSanitizedFailure(t *testing.T) {
	var logs []string
	var logMu sync.Mutex
	repository := &recorderFakeRepository{panicEnabled: true}
	recorder := NewRecorder(repository, Config{
		QueueSize: 4,
		Log: func(component, class string, count uint64) {
			logMu.Lock()
			defer logMu.Unlock()
			logs = append(logs, component+":"+class)
		},
	})
	if _, ok := recorder.TryObserveEvent(testEventInput()); !ok {
		t.Fatal("event was not accepted")
	}
	if _, ok := recorder.TryObserveEvent(testEventInput()); !ok {
		t.Fatal("second event was not accepted")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := recorder.Stats()
	if stats.WorkerPanics != 1 {
		t.Fatalf("worker panics = %d, want 1", stats.WorkerPanics)
	}
	if len(logs) == 0 || logs[0] != "worker:panic" {
		t.Fatalf("sanitized panic logs = %#v", logs)
	}
}

func TestRecorderPersistenceErrorsAreFailOpenAndSanitized(t *testing.T) {
	var logs []string
	repository := &recorderFakeRepository{err: errors.New("raw secret and SQL must not be logged")}
	recorder := NewRecorder(repository, Config{Log: func(component, class string, count uint64) {
		logs = append(logs, component+":"+class)
	}})
	if _, ok := recorder.TryObserveEvent(testEventInput()); !ok {
		t.Fatal("event was not accepted")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.Stats().PersistenceErrors != 1 {
		t.Fatalf("persistence errors = %d, want 1", recorder.Stats().PersistenceErrors)
	}
	if len(logs) != 1 || logs[0] != "event:persistence_error" {
		t.Fatalf("persistence logs = %#v", logs)
	}
}

func TestRecorderRetentionUsesRepositoryBoundary(t *testing.T) {
	repository := &recorderFakeRepository{pruned: 3}
	recorder := NewRecorder(repository, Config{Retention: 24 * time.Hour, BatchSize: 8})
	defer recorder.Close(context.Background())
	count, err := recorder.PruneOnce(context.Background(), time.Unix(1700000000, 0))
	if err != nil || count != 3 {
		t.Fatalf("prune = %d, %v; want 3,nil", count, err)
	}
}

func TestRecorderGlobalBridgeDefaultsToNoopAndRestores(t *testing.T) {
	previous := Install(nil)
	if _, ok := TryObserveEvent(testEventInput()); ok {
		t.Fatal("default Noop accepted an event")
	}
	fake := &recorderFakeRepository{}
	recorder := NewRecorder(fake, Config{})
	restore := Install(recorder)
	trace, ok := TryObserveEvent(testEventInput())
	if !ok || !trace.Valid() {
		t.Fatal("installed recorder did not accept event")
	}
	restore()
	if _, ok := TryObserveEvent(testEventInput()); ok {
		t.Fatal("restored Noop accepted an event")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous()
}

func TestGlobalObservationBridgeRecoversObserverPanic(t *testing.T) {
	restore := Install(panicSink{})
	defer restore()
	if trace, ok := TryObserveEvent(testEventInput()); ok || trace.Valid() {
		t.Fatal("event panic was not converted to invalid trace")
	}
	if route, ok := TryObserveRoute(Trace{}, RouteInput{}); ok || route.Valid() {
		t.Fatal("route panic was not converted to invalid trace")
	}
	if ObserveDelivery(RouteTrace{}, DeliveryInput{}) {
		t.Fatal("delivery panic was not converted to rejection")
	}
}
