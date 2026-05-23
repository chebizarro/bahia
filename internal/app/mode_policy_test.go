package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewModePolicyMapsModeToTier(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		tier Tier
	}{
		{name: "full", mode: ModeFull, tier: Tier3},
		{name: "degraded", mode: ModeDegraded, tier: Tier2},
		{name: "emergency", mode: ModeEmergency, tier: Tier1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := NewModePolicy(tt.mode)

			require.Equal(t, tt.mode, policy.RequestedMode)
			require.Equal(t, tt.tier, policy.RequestedTier)
			require.Equal(t, tt.tier, policy.ActiveTier)
		})
	}
}

func TestModePolicyAllowsTierBoundaries(t *testing.T) {
	policy := NewModePolicy(ModeDegraded)

	require.True(t, policy.AllowsTier(Tier0))
	require.True(t, policy.AllowsTier(Tier1))
	require.True(t, policy.AllowsTier(Tier2))
	require.False(t, policy.AllowsTier(Tier3))
}

func TestModePolicyRouteAndRunnerEnabled(t *testing.T) {
	policy := NewModePolicy(ModeEmergency)

	require.True(t, policy.RouteEnabled(Tier0))
	require.True(t, policy.RouteEnabled(Tier1))
	require.False(t, policy.RouteEnabled(Tier2))

	require.True(t, policy.RunnerEnabled(Tier0))
	require.True(t, policy.RunnerEnabled(Tier1))
	require.False(t, policy.RunnerEnabled(Tier2))
}

func TestModePolicySetActiveTierAndIsDegraded(t *testing.T) {
	policy := NewModePolicy(ModeFull)

	require.False(t, policy.IsDegraded())

	policy.SetActiveTier(Tier2)
	require.Equal(t, Tier2, policy.ActiveTier)
	require.True(t, policy.IsDegraded())
	require.True(t, policy.AllowsTier(Tier2))
	require.False(t, policy.AllowsTier(Tier3))

	policy.SetActiveTier(Tier3)
	require.False(t, policy.IsDegraded())
}
