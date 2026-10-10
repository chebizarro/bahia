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

func TestF74aCompactIsReadOnlyEvenWithConfirm(t *testing.T) {
	for _, args := range [][]string{
		{"f74a-compact", "--confirm"},
		{"f74a-compact", "--cutoff", "2026-10-01T00:00:00Z", "--confirm"},
	} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "no trusted same-PostgreSQL backup and restore receipt contract")
	}
	var output, errors bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{"f74a-compact"}, &output, &errors))
	require.Contains(t, errors.String(), "requires --cutoff")
}

func TestF74aImportRequiresExplicitQuiescenceInEitherArgumentOrder(t *testing.T) {
	for _, args := range [][]string{{"f74a-import"}, {"--config", "missing.yaml", "f74a-import"}} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "requires --confirm-quiesced")
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, args := range [][]string{{"f74a-import", "--confirm-quiesced", "--config", missing}, {"--config", missing, "--confirm-quiesced", "f74a-import"}} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.NotContains(t, errors.String(), "unknown migration action")
		require.Contains(t, errors.String(), "loading Bahia config")
	}
}

func TestF74aImportRelaysFollowControlPlanePolicy(t *testing.T) {
	cfg := config.NostrConfig{Relays: []string{"wss://interop.example"}, ContextVMRelays: []string{"wss://cp.example"}}
	require.Equal(t, []string{"wss://cp.example"}, f74aControlPlaneRelays(cfg))
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.BackendURL = "ws://sidecar.internal"
	require.Equal(t, []string{"ws://sidecar.internal"}, f74aControlPlaneRelays(cfg))
}
