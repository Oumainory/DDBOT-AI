package auth

import (
	"testing"
	"time"
)

func TestRateLimiterBoundsFailuresAndResetsAfterWindow(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	limiter := NewRateLimiter(RateLimiterConfig{
		Limit:  2,
		Window: time.Minute,
		Now:    func() time.Time { return now },
	})
	if !limiter.Allow("key") {
		t.Fatal("new key was rate limited")
	}
	limiter.Failure("key")
	limiter.Failure("key")
	if limiter.Allow("key") {
		t.Fatal("key remained allowed after limit")
	}
	limiter.Reset("key")
	if !limiter.Allow("key") {
		t.Fatal("reset key remained rate limited")
	}
	limiter.Failure("key")
	limiter.Failure("key")
	now = now.Add(time.Minute)
	if !limiter.Allow("key") {
		t.Fatal("expired rate-limit window remained blocked")
	}
}
