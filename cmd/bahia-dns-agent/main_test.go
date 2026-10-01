package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
