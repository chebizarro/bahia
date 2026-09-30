package app

import (
	"sync"
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
			require.Equal(t, tt.tier, policy.ActiveTier())
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
	require.Equal(t, Tier2, policy.ActiveTier())
	require.True(t, policy.IsDegraded())
	require.True(t, policy.AllowsTier(Tier2))
	require.False(t, policy.AllowsTier(Tier3))

	policy.SetActiveTier(Tier3)
	require.False(t, policy.IsDegraded())
}
func TestModePolicyDependencyCapStillAllowsLoweringAndRaisingWithinCap(t *testing.T) {
	policy := NewModePolicy(ModeFull)
	policy.CapTier(Tier2)
	require.Equal(t, Tier2, policy.ActiveTier())
	require.Equal(t, Tier2, policy.MaxTier())

	policy.SetActiveTier(Tier0)
	require.Equal(t, Tier0, policy.ActiveTier())
	policy.SetActiveTier(Tier3)
	require.Equal(t, Tier2, policy.ActiveTier())

	// A later, lower cap wins; a higher cap never loosens an earlier one.
	policy.CapTier(Tier1)
	policy.CapTier(Tier3)
	require.Equal(t, Tier1, policy.MaxTier())
	require.Equal(t, Tier1, policy.ActiveTier())
}

// TestModePolicyActiveTierIsRaceFree mirrors production: the bootstrap
// goroutine calls SetActiveTier (and startup calls CapTier) while HTTP route
// gating, runner gating and health snapshots read the active tier. Run with
// -race; any unsynchronized access fails the test.
func TestModePolicyActiveTierIsRaceFree(t *testing.T) {
	policy := NewModePolicy(ModeFull)
	const iterations = 2000
	var wg sync.WaitGroup
	start := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			policy.SetActiveTier(Tier(i % 4))
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			policy.CapTier(Tier3 - Tier(i%2))
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if active := policy.ActiveTier(); active < Tier0 || active > Tier3 {
					t.Errorf("active tier %d out of range", active)
				}
				_ = policy.RouteEnabled(Tier2)
				_ = policy.RunnerEnabled(Tier3)
				_ = policy.IsDegraded()
				_ = policy.RouteErrorBody(int(Tier2))
				_ = currentMode(policy)
			}
		}()
	}
	close(start)
	wg.Wait()

	// The cap only tightens, so it settled at Tier2, and no SetActiveTier may
	// have pushed the active tier above it.
	require.Equal(t, Tier2, policy.MaxTier())
	require.LessOrEqual(t, policy.ActiveTier(), Tier2)
}
