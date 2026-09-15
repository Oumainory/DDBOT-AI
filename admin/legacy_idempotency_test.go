package admin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/idempotency"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

func TestLegacyCommandIdempotencySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.sqlite")
	body := []byte(`{"site":"bilibili","id":123,"type":"dynamic","groupCode":777}`)
	key := "legacy-command-key-01"
	calls := 0

	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{legacyIdempotency: platformdb.NewIdempotencyStore(store)}
	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, "/api/v1/subs/add?scope=legacy", nil)
	firstRequest.Header.Set("Idempotency-Key", key)
	server.executeLegacyCommand(first, firstRequest, "create_subscription", body, func() (int, []byte) {
		calls++
		return http.StatusOK, legacyJSON(map[string]string{"status": "success"})
	})
	firstBody := first.Body.String()
	if first.Code != http.StatusOK || calls != 1 {
		t.Fatalf("first command status=%d calls=%d body=%s", first.Code, calls, firstBody)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := platformdb.Open(context.Background(), platformdb.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server = &Server{legacyIdempotency: platformdb.NewIdempotencyStore(reopened)}
	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodPost, "/api/v1/subs/add?scope=legacy", nil)
	secondRequest.Header.Set("Idempotency-Key", key)
	server.executeLegacyCommand(second, secondRequest, "create_subscription", body, func() (int, []byte) {
		calls++
		return http.StatusOK, legacyJSON(map[string]string{"status": "should-not-run"})
	})
	if second.Code != first.Code || second.Body.String() != firstBody || calls != 1 {
		t.Fatalf("restart replay status=%d body=%s calls=%d, want exact first response and one side effect", second.Code, second.Body.String(), calls)
	}

	conflict := httptest.NewRecorder()
	conflictRequest := httptest.NewRequest(http.MethodPost, "/api/v1/subs/add?scope=legacy", nil)
	conflictRequest.Header.Set("Idempotency-Key", key)
	server.executeLegacyCommand(conflict, conflictRequest, "create_subscription", []byte(`{"site":"twitter"}`), func() (int, []byte) {
		calls++
		return http.StatusOK, legacyJSON(map[string]string{"status": "should-not-run"})
	})
	if conflict.Code != http.StatusConflict || conflict.Body.String() != "{\"error\":\"idempotency_conflict\"}\n" || calls != 1 {
		t.Fatalf("conflicting replay status=%d body=%s calls=%d", conflict.Code, conflict.Body.String(), calls)
	}
}

func TestReadLegacyJSONBodyRejectsOverLimitAndTrailingValues(t *testing.T) {
	tooLarge := make([]byte, maxLegacyJSONBodyBytes+1)
	for i := range tooLarge {
		tooLarge[i] = ' '
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/subs/add", bytes.NewReader(tooLarge))
	if _, err := readLegacyJSONBody(request); err == nil {
		t.Fatal("oversized legacy JSON body unexpectedly accepted")
	}
	trailing := httptest.NewRequest(http.MethodPost, "/api/v1/subs/add", bytes.NewReader([]byte(`{"ok":true}{"extra":true}`)))
	if _, err := readLegacyJSONBody(trailing); err == nil {
		t.Fatal("multiple legacy JSON values unexpectedly accepted")
	}
}

type legacyBodyMustNotBeRead struct{}

func (legacyBodyMustNotBeRead) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (legacyBodyMustNotBeRead) Close() error { return nil }

type countingLegacyIdempotencyStore struct {
	*idempotency.MemoryStore
	beginCalls int
}

func (s *countingLegacyIdempotencyStore) BeginCommand(principal, key, command string, fingerprint idempotency.Fingerprint, now time.Time) (idempotency.Record, idempotency.Outcome, error) {
	s.beginCalls++
	return s.MemoryStore.BeginCommand(principal, key, command, fingerprint, now)
}

func TestLegacySubscriptionMutationsArePostOnlyAtHTTPBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: filepath.Join(t.TempDir(), "method-gate.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	countingStore := &countingLegacyIdempotencyStore{MemoryStore: idempotency.NewMemoryStore(time.Hour)}
	server := &Server{legacyIdempotency: countingStore}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/subs/add", server.withAuth(server.handleAddSub))
	mux.Handle("/api/v1/subs/remove", server.withAuth(server.handleRemoveSub))

	for _, endpoint := range []string{"/api/v1/subs/add", "/api/v1/subs/remove"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead} {
			req := httptest.NewRequest(method, endpoint, legacyBodyMustNotBeRead{})
			req.Header.Set("Idempotency-Key", "method-gate-"+method)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status=%d, want 405", method, endpoint, response.Code)
			}
			if response.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("%s %s Allow=%q, want POST", method, endpoint, response.Header().Get("Allow"))
			}
		}
	}
	if countingStore.beginCalls != 0 {
		t.Fatalf("method-rejected requests claimed %d idempotency records", countingStore.beginCalls)
	}

	options := httptest.NewRecorder()
	mux.ServeHTTP(options, httptest.NewRequest(http.MethodOptions, "/api/v1/subs/add", legacyBodyMustNotBeRead{}))
	if options.Code != http.StatusOK {
		t.Fatalf("OPTIONS status=%d, want middleware preflight 200", options.Code)
	}
	post := httptest.NewRecorder()
	postRequest := httptest.NewRequest(http.MethodPost, "/api/v1/subs/add", bytes.NewReader([]byte(`{"site":"unsupported","id":1,"type":"dynamic","groupCode":779}`)))
	postRequest.Header.Set("Idempotency-Key", "method-gate-post")
	mux.ServeHTTP(post, postRequest)
	if post.Code == http.StatusMethodNotAllowed || countingStore.beginCalls != 1 {
		t.Fatalf("POST status=%d idempotency claims=%d, want handler execution with one claim", post.Code, countingStore.beginCalls)
	}
}
