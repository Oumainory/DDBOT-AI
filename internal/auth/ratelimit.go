package auth

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultLoginLimit   = 5
	DefaultLoginWindow  = 5 * time.Minute
	DefaultLoginMaxKeys = 4096
)

type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	entries map[string]rateEntry
}

type rateEntry struct {
	started time.Time
	count   int
}

type RateLimiterConfig struct {
	Limit   int
	Window  time.Duration
	MaxKeys int
	Now     func() time.Time
}

func NewRateLimiter(config RateLimiterConfig) *RateLimiter {
	if config.Limit <= 0 {
		config.Limit = DefaultLoginLimit
	}
	if config.Window <= 0 {
		config.Window = DefaultLoginWindow
	}
	if config.MaxKeys <= 0 {
		config.MaxKeys = DefaultLoginMaxKeys
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &RateLimiter{limit: config.Limit, window: config.Window, maxKeys: config.MaxKeys, now: config.Now, entries: make(map[string]rateEntry)}
}

// Allow reports whether another failed login may be recorded for this key.
// It does not reveal whether the username exists; callers use the same key for
// both successful and failed credential attempts.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	key = strings.TrimSpace(key)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok || now.Sub(entry.started) >= l.window {
		return true
	}
	return entry.count < l.limit
}

func (l *RateLimiter) Failure(key string) {
	if l == nil {
		return
	}
	key = strings.TrimSpace(key)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry, ok := l.entries[key]; ok && now.Sub(entry.started) < l.window {
		entry.count++
		l.entries[key] = entry
		return
	}
	if len(l.entries) >= l.maxKeys {
		l.evictOldestLocked()
	}
	l.entries[key] = rateEntry{started: now, count: 1}
}

func (l *RateLimiter) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.entries, strings.TrimSpace(key))
	l.mu.Unlock()
}

func (l *RateLimiter) evictOldestLocked() {
	if len(l.entries) == 0 {
		return
	}
	keys := make([]string, 0, len(l.entries))
	for key := range l.entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	oldest := keys[0]
	for _, key := range keys[1:] {
		if l.entries[key].started.Before(l.entries[oldest].started) {
			oldest = key
		}
	}
	delete(l.entries, oldest)
}
