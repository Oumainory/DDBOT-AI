package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

func testEvent() domain.NormalizedEvent {
	return domain.NormalizedEvent{ID: "event-1", NormalizedEventID: "event-1", SchemaVersion: 1, Platform: domain.PlatformTwitter, SourceID: "user", ExternalID: "tweet", EventType: domain.EventTweet, Title: "hello", Body: "public body", URL: "https://example.test/tweet", NormalizerVersion: "twitter-v1", PreprocessorVersion: "text-v1", ObservedAt: time.Unix(1700000000, 0).UTC(), CreatedAt: time.Unix(1700000000, 0).UTC(), ReplayPayload: json.RawMessage(`{"public":true}`)}
}

func TestOpenAICompatibleUsesBoundedStructuredRequest(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-api-key" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"schema_version\":1,\"category\":\"promotion\",\"importance\":\"low\",\"tags\":[],\"flags\":[],\"confidence\":0.99,\"uncertain\":false,\"insufficient_context\":false,\"prompt_injection_suspected\":false}"}}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Model: "test-model", APIKey: "secret-api-key", StructuredOutputMode: "json_schema"})
	if err != nil {
		t.Fatal(err)
	}
	result, usage, err := client.Classify(context.Background(), testEvent())
	if err != nil || result.Category != domain.CategoryPromotion || usage.InputTokens == nil {
		t.Fatalf("classify = %#v %#v %v", result, usage, err)
	}
	encoded, _ := json.Marshal(received)
	if strings.Contains(string(encoded), "secret-api-key") || received["stream"] != false {
		t.Fatalf("unsafe request body = %s", encoded)
	}
	if received["response_format"] == nil {
		t.Fatal("structured response format missing")
	}
}

func TestOpenAICompatibleMapsParseAndHTTPFailuresWithoutRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Classify(context.Background(), testEvent())
	if err == nil || StableErrorCode(err) != "provider_http_error" || calls != 1 {
		t.Fatalf("429 result = %v code=%s calls=%d", err, StableErrorCode(err), calls)
	}
}
