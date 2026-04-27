// Package middleware provides HTTP middleware for the Bahia API.
package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// RateLimiterConfig configures the per-IP rate limiter.
type RateLimiterConfig struct {
	// Rate is the number of requests allowed per Interval.
	Rate int
	// Interval is the time window for the rate (e.g., 1*time.Second).
	Interval time.Duration
	// CleanupInterval controls how often stale entries are purged.
	// Zero means 2x Interval.
	CleanupInterval time.Duration
}

// visitor tracks token bucket state for a single IP.
type visitor struct {
	tokens   float64
	lastSeen time.Time
}

// IPRateLimiter implements a per-IP token bucket rate limiter.
type IPRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     float64       // tokens per second
	burst    float64       // max tokens (= rate * interval in seconds)
	interval time.Duration // for staleness cleanup
	done     chan struct{}
}

// NewIPRateLimiter creates a rate limiter that allows cfg.Rate requests per
// cfg.Interval per IP address. The token bucket refills continuously.
func NewIPRateLimiter(cfg RateLimiterConfig) *IPRateLimiter {
	if cfg.Rate <= 0 {
		cfg.Rate = 100
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}

	burst := float64(cfg.Rate)
	ratePerSec := burst / cfg.Interval.Seconds()

	cleanupInterval := cfg.CleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = cfg.Interval * 2
	}

	rl := &IPRateLimiter{
		visitors: make(map[string]*visitor),
		rate:     ratePerSec,
		burst:    burst,
		interval: cfg.Interval,
		done:     make(chan struct{}),
	}

	go rl.cleanup(cleanupInterval)

	return rl
}

// Stop terminates the background cleanup goroutine.
func (rl *IPRateLimiter) Stop() {
	close(rl.done)
}

// Allow checks whether a request from the given IP should be allowed.
// It returns (allowed, remaining tokens).
func (rl *IPRateLimiter) Allow(ip string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	v, exists := rl.visitors[ip]
	if !exists {
		v = &visitor{tokens: rl.burst, lastSeen: now}
		rl.visitors[ip] = v
	}

	// Replenish tokens based on elapsed time.
	elapsed := now.Sub(v.lastSeen).Seconds()
	v.tokens += elapsed * rl.rate
	if v.tokens > rl.burst {
		v.tokens = rl.burst
	}
	v.lastSeen = now

	if v.tokens < 1 {
		return false, 0
	}

	v.tokens--
	return true, int(v.tokens)
}

// cleanup periodically removes visitors that haven't been seen recently.
func (rl *IPRateLimiter) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-rl.done:
			return
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-interval)
			for ip, v := range rl.visitors {
				if v.lastSeen.Before(cutoff) {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// RateLimit returns chi-compatible middleware that enforces per-IP rate limits.
func RateLimit(limiter *IPRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr // chi RealIP middleware sets this

			allowed, remaining := limiter.Allow(ip)
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", int(limiter.burst)))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

			if !allowed {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(limiter.interval.Seconds())))
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
