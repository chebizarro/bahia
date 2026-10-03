package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/spf13/cobra"
)

var (
	outboxPath   string
	daemonOutbox bool
)

func outboxCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outbox",
		Short: "Inspect and manage the local Nostr publish outbox",
	}
	cmd.PersistentFlags().StringVar(&outboxPath, "outbox-path", "", "Path to the outbox bolt file (overrides default)")
	cmd.PersistentFlags().BoolVar(&daemonOutbox, "daemon", false, "Inspect the daemon's outbox (read-only)")
	cmd.AddCommand(
		outboxListCommand(),
		outboxCountsCommand(),
		outboxRetryCommand(),
		outboxPruneCommand(),
	)
	return cmd
}

// cliOutboxDefaultPath returns the default CLI-local outbox path.
// N2 (IntentPublisher) will enqueue into this file.
func cliOutboxDefaultPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "bahia", "outbox.bolt")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "bahia-outbox.bolt")
	}
	return filepath.Join(home, ".local", "share", "bahia", "outbox.bolt")
}

// daemonOutboxDefaultPath returns the default path for the daemon's outbox.
func daemonOutboxDefaultPath() string {
	if dir := os.Getenv("BAHIA_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "nostr-cache", "outbox.bolt")
	}
	return filepath.Join(".", "data", "nostr-cache", "outbox.bolt")
}

// resolveOutboxPath returns the effective outbox path, respecting --outbox-path
// and --daemon flags.
func resolveOutboxPath() string {
	if outboxPath != "" {
		return outboxPath
	}
	if daemonOutbox {
		return daemonOutboxDefaultPath()
	}
	return cliOutboxDefaultPath()
}

// openOutboxForCommand opens the outbox, choosing read-only mode when --daemon
// is set. For the CLI-local outbox, it creates the file and directories on
// first use so that `outbox list` and `outbox counts` work before N2 enqueues
// anything.
func openOutboxForCommand() (*localstore.Outbox, error) {
	path := resolveOutboxPath()
	if daemonOutbox {
		return localstore.OpenOutboxReadOnly(path)
	}
	return localstore.OpenOutbox(path)
}

func outboxListCommand() *cobra.Command {
	var (
		limit    int
		stateStr string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List outbox entries (pending and failed by default)",
		Long: `List outbox entries with optional state filtering.

States: pending, failed, published.
Default shows pending and failed entries (the most actionable).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			outbox, err := openOutboxForCommand()
			if err != nil {
				return fmt.Errorf("open outbox at %s: %w", resolveOutboxPath(), err)
			}
			defer outbox.Close()

			states := parseStateFilter(stateStr)
			entries, err := outbox.ListEntries(states, limit)
			if err != nil {
				return err
			}

			if outputFormat == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching outbox entries.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "EVENT ID\tSTATE\tKIND\tTARGET\tROUNDS\tENQUEUED\tERROR")
			for _, e := range entries {
				enqueued := e.EnqueuedAt.UTC().Format(time.RFC3339)
				errDisplay := formatEntryError(e)
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\t%s\n",
					e.Event.ID.Hex(),
					e.State,
					int(e.Event.Kind),
					e.Target,
					e.Rounds,
					enqueued,
					errDisplay,
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum entries to show")
	cmd.Flags().StringVar(&stateStr, "state", "", "Filter by state: pending, failed, published, all (default: pending+failed)")
	return cmd
}

// parseStateFilter converts a state flag string into a list of states for
// ListEntries. An empty string defaults to pending+failed.
func parseStateFilter(s string) []string {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return []string{localstore.OutboxPending, localstore.OutboxFailed}
	case "all":
		return nil // nil = no filter, all states
	case "pending":
		return []string{localstore.OutboxPending}
	case "failed":
		return []string{localstore.OutboxFailed}
	case "published":
		return []string{localstore.OutboxPublished}
	default:
		return []string{s}
	}
}

func outboxCountsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "counts",
		Short: "Show pending and failed entry counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			outbox, err := openOutboxForCommand()
			if err != nil {
				return fmt.Errorf("open outbox at %s: %w", resolveOutboxPath(), err)
			}
			defer outbox.Close()

			counts, err := outbox.Counts()
			if err != nil {
				return err
			}

			if outputFormat == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(counts)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Pending: %d\nFailed:  %d\n", counts.Pending, counts.Failed)
			return nil
		},
	}
}

func outboxRetryCommand() *cobra.Command {
	var retryAll bool
	cmd := &cobra.Command{
		Use:   "retry [event-id]",
		Short: "Re-enqueue a failed entry (or --all) for retry",
		Long: `Reset a failed outbox entry back to pending so the outbox worker retries delivery.

Provide an event ID to retry one entry, or use --all to retry every failed entry.
Not available with --daemon (the daemon's outbox is read-only from the CLI).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if daemonOutbox {
				return fmt.Errorf("cannot retry entries in daemon outbox (read-only); use the daemon to manage its own outbox")
			}
			if !retryAll && len(args) == 0 {
				return fmt.Errorf("provide an event ID or use --all")
			}
			if retryAll && len(args) > 0 {
				return fmt.Errorf("--all and an event ID are mutually exclusive")
			}

			outbox, err := openOutboxForCommand()
			if err != nil {
				return fmt.Errorf("open outbox at %s: %w", resolveOutboxPath(), err)
			}
			defer outbox.Close()

			if retryAll {
				count, err := outbox.RetryAllFailed()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Retried %d failed entries.\n", count)
				return nil
			}

			id, err := parseEventID(args[0])
			if err != nil {
				return err
			}
			entry, err := outbox.Retry(id)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Retried entry %s (kind %d, target %q) → pending.\n",
				entry.Event.ID.Hex(), int(entry.Event.Kind), entry.Target)
			return nil
		},
	}
	cmd.Flags().BoolVar(&retryAll, "all", false, "Retry all failed entries")
	return cmd
}

