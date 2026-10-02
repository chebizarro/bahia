package main

import (
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/spf13/cobra"
)

func workersCleanupOrphansCommand() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "cleanup-orphans",
		Short: "Delete orphaned worker records with bare-pubkey d tags",
		Long: `Find and delete 30900 worker cp-state records whose d tag is a bare pubkey
(pre-irsry.36 format) rather than the canonical "worker:<entity>:<id>" format.

By default runs in dry-run mode and prints what would be deleted. Pass --apply
to publish NIP-09 kind-5 deletion events.

The command must be run with the service identity signer (the same key that
authored the original records). NIP-09 deletions signed by a different key
are ignored by relays.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorkersCleanupOrphans(cmd, !apply)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "publish deletions (default is dry-run)")
	return cmd
}

// resolveCleanupSigner resolves a nostr.Signer from CLI flags/env using the
// same inputs as the operator commands (--nostr-key-file / --nostr-bunker-file).
func resolveCleanupSigner(cmd *cobra.Command) (canonicalnostr.Signer, func() error, error) {
	key, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve signer: %w", err)
	}
	bunkerURI, clientKey, err := resolveNIP46OperatorInput(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve NIP-46 signer: %w", err)
	}
	if strings.TrimSpace(key) != "" && bunkerURI != "" {
		return nil, nil, fmt.Errorf("configure either a NIP-46 bunker signer or a local private key, not both")
	}
	if strings.TrimSpace(key) == "" && bunkerURI == "" {
		return nil, nil, fmt.Errorf("a signer is required: provide --nostr-key-file (or BAHIA_NOSTR_KEY_FILE) or --nostr-bunker-file (or BAHIA_NOSTR_BUNKER_FILE)")
	}
	if bunkerURI != "" {
		signer, _, closeSigner, signerErr := newCLINIP46Signer(cmd.Context(), bunkerURI, clientKey)
		if signerErr != nil {
			return nil, nil, fmt.Errorf("connect NIP-46 signer: %w", signerErr)
		}
		return signer, closeSigner, nil
	}
	signer, err := controlplane.NewPrivateKeySigner(key)
	if err != nil {
		return nil, nil, fmt.Errorf("create signer: %w", err)
	}
	if signer == nil {
		return nil, nil, fmt.Errorf("empty private key")
	}
	return signer, nil, nil
}

// runWorkersCleanupOrphans is the testable core of the cleanup-orphans command.
var runWorkersCleanupOrphans = func(cmd *cobra.Command, dryRun bool) error {
	signer, closeSigner, err := resolveCleanupSigner(cmd)
	if err != nil {
		return err
	}
	if closeSigner != nil {
		defer closeSigner() //nolint:errcheck
	}

	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return fmt.Errorf("resolve relays: %w", err)
	}
	if len(relays) == 0 {
		return fmt.Errorf("at least one --relay is required")
	}

	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	if dryRun {
		fmt.Fprintln(out, "DRY RUN: no deletions will be published")
	}

	for _, relayURL := range relays {
		relay, err := canonicalnostr.RelayConnect(ctx, relayURL, canonicalnostr.RelayOptions{})
		if err != nil {
			fmt.Fprintf(out, "WARN: could not connect to %s: %v\n", relayURL, err)
			continue
		}

		results, err := controlplane.CleanupOrphanedWorkerRecords(ctx, relay, signer, dryRun)
		relay.Close()
		if err != nil {
			return fmt.Errorf("cleanup on %s: %w", relayURL, err)
		}

		if len(results) == 0 {
			fmt.Fprintf(out, "%s: no orphaned worker records found\n", relayURL)
			continue
		}

		for _, r := range results {
			if dryRun {
				fmt.Fprintf(out, "%s: would delete d=%s (event %s)\n", relayURL, r.DTag, r.EventID)
			} else {
				fmt.Fprintf(out, "%s: deleted d=%s (event %s, deletion %s)\n", relayURL, r.DTag, r.EventID, r.DeletionID)
			}
		}
		fmt.Fprintf(out, "%s: %d orphaned record(s) %s\n", relayURL, len(results), map[bool]string{true: "found", false: "deleted"}[dryRun])
	}
	return nil
}
