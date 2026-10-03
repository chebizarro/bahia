package main

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/spf13/cobra"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
)

type cleanupCLITestRelay struct {
	events    []nostr.Event
	published []nostr.Event
}

func (r *cleanupCLITestRelay) QueryEvents(_ nostr.Filter) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		for _, ev := range r.events {
			if !yield(ev) {
				return
			}
		}
	}
}

func (r *cleanupCLITestRelay) Publish(_ context.Context, event nostr.Event) error {
	r.published = append(r.published, event)
	return nil
}

func TestWorkersCleanupOrphansCommandRegistered(t *testing.T) {
	resetOperatorGlobals(t)
	root := newRootCommand()
	cmd, _, err := root.Find([]string{"workers", "cleanup-orphans"})
	if err != nil || cmd == nil || cmd.Name() != "cleanup-orphans" {
		t.Fatalf("workers cleanup-orphans not found: %v", err)
	}
}

func TestWorkersCleanupOrphansDryRunViaEntryPoint(t *testing.T) {
	resetOperatorGlobals(t)

	sk := nostr.Generate()
	signer := keyer.NewPlainKeySigner(sk)
	pk, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	orphanedPubkey := "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	relay := &cleanupCLITestRelay{events: []nostr.Event{{
		ID: nostr.ID{0x01}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: pk, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", orphanedPubkey},
			{"domain", kinds.WorkerDomain},
			{"legacy_kind", "32000"},
		},
	}}}

	// Override the entry point to inject the test relay and signer.
	original := runWorkersCleanupOrphans
	t.Cleanup(func() { runWorkersCleanupOrphans = original })
	runWorkersCleanupOrphans = func(cmd *cobra.Command, dryRun bool) error {
		if !dryRun {
			t.Error("expected dry-run by default")
		}
		results, err := controlplane.CleanupOrphanedWorkerRecords(cmd.Context(), relay, signer, dryRun)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, r := range results {
			if dryRun {
				fmt.Fprintf(out, "would delete d=%s (event %s)\n", r.DTag, r.EventID)
			} else {
				fmt.Fprintf(out, "deleted d=%s (event %s, deletion %s)\n", r.DTag, r.EventID, r.DeletionID)
			}
		}
		return nil
	}

	root := newRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"workers", "cleanup-orphans"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	if !strings.Contains(output, "would delete") {
		t.Errorf("expected dry-run output, got: %s", output)
	}
	if !strings.Contains(output, orphanedPubkey) {
		t.Errorf("expected orphaned pubkey in output, got: %s", output)
	}
}

func TestWorkersCleanupOrphansApplyFlagViaEntryPoint(t *testing.T) {
	resetOperatorGlobals(t)

	sk := nostr.Generate()
	signer := keyer.NewPlainKeySigner(sk)
	pk, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	orphanedPubkey := "aabbccdd0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	relay := &cleanupCLITestRelay{events: []nostr.Event{{
		ID: nostr.ID{0x04}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: pk, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", orphanedPubkey},
			{"domain", kinds.WorkerDomain},
			{"legacy_kind", "32002"},
		},
	}}}

	original := runWorkersCleanupOrphans
	t.Cleanup(func() { runWorkersCleanupOrphans = original })
	runWorkersCleanupOrphans = func(cmd *cobra.Command, dryRun bool) error {
		if dryRun {
			t.Error("expected --apply to disable dry-run")
		}
		results, err := controlplane.CleanupOrphanedWorkerRecords(cmd.Context(), relay, signer, dryRun)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, r := range results {
			fmt.Fprintf(out, "deleted d=%s (deletion %s)\n", r.DTag, r.DeletionID)
		}
		return nil
	}

	root := newRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"workers", "cleanup-orphans", "--apply"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	if !strings.Contains(output, "deleted") {
		t.Errorf("expected deletion output, got: %s", output)
	}
	if len(relay.published) != 1 {
		t.Errorf("expected 1 published deletion, got %d", len(relay.published))
	}
}

func TestWorkersCleanupOrphansRequiresSigner(t *testing.T) {
	resetOperatorGlobals(t)

	// No key file or bunker configured — should error.
	root := newRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"workers", "cleanup-orphans"})

	// Clear any env vars that might provide keys.
	for _, env := range []string{"BAHIA_NOSTR_KEY_FILE", "BAHIA_NOSTR_NSEC", "BAHIA_NOSTR_PRIVATE_KEY", "BAHIA_NOSTR_BUNKER_FILE", "BAHIA_NOSTR_BUNKER_URI"} {
		if v := os.Getenv(env); v != "" {
			t.Setenv(env, "")
		}
	}

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when no signer is configured")
	}
	if !strings.Contains(err.Error(), "signer") {
		t.Errorf("expected signer error, got: %v", err)
	}
}

func TestWorkersCleanupOrphansWithKeyFile(t *testing.T) {
	resetOperatorGlobals(t)

	sk := nostr.Generate()
	signer := keyer.NewPlainKeySigner(sk)

	// Write key to temp file.
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(sk.Hex()), 0o600); err != nil {
		t.Fatal(err)
	}

	relay := &cleanupCLITestRelay{events: []nostr.Event{}}

	original := runWorkersCleanupOrphans
	t.Cleanup(func() { runWorkersCleanupOrphans = original })
	runWorkersCleanupOrphans = func(cmd *cobra.Command, dryRun bool) error {
		resolvedSigner, closeSigner, err := resolveCleanupSigner(cmd)
		if err != nil {
			return err
		}
		if closeSigner != nil {
			defer closeSigner() //nolint:errcheck
		}
		_ = signer // suppress unused warning
		results, err := controlplane.CleanupOrphanedWorkerRecords(cmd.Context(), relay, resolvedSigner, dryRun)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "found %d orphans\n", len(results))
		return nil
	}

	root := newRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	// Clear env to not interfere.
	for _, env := range []string{"BAHIA_NOSTR_KEY_FILE", "BAHIA_NOSTR_NSEC", "BAHIA_NOSTR_PRIVATE_KEY", "BAHIA_NOSTR_BUNKER_FILE", "BAHIA_NOSTR_BUNKER_URI"} {
		t.Setenv(env, "")
	}

	root.SetArgs([]string{"workers", "cleanup-orphans", "--nostr-key-file", keyFile})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("cleanup with key file failed: %v", err)
	}
	if !strings.Contains(buf.String(), "found 0 orphans") {
		t.Errorf("expected 'found 0 orphans', got: %s", buf.String())
	}
}
