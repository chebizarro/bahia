package app

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSoulRuntimePolicyAttestsResolvedControllerAndPinnedRuntimes(t *testing.T) {
	controller, pinned := strings.Repeat("c", 64), strings.Repeat("d", 64)
	sf := config.SoulFactoryConfig{
		AgentRuntimes:  []string{"openclaw", "metiq"},
		RuntimePubkeys: map[string][]string{"openclaw": {pinned}},
	}

	// The controller is the identity resolved at startup, not the config field:
	// it may come from the Signet signer when soul_factory_pubkey is unset.
	policy := soulRuntimePolicy(sf, &soulFactoryRuntime{runner: &soulFactoryRunner{controllerPubkey: controller}})
	require.Equal(t, []string{"openclaw", "metiq"}, policy.AgentRuntimes)
	require.Equal(t, []string{controller}, policy.ControllerPubkeys)
	require.Equal(t, map[string][]string{"openclaw": {pinned}}, policy.RuntimePubkeys)

	// The published policy must not alias validated config.
	policy.RuntimePubkeys["openclaw"][0] = "mutated"
	policy.AgentRuntimes[0] = "mutated"
	require.Equal(t, pinned, sf.RuntimePubkeys["openclaw"][0])
	require.Equal(t, "openclaw", sf.AgentRuntimes[0])
}

func TestSoulRuntimePolicyNamesNoKeysWhenSoulFactoryIsDisabled(t *testing.T) {
	sf := config.SoulFactoryConfig{
		AgentRuntimes:  []string{"openclaw"},
		RuntimePubkeys: map[string][]string{"openclaw": {strings.Repeat("d", 64)}},
	}
	policy := soulRuntimePolicy(sf, nil)
	require.Equal(t, []string{"openclaw"}, policy.AgentRuntimes)
	require.Empty(t, policy.ControllerPubkeys)
	require.Empty(t, policy.RuntimePubkeys)
}
