package nostr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayHealth_RecordPublishSuccess(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")

	h.RecordPublishSuccess(100 * time.Millisecond)
	h.RecordPublishSuccess(200 * time.Millisecond)

	assert.Equal(t, int64(2), h.PublishAttempts)
	assert.Equal(t, int64(2), h.PublishSuccess)
	assert.Equal(t, int64(0), h.PublishFailed)
	assert.InDelta(t, 0.15, h.AvgLatency(), 0.01) // average of 0.1 and 0.2
}

func TestRelayHealth_RecordPublishFailure(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")

	h.RecordPublishSuccess(100 * time.Millisecond)
	h.RecordPublishFailure("auth-required")

	assert.Equal(t, int64(2), h.PublishAttempts)
	assert.Equal(t, int64(1), h.PublishSuccess)
	assert.Equal(t, int64(1), h.PublishFailed)
	assert.Equal(t, "auth-required", h.LastError)
	assert.False(t, h.LastErrorTime.IsZero())
}

func TestRelayHealth_SuccessRate(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")

	// No data - assume healthy
	assert.Equal(t, 1.0, h.SuccessRate())

	// 80% success rate
	for i := 0; i < 8; i++ {
		h.RecordPublishSuccess(50 * time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		h.RecordPublishFailure("error")
	}

	assert.InDelta(t, 0.8, h.SuccessRate(), 0.01)
}

func TestRelayHealth_ConnectionState(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")

	assert.False(t, h.Connected)

	h.SetConnected(true)
	assert.True(t, h.Connected)
	assert.False(t, h.LastConnected.IsZero())

	h.SetConnected(false)
	assert.False(t, h.Connected)
}

func TestRelayHealth_Stats(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")

	h.RecordPublishSuccess(100 * time.Millisecond)
	h.RecordPublishSuccess(200 * time.Millisecond)
	h.RecordPublishFailure("rate-limited")
	h.RecordReconnect()
	h.SetConnected(true)

	stats := h.Stats()

	assert.Equal(t, "wss://test.relay", stats.URL)
	assert.True(t, stats.Connected)
	assert.Equal(t, int64(3), stats.PublishAttempts)
	assert.Equal(t, int64(2), stats.PublishSuccess)
	assert.Equal(t, int64(1), stats.PublishFailed)
	assert.Equal(t, int64(1), stats.Reconnects)
	assert.InDelta(t, 0.666, stats.SuccessRate, 0.01)
	assert.InDelta(t, 0.15, stats.AvgLatencySeconds, 0.01)
	assert.Equal(t, "rate-limited", stats.LastError)
}

func TestRelayHealthStats_IsHealthy(t *testing.T) {
	tests := []struct {
		name          string
		stats         RelayHealthStats
		expectHealthy bool
	}{
		{
			name: "no data - healthy",
			stats: RelayHealthStats{
				PublishAttempts: 0,
				SuccessRate:     1.0,
				Connected:       true,
			},
			expectHealthy: true,
		},
		{
			name: "high success rate - healthy",
			stats: RelayHealthStats{
				PublishAttempts: 100,
				SuccessRate:     0.95,
				Connected:       true,
			},
			expectHealthy: true,
		},
		{
			name: "low success rate - unhealthy",
			stats: RelayHealthStats{
				PublishAttempts: 100,
				SuccessRate:     0.7,
				Connected:       true,
			},
			expectHealthy: false,
		},
		{
			name: "disconnected with recent error - unhealthy",
			stats: RelayHealthStats{
				PublishAttempts: 10,
				SuccessRate:     0.9,
				Connected:       false,
				LastErrorTime:   time.Now().Add(-1 * time.Minute),
			},
			expectHealthy: false,
		},
		{
			name: "disconnected with old error - healthy",
			stats: RelayHealthStats{
				PublishAttempts: 10,
				SuccessRate:     0.9,
				Connected:       false,
				LastErrorTime:   time.Now().Add(-10 * time.Minute),
			},
			expectHealthy: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectHealthy, tt.stats.IsHealthy())
		})
	}
}

