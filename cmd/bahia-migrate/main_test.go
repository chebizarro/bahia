package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDownRequiresConfirmationInEitherArgumentOrder(t *testing.T) {
	for _, args := range [][]string{{"down"}, {"down", "--force"}, {"--force", "down"}} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "down requires --confirm")
	}
}

func TestNostrFlagsAreOnlyValidForNostrAction(t *testing.T) {
	for _, args := range [][]string{{"up", "--dry-run"}, {"--relays", "wss://r.example", "status"}, {"--relay-backfill", "up"}} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "only valid for nostr")
	}
}

func TestNostrActionIsRecognisedInEitherArgumentOrder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, args := range [][]string{{"nostr", "--config", missing, "--dry-run"}, {"--config", missing, "--dry-run", "nostr"}} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.NotContains(t, errors.String(), "unknown migration action")
		require.Contains(t, errors.String(), "loading Bahia config")
	}
}

func TestDefaultMigrationRelaysPreferSidecarThenInteropRelays(t *testing.T) {
	cfg := config.NostrConfig{Relays: []string{"wss://interop.example", "wss://sidecar.internal"}}
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.BackendURL = "wss://sidecar.internal"
	require.Equal(t, []string{"wss://sidecar.internal", "wss://interop.example"}, defaultMigrationRelays(cfg))
}
