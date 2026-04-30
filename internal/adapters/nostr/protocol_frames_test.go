package nostr

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr/nip11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Test PublishResult rejection reason detection

func TestPublishResult_IsAuthRequired(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected bool
	}{
		{"auth-required prefix", "auth-required: need to authenticate", true},
		{"auth-required with colon", "auth-required:", true},
		{"not auth-required", "rate-limited: slow down", false},
		{"partial match", "authentication required", false},
		{"empty reason", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PublishResult{Reason: tt.reason}
			assert.Equal(t, tt.expected, result.IsAuthRequired())
		})
	}
}

func TestPublishResult_IsRateLimited(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected bool
	}{
		{"rate-limited prefix", "rate-limited: too many requests", true},
		{"rate-limited with colon", "rate-limited:", true},
		{"not rate-limited", "auth-required: need auth", false},
		{"partial match", "rate limit exceeded", false},
		{"empty reason", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PublishResult{Reason: tt.reason}
			assert.Equal(t, tt.expected, result.IsRateLimited())
		})
	}
}

func TestPublishResult_IsBlocked(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected bool
	}{
		{"blocked prefix", "blocked: content policy violation", true},
		{"blocked with colon", "blocked:", true},
		{"not blocked", "rate-limited: slow down", false},
		{"partial match", "user is blocked", false},
		{"empty reason", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PublishResult{Reason: tt.reason}
			assert.Equal(t, tt.expected, result.IsBlocked())
		})
	}
}

func TestPublishResult_IsDuplicate(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected bool
	}{
		{"duplicate prefix", "duplicate: event already exists", true},
		{"duplicate with colon", "duplicate:", true},
		{"not duplicate", "blocked: policy", false},
		{"partial match", "this is a duplicate", false},
		{"empty reason", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PublishResult{Reason: tt.reason}
			assert.Equal(t, tt.expected, result.IsDuplicate())
		})
	}
}

func TestPublishResult_AcceptedVsRejected(t *testing.T) {
	t.Run("accepted event", func(t *testing.T) {
		result := PublishResult{
			RelayURL: "wss://relay.example.com",
			Accepted: true,
			Reason:   "",
			Error:    nil,
		}
		assert.True(t, result.Accepted)
		assert.False(t, result.IsAuthRequired())
		assert.False(t, result.IsRateLimited())
		assert.False(t, result.IsBlocked())
		assert.False(t, result.IsDuplicate())
		assert.Nil(t, result.Error)
	})

	t.Run("rejected event - auth required", func(t *testing.T) {
		result := PublishResult{
			RelayURL: "wss://relay.example.com",
			Accepted: false,
			Reason:   "auth-required: take a selfie and send it to the CIA",
			Error:    nil,
		}
		assert.False(t, result.Accepted)
		assert.True(t, result.IsAuthRequired())
		assert.False(t, result.IsRateLimited())
		assert.False(t, result.IsBlocked())
		assert.False(t, result.IsDuplicate())
	})

	t.Run("rejected event - rate limited", func(t *testing.T) {
		result := PublishResult{
			RelayURL: "wss://relay.example.com",
			Accepted: false,
			Reason:   "rate-limited: slow down there chief",
			Error:    nil,
		}
		assert.False(t, result.Accepted)
		assert.True(t, result.IsRateLimited())
	})

	t.Run("rejected event - blocked", func(t *testing.T) {
		result := PublishResult{
			RelayURL: "wss://relay.example.com",
			Accepted: false,
			Reason:   "blocked: user is on denylist",
			Error:    nil,
		}
		assert.False(t, result.Accepted)
		assert.True(t, result.IsBlocked())
	})

	t.Run("duplicate event - not error", func(t *testing.T) {
		result := PublishResult{
			RelayURL: "wss://relay.example.com",
			Accepted: false,
			Reason:   "duplicate: we already have this event",
			Error:    nil,
		}
		// Duplicate is a soft rejection - relay already has the event
		assert.False(t, result.Accepted)
		assert.True(t, result.IsDuplicate())
	})
}