func TestRelayHealthStats_IsDegraded(t *testing.T) {
	tests := []struct {
		name           string
		stats          RelayHealthStats
		expectDegraded bool
	}{
		{
			name: "too few samples - not degraded",
			stats: RelayHealthStats{
				PublishAttempts: 5,
				SuccessRate:     0.85,
			},
			expectDegraded: false,
		},
		{
			name: "success rate 85% - degraded",
			stats: RelayHealthStats{
				PublishAttempts: 100,
				SuccessRate:     0.85,
			},
			expectDegraded: true,
		},
		{
			name: "success rate 95% - not degraded",
			stats: RelayHealthStats{
				PublishAttempts: 100,
				SuccessRate:     0.95,
			},
			expectDegraded: false,
		},
		{
			name: "success rate 75% - unhealthy not degraded",
			stats: RelayHealthStats{
				PublishAttempts: 100,
				SuccessRate:     0.75,
			},
			expectDegraded: false, // unhealthy, not just degraded
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectDegraded, tt.stats.IsDegraded())
		})
	}
}

func TestRelayHealthTracker_GetOrCreate(t *testing.T) {
	tracker := NewRelayHealthTracker()

	h1 := tracker.GetOrCreate("wss://relay1.example.com")
	h2 := tracker.GetOrCreate("wss://relay2.example.com")
	h3 := tracker.GetOrCreate("wss://relay1.example.com") // same as h1

	require.NotNil(t, h1)
	require.NotNil(t, h2)
	assert.Same(t, h1, h3) // should be same instance
	assert.Equal(t, 2, tracker.TotalCount())
}

func TestRelayHealthTracker_AllStats(t *testing.T) {
	tracker := NewRelayHealthTracker()

	h1 := tracker.GetOrCreate("wss://relay1.example.com")
	h2 := tracker.GetOrCreate("wss://relay2.example.com")

	h1.RecordPublishSuccess(100 * time.Millisecond)
	h2.RecordPublishFailure("error")

	stats := tracker.AllStats()
	assert.Len(t, stats, 2)
}

func TestRelayHealthTracker_HealthCounts(t *testing.T) {
	tracker := NewRelayHealthTracker()

	// Healthy relay
	h1 := tracker.GetOrCreate("wss://healthy.relay")
	for i := 0; i < 20; i++ {
		h1.RecordPublishSuccess(50 * time.Millisecond)
	}
	h1.SetConnected(true)

	// Unhealthy relay
	h2 := tracker.GetOrCreate("wss://unhealthy.relay")
	for i := 0; i < 20; i++ {
		h2.RecordPublishFailure("error")
	}
	h2.SetConnected(false)

	assert.Equal(t, 1, tracker.HealthyCount())
	assert.Len(t, tracker.UnhealthyRelays(), 1)
	assert.Contains(t, tracker.UnhealthyRelays(), "wss://unhealthy.relay")
}

func TestRelayHealth_RecordsClosedAndRecoveryAttempts(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")
	h.RecordClosed("auth-required: sign in")
	h.RecordClosed("auth-required: retry")
	h.RecordClosed("unstructured relay message")
	h.RecordReREQ()
	h.RecordReconnect()

	stats := h.Stats()
	assert.Equal(t, int64(2), stats.ClosedReasons["auth-required"])
	assert.Equal(t, int64(1), stats.ClosedReasons["other"])
	assert.Equal(t, int64(1), stats.ReREQAttempts)
	assert.Equal(t, int64(1), stats.Reconnects)

	stats.ClosedReasons["auth-required"] = 99
	assert.Equal(t, int64(2), h.Stats().ClosedReasons["auth-required"], "snapshot must not alias tracker state")
}

func TestRelayHealth_P95Latency(t *testing.T) {
	h := NewRelayHealth("wss://test.relay")

	// Add 100 samples with increasing latency
	for i := 1; i <= 100; i++ {
		h.RecordPublishSuccess(time.Duration(i) * time.Millisecond)
	}

	p95 := h.P95Latency()
	// P95 of 1-100ms should be around 95ms
	assert.InDelta(t, 0.095, p95, 0.005)
}
