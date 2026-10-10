package main

import (
	"bytes"
	"context"
	"os"
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
		require.Contains(t, errors.String(), "signed receipt verification is read-only")
	}
	var output, errors bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{"f74a-compact"}, &output, &errors))
	require.Contains(t, errors.String(), "requires --cutoff")
}

func TestF74aReadOnlyActionsNeverSeedMountedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("dev_mode: true\n")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	t.Setenv("BAHIA_NOSTR__SIDECAR__ENABLED", "true")
	for _, action := range []string{"f74a-census", "f74a-compact", "f74a-restore-preflight"} {
		t.Run(action, func(t *testing.T) {
			var output, errors bytes.Buffer
			require.Equal(t, 1, run(context.Background(), []string{action, "--config", path, "--cutoff", "2026-10-01T00:00:00Z"}, &output, &errors))
			require.Contains(t, errors.String(), "read-only config load refuses mutable-policy bootstrap")
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, original, got)
		})
	}
}

func TestF74aReadOnlyDeadlineValidation(t *testing.T) {
	for _, args := range [][]string{
		{"f74a-census", "--f74a-timeout", "0s"},
		{"f74a-compact", "--cutoff", "2026-10-01T00:00:00Z", "--f74a-timeout", "25h"},
		{"f74a-restore-preflight", "--cutoff", "2026-10-01T00:00:00Z", "--f74a-timeout", "25h"},
	} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "--f74a-timeout must be between 1s and 24h")
	}
	var output, errors bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{"status", "--f74a-timeout", "1h"}, &output, &errors))
	require.Contains(t, errors.String(), "only valid for F74a")
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

func TestF74aRestorePreflightCannotConfirmDeletion(t *testing.T) {
	for _, args := range [][]string{
		{"f74a-restore-preflight", "--confirm", "--cutoff", "2026-10-01T00:00:00Z"},
		{"f74a-restore-preflight"},
	} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Empty(t, output.String())
	}
}

func TestF74aVerifyReceiptRequiresIndependentPinBeforeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("dev_mode: true\n"), 0o600))
	for _, args := range [][]string{
		{"f74a-verify-receipt", "--config", path, "--f74a-receipt", "unused.json"},
		{"--config", path, "--f74a-receipt", "unused.json", "f74a-verify-receipt"},
	} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "backup attestor public key is not configured")
		require.Empty(t, output.String())
	}
	var output, errors bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{"f74a-verify-receipt"}, &output, &errors))
	require.Contains(t, errors.String(), "requires --f74a-receipt")
	output.Reset()
	errors.Reset()
	require.Equal(t, 1, run(context.Background(), []string{"f74a-compact", "--f74a-receipt", "unused.json"}, &output, &errors))
	require.Contains(t, errors.String(), "only valid for f74a-verify-receipt")
}

func TestF74aVerifyReceiptNeverSeedsMountedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("dev_mode: true\n")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	t.Setenv("BAHIA_NOSTR__SIDECAR__ENABLED", "true")
	var output, errors bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{
		"f74a-verify-receipt", "--config", path, "--f74a-receipt", "unused.json",
	}, &output, &errors))
	require.Contains(t, errors.String(), "read-only config load refuses mutable-policy bootstrap")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestF74aVerifyReceiptFileErrorsDoNotEchoPath(t *testing.T) {
	for _, path := range []string{
		filepath.Join(t.TempDir(), "secret-receipt-path-missing"),
		t.TempDir(), // Opening succeeds; reading a directory fails.
	} {
		var output, errors bytes.Buffer
		status := runF74aVerifyReceipt(context.Background(), nil, "configured-pin", path, func(err error) error { return err }, &output, &errors)
		require.Equal(t, 1, status)
		require.Contains(t, errors.String(), "F74a receipt")
		require.NotContains(t, errors.String(), path)
		require.Empty(t, output.String())
	}
}
