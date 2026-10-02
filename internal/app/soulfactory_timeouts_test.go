package app

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openagentsinc/bahia/internal/soulfactory"
)

// soul_factory.runtime_result_timeout reaches every runtime adapter; unset, it
// is the 5m default rather than an unbounded wait.
func TestNewWiresSoulFactoryRuntimeResultTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured time.Duration
		want       time.Duration
	}{
		{name: "configured", configured: 7 * time.Minute, want: 7 * time.Minute},
		{name: "default", configured: 0, want: soulfactory.DefaultRuntimeControlResultTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
			defer restoreDBHooks()

			signer := newFakeSoulFactorySigner(t)
			var adapterConfigs []soulfactory.RuntimeAdapterConfig
			restoreSoulFactoryHooks := stubSoulFactoryHooks(t, signer, func(cfg soulfactory.RuntimeAdapterConfig) {
				adapterConfigs = append(adapterConfigs, cfg)
			})
			defer restoreSoulFactoryHooks()

			cfg := startupTestConfig("full")
			configureValidSoulFactory(t, cfg, signer.pubkey)
			cfg.SoulFactory.AgentRuntimes = []string{"openclaw", "metiq"}
			cfg.SoulFactory.RuntimeResultTimeout = tc.configured
			app, err := New(cfg)
			require.NoError(t, err)
			defer syncTestLogger(t, app.Logger)
			defer closeRelayPools(app.relayPools...)
			defer closeWithNoError(t, app.soulFactoryCloser)

			require.Len(t, adapterConfigs, 2)
			for _, adapterConfig := range adapterConfigs {
				require.Equal(t, tc.want, adapterConfig.ResultTimeout, "runtime %s", adapterConfig.Target)
			}
		})
	}
}
