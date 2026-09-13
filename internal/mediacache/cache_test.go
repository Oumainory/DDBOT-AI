package mediacache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

func TestCachePutDeduplicatesAndLinksPublicMedia(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cache := New(Config{Root: t.TempDir(), Repository: platformdb.NewPhase5Repository(store), Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	first, err := cache.Put(context.Background(), "route-1", "event-1", "https://cdn.example.test/image.png", []byte("png-data"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Put(context.Background(), "route-2", "event-2", "https://cdn.example.test/image.png", []byte("png-data"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.SHA256 != second.SHA256 {
		t.Fatalf("dedup entries = %#v / %#v", first, second)
	}
	if got, err := cache.Read(first); err != nil || string(got) != "png-data" {
		t.Fatalf("cached bytes = %q, err=%v", got, err)
	}
}

func TestCacheConcurrentEventQuotaNeverCommitsBeyondLimit(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache-event-quota.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := platformdb.NewPhase5Repository(store)
	cache := New(Config{Root: t.TempDir(), Repository: repo, MaxEventBytes: 30, MaxGlobalBytes: 1 << 20})
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, putErr := cache.Put(context.Background(), "route-event-quota", "event-event-quota", "https://cdn.example.test/event.png", []byte("png-data-"+string(rune('a'+i))), "image/png"); putErr == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if successes > 3 {
		t.Fatalf("successful concurrent event writes = %d, want at most 3", successes)
	}
	usage, err := repo.MediaCacheEventUsage(context.Background(), "event-event-quota")
	if err != nil {
		t.Fatal(err)
	}
	if usage > 30 {
		t.Fatalf("event usage = %d, exceeds 30-byte limit", usage)
	}
}

func TestCacheConcurrentGlobalQuotaNeverCommitsBeyondLimit(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache-global-quota.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := platformdb.NewPhase5Repository(store)
	cache := New(Config{Root: t.TempDir(), Repository: repo, MaxGlobalBytes: 25})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _ = cache.Put(context.Background(), "route-global-"+string(rune('a'+i)), "event-global-"+string(rune('a'+i)), "https://cdn.example.test/global.png", []byte("global-"+string(rune('a'+i))), "image/png")
		}(i)
	}
	close(start)
	wg.Wait()
	usage, err := repo.MediaCacheUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage > 25 {
		t.Fatalf("global usage = %d, exceeds 25-byte limit", usage)
	}
}

func TestCacheConcurrentSameSHACreatesOneDurableEntry(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache-dedup-race.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := platformdb.NewPhase5Repository(store)
	cache := New(Config{Root: t.TempDir(), Repository: repo})
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, putErr := cache.Put(context.Background(), "route-dedup", "event-dedup-"+string(rune('a'+i)), "https://cdn.example.test/dedup.png", []byte("same-png-data"), "image/png"); putErr == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if successes == 0 {
		t.Fatal("all concurrent deduplicated writes failed")
	}
	entry, err := repo.MediaCacheEntryBySHA(context.Background(), "sha256:"+sha256Hex("same-png-data"))
	if err != nil {
		t.Fatal(err)
	}
	usage, err := repo.MediaCacheUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID == "" || usage != int64(len("same-png-data")) {
		t.Fatalf("same-SHA entry=%#v usage=%d, want one %d-byte durable object", entry, usage, len("same-png-data"))
	}
}

func sha256Hex(value string) string {
	// The production cache uses SHA-256 as the content-addressed key. Keeping
	// this helper local to the regression test avoids relying on cache internals.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func TestCacheRejectsUnsafeURLAndOversizedStream(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/image", "http://10.0.0.1/image", "file:///tmp/x", "http://user:pass@example.com/x"} {
		if err := ValidateURL(raw); err != ErrInvalidMediaURL {
			t.Fatalf("ValidateURL(%q) = %v, want %v", raw, err, ErrInvalidMediaURL)
		}
	}
	if !unsafeIP(net.ParseIP("127.0.0.1")) || !unsafeIP(net.ParseIP("169.254.169.254")) {
		t.Fatal("unsafe IP policy did not reject loopback/metadata")
	}
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache-limit.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cache := New(Config{Root: t.TempDir(), Repository: platformdb.NewPhase5Repository(store), MaxFileBytes: 4})
	if _, err := cache.Put(context.Background(), "route", "event", "https://cdn.example.test/x", []byte("12345"), "text/plain"); err != ErrMediaTooLarge {
		t.Fatalf("oversized Put = %v, want %v", err, ErrMediaTooLarge)
	}
}

func TestCacheDoesNotResurrectExpiredContentAddressedEntry(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache-expiry.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	cache := New(Config{Root: t.TempDir(), Repository: platformdb.NewPhase5Repository(store), Retention: time.Hour, Now: func() time.Time { return now }})
	first, err := cache.Put(context.Background(), "route-1", "event-1", "https://cdn.example.test/image.png", []byte("png-data"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	second, err := cache.Put(context.Background(), "route-2", "event-2", "https://cdn.example.test/image.png", []byte("png-data"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !second.ExpiresAt.After(now) {
		t.Fatalf("replacement entry remained expired: %#v", second)
	}
	if got, err := cache.Read(second); err != nil || string(got) != "png-data" {
		t.Fatalf("replacement bytes = %q, err=%v", got, err)
	}
	if first.ExpiresAt.After(now) {
		t.Fatalf("test clock did not advance past first expiry: first=%v now=%v", first.ExpiresAt, now)
	}
}

func TestCacheReadReferenceUsesDurableEventLink(t *testing.T) {
	store, err := platformdb.Open(context.Background(), platformdb.Config{Path: filepath.Join(t.TempDir(), "cache-reference.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	cache := New(Config{Root: t.TempDir(), Repository: platformdb.NewPhase5Repository(store), Retention: time.Hour, Now: func() time.Time { return now }})
	const sourceURL = "https://cdn.example.test/reference.png"
	if _, err := cache.Put(context.Background(), "route-reference", "event-reference", sourceURL, []byte("png-data"), "image/png"); err != nil {
		t.Fatal(err)
	}
	entry, data, err := cache.ReadReference(context.Background(), "event-reference", sourceURL)
	if err != nil || entry.ID == "" || string(data) != "png-data" {
		t.Fatalf("reference = %#v, data=%q, err=%v", entry, data, err)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := cache.ReadReference(context.Background(), "event-reference", sourceURL); err == nil {
		t.Fatal("expired cache reference unexpectedly remained readable")
	}
}
