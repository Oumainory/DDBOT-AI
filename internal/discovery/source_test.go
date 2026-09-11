package discovery

import (
	"context"
	"errors"
	"testing"
)

func TestBilibiliUIDAcceptsOnlyOfficialExactProfiles(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"401742377", "401742377"},
		{"https://space.bilibili.com/401742377", "401742377"},
		{"https://www.bilibili.com/space/401742377/", "401742377"},
	}
	for _, test := range tests {
		got, _, err := BilibiliUID(test.input)
		if err != nil || got != test.want {
			t.Fatalf("BilibiliUID(%q) = %q, %v", test.input, got, err)
		}
	}
	for _, input := range []string{"https://evil.example/401742377", "https://space.bilibili.com/abc", "https://space.bilibili.com/1/2"} {
		if _, _, err := BilibiliUID(input); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("BilibiliUID(%q) error = %v, want invalid profile", input, err)
		}
	}
}

func TestTwitterHandleAcceptsExactHandleAndOfficialProfile(t *testing.T) {
	for _, input := range []string{"@GenshinImpact", "GenshinImpact", "https://x.com/GenshinImpact"} {
		got, _, err := TwitterHandle(input)
		if err != nil || got != "GenshinImpact" {
			t.Fatalf("TwitterHandle(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"https://evil.example/GenshinImpact", "https://x.com/a/b", "bad handle"} {
		if _, _, err := TwitterHandle(input); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("TwitterHandle(%q) error = %v, want invalid profile", input, err)
		}
	}
}

func TestStaticResolversDoNotPerformGenericSearch(t *testing.T) {
	if _, err := (StaticBilibiliResolver{}).Search(context.Background(), "原神"); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("static search error = %v, want unavailable", err)
	}
	candidates, err := (StaticBilibiliResolver{}).Search(context.Background(), "401742377")
	if err != nil || len(candidates) != 1 || candidates[0].UID != "401742377" {
		t.Fatalf("exact bilibili search = %#v, %v", candidates, err)
	}
	resolved, err := (StaticTwitterResolver{}).Resolve(context.Background(), "@GenshinImpact")
	if err != nil || resolved.ExternalID != "GenshinImpact" || resolved.CanonicalURL == "" {
		t.Fatalf("twitter resolve = %#v, %v", resolved, err)
	}
}
