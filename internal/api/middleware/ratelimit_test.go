package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func newTestLimiter(rate int, interval time.Duration) *IPRateLimiter {
	rl := NewIPRateLimiter(RateLimiterConfig{
		Rate:            rate,
		Interval:        interval,
		CleanupInterval: interval * 2,
	})
	return rl
}

func TestIPRateLimiter_AllowsUpToRate(t *testing.T) {
	rl := newTestLimiter(5, time.Minute)
	defer rl.Stop()

	for i := 0; i < 5; i++ {
		allowed, _ := rl.Allow("192.168.1.1")
		if !allowed {
			t.Fatalf("request %d should have been allowed", i+1)
		}
	}

	allowed, remaining := rl.Allow("192.168.1.1")
	if allowed {
		t.Error("6th request should have been rejected")
	}
	if remaining != 0 {
		t.Errorf("remaining should be 0, got %d", remaining)
	}
}

func TestIPRateLimiter_SeparateIPs(t *testing.T) {
	rl := newTestLimiter(2, time.Minute)
	defer rl.Stop()

	// Exhaust IP1
	rl.Allow("10.0.0.1")
	rl.Allow("10.0.0.1")

	allowed, _ := rl.Allow("10.0.0.1")
	if allowed {
		t.Error("IP1 should be rate limited")
	}

	// IP2 should still be fine
	allowed, _ = rl.Allow("10.0.0.2")
	if !allowed {
		t.Error("IP2 should not be rate limited")
	}
}

func TestIPRateLimiter_TokenReplenish(t *testing.T) {
	// Create limiter that allows 10 req / 100ms = refills very fast
	rl := newTestLimiter(10, 100*time.Millisecond)
	defer rl.Stop()

	// Exhaust all tokens
	for i := 0; i < 10; i++ {
		rl.Allow("10.0.0.1")
	}

	allowed, _ := rl.Allow("10.0.0.1")
	if allowed {
		t.Error("should be rate limited after exhausting tokens")
	}

	// Wait for tokens to refill
	time.Sleep(120 * time.Millisecond)

	allowed, _ = rl.Allow("10.0.0.1")
	if !allowed {
		t.Error("should be allowed after token replenishment")
	}
}

func TestIPRateLimiter_RemainingCount(t *testing.T) {
	rl := newTestLimiter(3, time.Minute)
	defer rl.Stop()

	_, rem := rl.Allow("10.0.0.1")
	if rem != 2 {
		t.Errorf("remaining should be 2, got %d", rem)
	}

	_, rem = rl.Allow("10.0.0.1")
	if rem != 1 {
		t.Errorf("remaining should be 1, got %d", rem)
	}

	_, rem = rl.Allow("10.0.0.1")
	if rem != 0 {
		t.Errorf("remaining should be 0, got %d", rem)
	}
}

func TestIPRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := newTestLimiter(100, time.Minute)
	defer rl.Stop()

	var wg sync.WaitGroup
	allowed := make(chan bool, 200)

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := rl.Allow("10.0.0.1")
			allowed <- ok
		}()
	}

	wg.Wait()
	close(allowed)

	var allowedCount int
	for ok := range allowed {
		if ok {
			allowedCount++
		}
	}

	if allowedCount != 100 {
		t.Errorf("expected exactly 100 allowed requests, got %d", allowedCount)
	}
}

func TestIPRateLimiter_DefaultConfig(t *testing.T) {
	// Zero values should get defaults
	rl := NewIPRateLimiter(RateLimiterConfig{})
	defer rl.Stop()

	if rl.burst != 100 {
		t.Errorf("expected default burst 100, got %v", rl.burst)
	}
}

func TestRateLimitMiddleware_AllowedRequest(t *testing.T) {
	rl := newTestLimiter(10, time.Minute)
	defer rl.Stop()

	handler := RateLimit(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "10" {
		t.Errorf("X-RateLimit-Limit = %q, want %q", got, "10")
	}

	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "9" {
		t.Errorf("X-RateLimit-Remaining = %q, want %q", got, "9")
	}
}

func TestRateLimitMiddleware_BlockedRequest(t *testing.T) {
	rl := newTestLimiter(1, time.Minute)
	defer rl.Stop()

	handler := RateLimit(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request allowed
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first request should be allowed, got %d", rec.Code)
	}

	// Second request blocked
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rec.Code)
	}

	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want %q", got, "60")
	}

	// Verify JSON error body
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["error"] != "rate limit exceeded" {
		t.Errorf("error = %q, want %q", body["error"], "rate limit exceeded")
	}
}

func TestRateLimitMiddleware_WriteLimiter(t *testing.T) {
	// Simulate separate read/write limiters
	readLimiter := newTestLimiter(10, time.Minute)
	defer readLimiter.Stop()
	writeLimiter := newTestLimiter(2, time.Minute)
	defer writeLimiter.Stop()

	readHandler := RateLimit(readLimiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	writeHandler := RateLimit(writeLimiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ip := "10.0.0.1:1234"

	// Exhaust write limit
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/services", nil)
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()
		writeHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("write request %d should be allowed", i+1)
		}
	}

	// Write should be blocked
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services", nil)
	req.RemoteAddr = ip
	rec := httptest.NewRecorder()
	writeHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Error("write should be rate limited")
	}

	// Read from same IP should still work
	req = httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.RemoteAddr = ip
	rec = httptest.NewRecorder()
	readHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Error("read should still be allowed with separate limiter")
	}
}

func TestIPRateLimiter_Cleanup(t *testing.T) {
	rl := NewIPRateLimiter(RateLimiterConfig{
		Rate:            5,
		Interval:        50 * time.Millisecond,
		CleanupInterval: 60 * time.Millisecond,
	})
	defer rl.Stop()

	rl.Allow("10.0.0.1")

	// Verify visitor exists
	rl.mu.Lock()
	if len(rl.visitors) != 1 {
		t.Errorf("expected 1 visitor, got %d", len(rl.visitors))
	}
	rl.mu.Unlock()

	// Wait for cleanup to run
	time.Sleep(150 * time.Millisecond)

	rl.mu.Lock()
	count := len(rl.visitors)
	rl.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 visitors after cleanup, got %d", count)
	}
}
