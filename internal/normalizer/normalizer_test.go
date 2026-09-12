package normalizer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
)

func TestBilibiliNormalizationIsDeterministicAndBounded(t *testing.T) {
	input := Input{ObservedEventID: "obs-1", Platform: "bilibili", SourceID: "401742377", ExternalID: "dyn-1", EventType: "dynamic", Title: "  title  ", Text: strings.Repeat("x", MaxInputRunes+100), RelatedText: strings.Repeat("r", MaxRelatedRunes+100), URL: "https://www.bilibili.com/opus/1", MediaURLs: []string{"https://i.example.test/a"}, ObservedAt: time.Unix(1700000000, 0)}
	first, err := NormalizeBilibili(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeBilibili(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.NormalizedEventID != second.NormalizedEventID {
		t.Fatalf("normalization id is not deterministic: %#v %#v", first, second)
	}
	if !first.Truncated || len([]rune(first.Body))+len([]rune(first.Title))+len([]rune(first.RelatedBody)) > MaxInputRunes {
		t.Fatalf("bounded event = %#v", first)
	}
	if len(first.ReplayPayload) == 0 || len(first.Media) != 1 || first.URL == "" {
		t.Fatalf("public snapshot fields missing: %#v", first)
	}
}

func TestTwitterNormalizationRejectsPrivateAndUnsupportedInput(t *testing.T) {
	if _, err := Normalize(Input{Platform: "mastodon", SourceID: "s", ExternalID: "e", EventType: "tweet"}); err != ErrUnsupportedPlatform {
		t.Fatalf("unsupported error = %v", err)
	}
	value, err := NormalizeTwitter(Input{ObservedEventID: "obs", SourceID: "user", ExternalID: "tweet", EventType: "tweet", Text: "hello", PublicURLs: []string{"file:///secret", "https://x.example/t/1"}, ObservedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(value.PublicURLs) != 1 || value.PublicURLs[0] != "https://x.example/t/1" {
		t.Fatalf("private URL survived: %#v", value.PublicURLs)
	}
	if value.Platform != domain.PlatformTwitter {
		t.Fatalf("platform = %q", value.Platform)
	}
}

func TestNormalizeObservedPreservesEventIdentityAndRejectsUnsafeURLQuery(t *testing.T) {
	record := platformdb.ObservedEventRecord{ID: "obs-1", Platform: "twitter", SourceExternalID: "user-1", UpstreamEventID: "tweet-99", EventType: "tweet", ObservedAt: time.Unix(2, 0), PublicSnapshotJSON: `{"platform":"twitter","source_external_id":"user-1","upstream_event_id":"tweet-99","event_type":"tweet","text":"hello","url":"https://x.example/status/99?token=do-not-store&view=full"}`}
	event, err := NormalizeObserved(record)
	if err != nil {
		t.Fatal(err)
	}
	if event.SourceID != "user-1" || event.ExternalID != "tweet-99" {
		t.Fatalf("event identity = source=%q external=%q", event.SourceID, event.ExternalID)
	}
	if strings.Contains(string(event.ReplayPayload), "do-not-store") || strings.Contains(event.URL, "token") {
		t.Fatalf("unsafe URL query survived: %q payload=%s", event.URL, event.ReplayPayload)
	}
	if _, err := NormalizeObserved(platformdb.ObservedEventRecord{ID: "obs-2", Platform: "twitter", SourceExternalID: "user-1", EventType: "tweet", ObservedAt: time.Unix(2, 0), PublicSnapshotJSON: `{"text":"one"} {"text":"two"}`}); err != ErrInvalidSnapshot {
		t.Fatalf("trailing snapshot error = %v", err)
	}
	if !json.Valid(event.ReplayPayload) {
		t.Fatal("replay payload is invalid JSON")
	}
}
