package nostr

import (
	"testing"

	"fiatjaf.com/nostr/nip11"
	"go.uber.org/zap"
)

func TestRelayPool_GetRelayInfo_NotCached(t *testing.T) {
	pool := NewRelayPool([]string{"wss://test.relay"}, zap.NewNop())

	info := pool.GetRelayInfo("wss://test.relay")
	if info != nil {
		t.Error("expected nil for uncached relay info")
	}
}

func TestRelayPool_SupportsNIP(t *testing.T) {
	pool := NewRelayPool([]string{"wss://test.relay"}, zap.NewNop())

	// Manually populate cache for testing
	pool.mu.Lock()
	pool.relayInfoCache["wss://test.relay"] = &nip11.RelayInformationDocument{
		SupportedNIPs: []any{float64(1), float64(11), float64(42)},
	}
	pool.mu.Unlock()

	tests := []struct {
		nip      int
		expected bool
	}{
		{1, true},
		{11, true},
		{42, true},
		{99, false},
		{0, false},
	}

	for _, tt := range tests {
		if got := pool.SupportsNIP("wss://test.relay", tt.nip); got != tt.expected {
			t.Errorf("SupportsNIP(%d) = %v, want %v", tt.nip, got, tt.expected)
		}
	}
}

func TestRelayPool_IsAuthRequired(t *testing.T) {
	pool := NewRelayPool([]string{"wss://auth.relay", "wss://open.relay"}, zap.NewNop())

	// Populate cache with different auth requirements
	pool.mu.Lock()
	pool.relayInfoCache["wss://auth.relay"] = &nip11.RelayInformationDocument{
		Limitation: &nip11.RelayLimitationDocument{
			AuthRequired: true,
		},
	}
	pool.relayInfoCache["wss://open.relay"] = &nip11.RelayInformationDocument{
		Limitation: &nip11.RelayLimitationDocument{
			AuthRequired: false,
		},
	}
	pool.mu.Unlock()

	if !pool.IsAuthRequired("wss://auth.relay") {
		t.Error("expected auth.relay to require auth")
	}
	if pool.IsAuthRequired("wss://open.relay") {
		t.Error("expected open.relay to not require auth")
	}
	if pool.IsAuthRequired("wss://unknown.relay") {
		t.Error("expected uncached relay to return false")
	}
}

func TestRelayPool_GetMaxLimit(t *testing.T) {
	pool := NewRelayPool([]string{"wss://test.relay"}, zap.NewNop())

	pool.mu.Lock()
	pool.relayInfoCache["wss://test.relay"] = &nip11.RelayInformationDocument{
		Limitation: &nip11.RelayLimitationDocument{
			MaxLimit: 500,
		},
	}
	pool.mu.Unlock()

	if got := pool.GetMaxLimit("wss://test.relay"); got != 500 {
		t.Errorf("GetMaxLimit() = %d, want 500", got)
	}
	if got := pool.GetMaxLimit("wss://unknown.relay"); got != 0 {
		t.Errorf("GetMaxLimit(unknown) = %d, want 0", got)
	}
}

func TestRelayPool_GetMaxSubscriptions(t *testing.T) {
	pool := NewRelayPool([]string{"wss://test.relay"}, zap.NewNop())

	pool.mu.Lock()
	pool.relayInfoCache["wss://test.relay"] = &nip11.RelayInformationDocument{
		Limitation: &nip11.RelayLimitationDocument{
			MaxSubscriptions: 20,
		},
	}
	pool.mu.Unlock()

	if got := pool.GetMaxSubscriptions("wss://test.relay"); got != 20 {
		t.Errorf("GetMaxSubscriptions() = %d, want 20", got)
	}
	if got := pool.GetMaxSubscriptions("wss://unknown.relay"); got != 0 {
		t.Errorf("GetMaxSubscriptions(unknown) = %d, want 0", got)
	}
}

func TestRelayPool_RelayInfoCache_Initialization(t *testing.T) {
	pool := NewRelayPool([]string{"wss://test.relay"}, zap.NewNop())

	if pool.relayInfoCache == nil {
		t.Error("relayInfoCache should be initialized")
	}
	if len(pool.relayInfoCache) != 0 {
		t.Error("relayInfoCache should be empty initially")
	}
}
