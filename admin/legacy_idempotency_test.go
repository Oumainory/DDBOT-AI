package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
