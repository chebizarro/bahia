package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/spf13/cobra"
)

var outboxPath string

func outboxCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outbox",
		Short: "Inspect the local Nostr publish outbox (read-only admin view)",
	}
	cmd.PersistentFlags().StringVar(&outboxPath, "outbox-path", defaultOutboxPath(), "Path to the outbox bolt file")
	cmd.AddCommand(outboxFailedCommand(), outboxCountsCommand())
	return cmd
}

func defaultOutboxPath() string {
	return filepath.Join(".", "data", "nostr-cache", "outbox.bolt")
}

func outboxFailedCommand() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "failed",
		Short: "List events that could not be delivered",
		RunE: func(cmd *cobra.Command, args []string) error {
			outbox, err := localstore.OpenOutbox(outboxPath)
			if err != nil {
				return fmt.Errorf("open outbox at %s: %w", outboxPath, err)
			}
			defer outbox.Close()

			entries, err := outbox.ListFailed(limit)
			if err != nil {
				return err
			}

			if outputFormat == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No failed outbox entries.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "EVENT ID\tKIND\tTARGET\tROUNDS\tSETTLED\tERROR")
			for _, e := range entries {
				settled := "-"
				if !e.SettledAt.IsZero() {
					settled = e.SettledAt.UTC().Format(time.RFC3339)
				}
				lastErr := e.LastError
				if len(lastErr) > 80 {
					lastErr = lastErr[:77] + "..."
				}
				relayDetails := formatRelayErrors(e.Relays)
				errDisplay := lastErr
				if relayDetails != "" && errDisplay == "" {
					errDisplay = relayDetails
				} else if relayDetails != "" {
					errDisplay = lastErr + " | " + relayDetails
				}
				fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\t%s\n",
					e.Event.ID.Hex(),
					int(e.Event.Kind),
					e.Target,
					e.Rounds,
					settled,
					errDisplay,
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum entries to show")
	return cmd
}

func outboxCountsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "counts",
		Short: "Show pending and failed entry counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			outbox, err := localstore.OpenOutbox(outboxPath)
			if err != nil {
				return fmt.Errorf("open outbox at %s: %w", outboxPath, err)
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

func formatRelayErrors(relays map[string]localstore.RelayDelivery) string {
	if len(relays) == 0 {
		return ""
	}
	var parts []string
	for url, d := range relays {
		// Shorten relay URL for display.
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
