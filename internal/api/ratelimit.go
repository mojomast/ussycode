// Package api implements the ussycode HTTPS API endpoints.
package api

import (
	"sync"
	"time"
)

// RateLimiter provides per-key token bucket rate limiting.
// It uses an in-memory sync.Map and is safe for concurrent use.
type RateLimiter struct {
	rate    float64       // tokens per second
	burst   int           // maximum burst size
	buckets sync.Map      // map[string]*bucket
	cleanup time.Duration // how often to clean stale buckets
}

// bucket is a single token bucket for one key.
type bucket struct {
	mu       sync.Mutex
	tokens   float64
	maxBurst int
	rate     float64
	lastTime time.Time
}

// NewRateLimiter creates a rate limiter.
// rate is requests per minute, burst is the max burst size.
func NewRateLimiter(ratePerMinute float64, burst int) *RateLimiter {
	if ratePerMinute <= 0 {
		ratePerMinute = 60
	}
	if burst <= 0 {
		burst = 10
	}

	rl := &RateLimiter{
		rate:    ratePerMinute / 60.0, // convert to per-second
		burst:   burst,
		cleanup: 10 * time.Minute,
	}

	// Start background cleanup goroutine
	go rl.cleanupLoop()

	return rl
}

// Allow checks if a request is allowed for the given key (e.g., SSH fingerprint).
// Returns true if the request is within rate limits.
func (rl *RateLimiter) Allow(key string) bool {
	b := rl.getBucket(key)

	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.lastTime = now

	// Add tokens for elapsed time
	b.tokens += elapsed * b.rate
	if b.tokens > float64(b.maxBurst) {
		b.tokens = float64(b.maxBurst)
	}

	// Try to consume one token
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	return false
}

// RetryAfter returns the duration until the next request will be allowed
// for the given key.
func (rl *RateLimiter) RetryAfter(key string) time.Duration {
	b := rl.getBucket(key)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.tokens >= 1.0 {
		return 0
	}

	needed := 1.0 - b.tokens
	return time.Duration(needed/b.rate*1000) * time.Millisecond
}

func (rl *RateLimiter) getBucket(key string) *bucket {
	if v, ok := rl.buckets.Load(key); ok {
		return v.(*bucket)
	}

	b := &bucket{
		tokens:   float64(rl.burst),
		maxBurst: rl.burst,
		rate:     rl.rate,
		lastTime: time.Now(),
	}

	actual, _ := rl.buckets.LoadOrStore(key, b)
	return actual.(*bucket)
}

// cleanupLoop periodically removes stale buckets (those with full tokens
// that haven't been accessed recently).
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		rl.buckets.Range(func(key, value any) bool {
			b := value.(*bucket)
			b.mu.Lock()
			idle := now.Sub(b.lastTime)
			b.mu.Unlock()

			// Remove buckets idle for more than 2x the cleanup interval
			if idle > 2*rl.cleanup {
				rl.buckets.Delete(key)
			}
			return true
		})
	}
}