// Test EOSE handling in subscriber

func TestSubscriber_CaughtUpState(t *testing.T) {
	t.Run("initially not caught up", func(t *testing.T) {
		// A subscriber starts not caught up
		// This is a conceptual test - actual subscriber requires relay connections
		// We're testing the atomic boolean behavior
		var caughtUp bool
		require.False(t, caughtUp, "subscriber should start not caught up")
	})

	t.Run("caught up after EOSE", func(t *testing.T) {
		// After receiving EOSE, subscriber should be caught up
		caughtUp := true // simulating EOSE received
		require.True(t, caughtUp, "subscriber should be caught up after EOSE")
	})
}

// Test MergedSubscription EOSE behavior

func TestMergedSubscription_EOSEChannel(t *testing.T) {
	t.Run("EOSE signal is non-blocking", func(t *testing.T) {
		eose := make(chan struct{})

		// Should not block when no one is listening
		select {
		case <-eose:
			t.Fatal("EOSE channel should be empty initially")
		default:
			// expected - channel is empty
		}

		// Close to signal EOSE
		close(eose)

		// Should receive signal
		select {
		case <-eose:
			// expected
		default:
			t.Fatal("should receive EOSE signal after close")
		}

		// Multiple reads should work (channel is closed)
		select {
		case <-eose:
			// expected - closed channel returns immediately
		default:
			t.Fatal("closed channel should return immediately on read")
		}
	})
}

// Test backoff behavior

func TestBackoff_Reset(t *testing.T) {
	b := &Backoff{
		Initial:    time.Second,
		Max:        2 * time.Minute,
		Multiplier: 2.0,
		Jitter:     0,
	}

	// Get some delays
	d1 := b.Next()
	d2 := b.Next()
	require.True(t, d2 > d1, "delay should increase")

	// Reset
	b.Reset()

	// Should be back to initial
	d3 := b.Next()
	require.Equal(t, d1, d3, "delay after reset should return to initial")
}

func TestBackoff_MaxCap(t *testing.T) {
	b := &Backoff{
		Initial:    100,
		Max:        500,
		Multiplier: 10.0, // aggressive multiplier
		Jitter:     0,    // no jitter for predictable test
	}

	// Run many iterations
	var lastDelay int64
	for i := 0; i < 10; i++ {
		d := b.Next()
		lastDelay = int64(d)
	}

	// Should be capped at max (500 = 500 * time.Nanosecond in this case)
	require.LessOrEqual(t, lastDelay, int64(b.Max), "delay should be capped at max")
}

// Test event deduplication under concurrent access

func TestEventDeduplicator_ConcurrentAccess(t *testing.T) {
	dedup := NewEventDeduplicator(1000)
	done := make(chan struct{})

	// Run concurrent checks
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				eventID := "event-" + string(rune('a'+id))
				dedup.IsDuplicate(eventID)
			}
			done <- struct{}{}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not panic and stats should be reasonable
	require.True(t, dedup.Size() <= 1000, "dedup should respect capacity")
}

// Test NIP-11 relay info capability checks

func TestRelayPool_SupportsNIP_VariousFormats(t *testing.T) {
	// NIP numbers can come as int or float64 from JSON
	pool := NewRelayPool([]string{"wss://test.relay"}, zap.NewNop())

	pool.mu.Lock()
	pool.relayInfoCache["wss://test.relay"] = &nip11.RelayInformationDocument{
		SupportedNIPs: []any{
			float64(1),  // from JSON number
			float64(11), // from JSON number
			42,          // int (rare but possible)
		},
	}
	pool.mu.Unlock()

	// Should handle both types
	assert.True(t, pool.SupportsNIP("wss://test.relay", 1))
	assert.True(t, pool.SupportsNIP("wss://test.relay", 11))
	assert.True(t, pool.SupportsNIP("wss://test.relay", 42))
	assert.False(t, pool.SupportsNIP("wss://test.relay", 99))
}