func outboxPruneCommand() *cobra.Command {
	var (
		confirm bool
		maxAge  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old settled outbox entries (dry-run by default)",
		Long: `Prune settled (published and failed) outbox entries older than --max-age.

By default this is a dry run: it shows how many entries would be removed.
Pass --confirm to actually delete them.
Not available with --daemon (the daemon's outbox is read-only from the CLI).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if daemonOutbox {
				return fmt.Errorf("cannot prune daemon outbox (read-only); use the daemon to manage its own outbox")
			}

			outbox, err := openOutboxForCommand()
			if err != nil {
				return fmt.Errorf("open outbox at %s: %w", resolveOutboxPath(), err)
			}
			defer outbox.Close()

			cutoff := time.Now().Add(-maxAge)

			if !confirm {
				// Dry run: count what would be removed by listing settled
				// entries older than the cutoff.
				published, err := outbox.ListEntries([]string{localstore.OutboxPublished}, 0)
				if err != nil {
					return err
				}
				failed, err := outbox.ListEntries([]string{localstore.OutboxFailed}, 0)
				if err != nil {
					return err
				}
				pubCount, failCount := 0, 0
				for _, e := range published {
					if !e.SettledAt.IsZero() && e.SettledAt.Before(cutoff) {
						pubCount++
					}
				}
				for _, e := range failed {
					if !e.SettledAt.IsZero() && e.SettledAt.Before(cutoff) {
						failCount++
					}
				}
				total := pubCount + failCount
				fmt.Fprintf(cmd.OutOrStdout(), "Dry run: would prune %d entries (%d published, %d failed) settled before %s.\n",
					total, pubCount, failCount, cutoff.UTC().Format(time.RFC3339))
				if total > 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "Pass --confirm to execute.")
				}
				return nil
			}

			removed, err := outbox.Prune(cutoff, cutoff)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pruned %d settled entries older than %s.\n", removed, maxAge)
			return nil
		},
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Actually prune (without this flag, only a dry run is performed)")
	cmd.Flags().DurationVar(&maxAge, "max-age", 7*24*time.Hour, "Prune entries settled longer ago than this duration")
	return cmd
}

// parseEventID decodes a hex event ID string into a nostr.ID.
func parseEventID(s string) (nostr.ID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nostr.ZeroID, fmt.Errorf("invalid event ID %q: must be 64 hex characters", s)
	}
	var id nostr.ID
	copy(id[:], b)
	return id, nil
}

// formatEntryError builds a compact error display from an entry's LastError
// and per-relay state.
func formatEntryError(e localstore.OutboxEntry) string {
	relayDetails := formatRelayErrors(e.Relays)
	lastErr := e.LastError
	if len(lastErr) > 80 {
		lastErr = lastErr[:77] + "..."
	}
	switch {
	case lastErr != "" && relayDetails != "":
		return lastErr + " | " + relayDetails
	case lastErr != "":
		return lastErr
	default:
		return relayDetails
	}
}

func formatRelayErrors(relays map[string]localstore.RelayDelivery) string {
	if len(relays) == 0 {
		return ""
	}
	var parts []string
	for url, d := range relays {
		short := url
		if strings.HasPrefix(short, "wss://") {
			short = short[6:]
		}
		if d.Rejected != "" {
			parts = append(parts, short+": rejected: "+d.Rejected)
		} else if d.LastError != "" {
			parts = append(parts, short+": "+d.LastError)
		}
	}
	result := strings.Join(parts, "; ")
	if len(result) > 120 {
		result = result[:117] + "..."
	}
	return result
}
