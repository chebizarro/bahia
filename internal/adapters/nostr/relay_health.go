// Package nostr provides Nostr relay health tracking for monitoring relay performance.
package nostr

import (
	"sync"
	"time"
)

// RelayHealth tracks the health and performance of a single relay.
type RelayHealth struct {
	mu sync.RWMutex

	URL string

	// Counters
	PublishAttempts int64
	PublishSuccess  int64
	PublishFailed   int64
	Reconnects      int64

	// Recent errors
	LastError     string
	LastErrorTime time.Time

	// Connection state
	Connected     bool
	LastConnected time.Time

	// Latency tracking (sliding window)
	latencies    []float64 // in seconds
	latencyIndex int
	latencyCount int
}

const maxLatencySamples = 100

// NewRelayHealth creates a new RelayHealth tracker for a relay URL.
func NewRelayHealth(url string) *RelayHealth {
	return &RelayHealth{
		URL:       url,
		latencies: make([]float64, maxLatencySamples),
	}
}

// RecordPublishSuccess records a successful publish with latency.
func (h *RelayHealth) RecordPublishSuccess(latency time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.PublishAttempts++
	h.PublishSuccess++
	h.recordLatency(latency.Seconds())
}

// RecordPublishFailure records a failed publish with reason.
func (h *RelayHealth) RecordPublishFailure(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.PublishAttempts++
	h.PublishFailed++
	h.LastError = reason
	h.LastErrorTime = time.Now()
}

// RecordReconnect records a reconnection attempt.
func (h *RelayHealth) RecordReconnect() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Reconnects++
}

// SetConnected updates the connection state.
func (h *RelayHealth) SetConnected(connected bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Connected = connected
	if connected {
		h.LastConnected = time.Now()
	}
}

// recordLatency adds a latency sample to the sliding window (must hold lock).
func (h *RelayHealth) recordLatency(seconds float64) {
	h.latencies[h.latencyIndex] = seconds
	h.latencyIndex = (h.latencyIndex + 1) % maxLatencySamples
	if h.latencyCount < maxLatencySamples {
		h.latencyCount++
	}
}

// SuccessRate returns the publish success rate (0.0 to 1.0).
func (h *RelayHealth) SuccessRate() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.PublishAttempts == 0 {
		return 1.0 // No data, assume healthy
	}
	return float64(h.PublishSuccess) / float64(h.PublishAttempts)
}

// AvgLatency returns the average publish latency in seconds.
func (h *RelayHealth) AvgLatency() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.latencyCount == 0 {
		return 0
	}

	var sum float64
	for i := 0; i < h.latencyCount; i++ {
		sum += h.latencies[i]
	}
	return sum / float64(h.latencyCount)
}

// P95Latency returns the 95th percentile latency in seconds.
func (h *RelayHealth) P95Latency() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.latencyCount == 0 {
		return 0
	}

	// Copy and sort latencies
	samples := make([]float64, h.latencyCount)
	copy(samples, h.latencies[:h.latencyCount])
	sortFloat64s(samples)

	idx := int(float64(len(samples)) * 0.95)
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

// Stats returns a snapshot of relay health statistics.
func (h *RelayHealth) Stats() RelayHealthStats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stats := RelayHealthStats{
		URL:             h.URL,
		Connected:       h.Connected,
		PublishAttempts: h.PublishAttempts,
		PublishSuccess:  h.PublishSuccess,
		PublishFailed:   h.PublishFailed,
		Reconnects:      h.Reconnects,
		LastError:       h.LastError,
		LastErrorTime:   h.LastErrorTime,
		LastConnected:   h.LastConnected,
	}

	if h.PublishAttempts > 0 {
		stats.SuccessRate = float64(h.PublishSuccess) / float64(h.PublishAttempts)
	} else {
		stats.SuccessRate = 1.0
	}

	if h.latencyCount > 0 {
		var sum float64
		for i := 0; i < h.latencyCount; i++ {
			sum += h.latencies[i]
		}
		stats.AvgLatencySeconds = sum / float64(h.latencyCount)
	}

	return stats
}

// RelayHealthStats is a snapshot of relay health for export.
type RelayHealthStats struct {
	URL               string
	Connected         bool
	PublishAttempts   int64
	PublishSuccess    int64
	PublishFailed     int64
	SuccessRate       float64 // 0.0 to 1.0
	Reconnects        int64
	AvgLatencySeconds float64
	LastError         string
	LastErrorTime     time.Time
	LastConnected     time.Time
}

