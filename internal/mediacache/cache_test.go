package mediacache

import (
	"context"
	"net"
	"path/filepath"
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
