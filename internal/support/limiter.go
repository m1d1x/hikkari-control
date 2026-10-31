package support

import (
	"sync"
	"time"
)

// Limiter implements a thread-safe sliding window rate limiter per key.
type Limiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	maxReqs  int
	window   time.Duration
}

// NewLimiter creates a sliding window limiter.
func NewLimiter(maxReqs int, window time.Duration) *Limiter {
	return &Limiter{
		requests: make(map[string][]time.Time),
		maxReqs:  maxReqs,
		window:   window,
	}
}

// Allow reports whether a request for the given key is permitted within the rate limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	timestamps := l.requests[key]
	// Filter out expired timestamps
	valid := timestamps[:0]
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= l.maxReqs {
		l.requests[key] = valid
		return false
	}

	valid = append(valid, now)
	l.requests[key] = valid
	return true
}

// Reset clears recorded timestamps for a key (useful for tests).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.requests, key)
}