// IsHealthy returns true if the relay is considered healthy.
// A relay is unhealthy if:
// - Success rate < 80%
// - Not connected and last error was recent (within 5 minutes)
func (s RelayHealthStats) IsHealthy() bool {
	if s.SuccessRate < 0.8 && s.PublishAttempts >= 10 {
		return false
	}
	if !s.Connected && time.Since(s.LastErrorTime) < 5*time.Minute {
		return false
	}
	return true
}

// IsDegraded returns true if the relay is degraded but not unhealthy.
// A relay is degraded if success rate is between 80-95%.
func (s RelayHealthStats) IsDegraded() bool {
	if s.PublishAttempts < 10 {
		return false
	}
	return s.SuccessRate >= 0.8 && s.SuccessRate < 0.95
}

// sortFloat64s is a simple insertion sort for small slices.
func sortFloat64s(a []float64) {
	for i := 1; i < len(a); i++ {
		key := a[i]
		j := i - 1
		for j >= 0 && a[j] > key {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}

// RelayHealthTracker manages health tracking for multiple relays.
type RelayHealthTracker struct {
	mu      sync.RWMutex
	relays  map[string]*RelayHealth
	alerts  []RelayHealthAlert
	alertCh chan RelayHealthAlert
}

// RelayHealthAlert represents a health alert for a relay.
type RelayHealthAlert struct {
	RelayURL  string
	AlertType string // "degraded", "unhealthy", "recovered"
	Message   string
	Timestamp time.Time
}

// NewRelayHealthTracker creates a new health tracker.
func NewRelayHealthTracker() *RelayHealthTracker {
	return &RelayHealthTracker{
		relays:  make(map[string]*RelayHealth),
		alertCh: make(chan RelayHealthAlert, 100),
	}
}

// GetOrCreate returns the health tracker for a relay, creating if needed.
func (t *RelayHealthTracker) GetOrCreate(url string) *RelayHealth {
	t.mu.Lock()
	defer t.mu.Unlock()

	if h, exists := t.relays[url]; exists {
		return h
	}

	h := NewRelayHealth(url)
	t.relays[url] = h
	return h
}

// AllStats returns health stats for all tracked relays.
func (t *RelayHealthTracker) AllStats() []RelayHealthStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := make([]RelayHealthStats, 0, len(t.relays))
	for _, h := range t.relays {
		stats = append(stats, h.Stats())
	}
	return stats
}

// UnhealthyRelays returns URLs of relays that are unhealthy.
func (t *RelayHealthTracker) UnhealthyRelays() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var unhealthy []string
	for url, h := range t.relays {
		if !h.Stats().IsHealthy() {
			unhealthy = append(unhealthy, url)
		}
	}
	return unhealthy
}

// DegradedRelays returns URLs of relays that are degraded.
func (t *RelayHealthTracker) DegradedRelays() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var degraded []string
	for url, h := range t.relays {
		if h.Stats().IsDegraded() {
			degraded = append(degraded, url)
		}
	}
	return degraded
}

// HealthyCount returns the count of healthy relays.
func (t *RelayHealthTracker) HealthyCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	count := 0
	for _, h := range t.relays {
		if h.Stats().IsHealthy() {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of tracked relays.
func (t *RelayHealthTracker) TotalCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.relays)
}

// Alerts returns the channel for health alerts.
func (t *RelayHealthTracker) Alerts() <-chan RelayHealthAlert {
	return t.alertCh
}

// CheckAndAlert checks relay health and emits alerts for state changes.
func (t *RelayHealthTracker) CheckAndAlert() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for url, h := range t.relays {
		stats := h.Stats()

		if !stats.IsHealthy() {
			alert := RelayHealthAlert{
				RelayURL:  url,
				AlertType: "unhealthy",
				Message:   "Relay is unhealthy: success rate below 80% or connection issues",
				Timestamp: time.Now(),
			}
			select {
			case t.alertCh <- alert:
			default:
				// Channel full, drop alert
			}
		} else if stats.IsDegraded() {
			alert := RelayHealthAlert{
				RelayURL:  url,
				AlertType: "degraded",
				Message:   "Relay is degraded: success rate between 80-95%",
				Timestamp: time.Now(),
			}
			select {
			case t.alertCh <- alert:
			default:
			}
		}
	}
}
