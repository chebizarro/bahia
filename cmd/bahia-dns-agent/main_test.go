package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	// The transport tests publish real requests and replies through multiple
	// pools. Keep admission enabled without sharing the production-sized burst
	// across otherwise independent test cases and -count repetitions.
	budget := nostrout.PurposeBudget{RatePerMinute: 6_000, Burst: 200}
	nostrout.InitDefault(nostrout.Config{
		Aggregate: budget,
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: budget,
			nostrout.PurposeState:    budget,
			nostrout.PurposeGeneral:  budget,
			nostrout.PurposeBulk:     budget,
			nostrout.PurposeSigner:   budget,
		},
		RelayWire:         budget,
		RelayWirePriority: budget,
	})
	os.Exit(m.Run())
}

func TestDNSAgentEventStoreDefaultsNextToDurableState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state", "serials.json")
	cfg, err := validateConfig(config{
		PrivateKeyFile:   "/keys/dns-agent.key",
		RelayURLs:        []string{"ws://relay.example.test"},
		AuthorizedPubkey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
		IncludeDir:       t.TempDir(), AllowedZones: []string{"example.internal"},
		StateFilePath: state,
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(filepath.Dir(state), ".bahia-dns-agent-events.bolt"), cfg.StorePath)
}
