package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
)

// This operator action never connects to a relay or signs an event. SQL rows
// with recorded attempts have unknown per-relay acceptance and cannot be
// represented faithfully by a new local outbox entry. Even zero attempts do
// not prove no relay accepted before a process crash; explicit confirmation
// allows replay of the exact signed ID without fabricating acceptance state.
type outboxTransferOptions struct {
	target          string
	apply           bool
	confirmedPath   string
	confirmedRelays string
	maxRows         int
	maxPending      int64
}

type outboxTransferSource interface {
	ListUnpublishedAfter(context.Context, string, *repository.NostrOutboxCursor, int) ([]repository.NostrEventRecord, error)
	TransferUnattemptedToLocalOutbox(context.Context, string, string) (bool, error)
}

func runOutboxTransfer(ctx context.Context, cfg *config.Config, source outboxTransferSource, opts outboxTransferOptions, stdout, stderr io.Writer) int {
	target := repository.NostrPublishTargetDefault
	relays := outboxInteropRelays(cfg)
	if opts.target == "control-plane" {
		target = repository.NostrPublishTargetControlPlane
		relays = f74aControlPlaneRelays(cfg.Nostr)
	}
	if len(relays) == 0 {
		return reportError(stderr, "outbox-transfer needs configured relays for selected target")
	}
	path, err := filepath.Abs(cfg.Nostr.LocalStore.ResolvedOutboxPath())
	if err != nil {
		return reportError(stderr, "resolve outbox path: %v", err)
	}
	if _, err := fmt.Fprintf(stdout, "source_target=%s outbox=%s relays=%s mode=%s\n", opts.target, path, strings.Join(relays, ","), map[bool]string{true: "apply", false: "inventory"}[opts.apply]); err != nil {
		return 1
	}
	if opts.apply {
		if _, err := fmt.Fprintln(stdout, "warning: PostgreSQL has no per-relay OK state; replay keeps the signed ID but cannot preserve unknown prior acceptance"); err != nil {
			return 1
		}
	}
	if opts.apply && opts.confirmedRelays != strings.Join(relays, ",") {
		return reportError(stderr, "confirmed relays do not match configured target relays %s", strings.Join(relays, ","))
	}
	if opts.apply && opts.confirmedPath != path {
		return reportError(stderr, "confirmed outbox path does not match configured path %s", path)
	}
	var outbox *localstore.Outbox
	if opts.apply {
		outbox, err = localstore.OpenOutbox(path)
		if err != nil {
			return reportError(stderr, "open exclusive daemon outbox (stop daemon first): %v", err)
		}
		defer outbox.Close()
		if moved := outbox.MovedAside(); moved != "" {
			return reportError(stderr, "outbox corruption moved %s aside; recover it before transfer", moved)
		}
	}
	stats, err := transferOutboxRows(ctx, source, outbox, target, opts, stdout)
	if err != nil {
		return reportError(stderr, "outbox-transfer: %v", err)
	}
	if _, err := fmt.Fprintf(stdout, "inspected=%d replay_candidates=%d transferred=%d conflicts=%d remaining_after_cursor=%t\n", stats.inspected, stats.replayCandidates, stats.transferred, stats.conflicts, stats.more); err != nil {
		return 1
	}
	if stats.conflicts != 0 {
		return reportError(stderr, "%d conflicting SQL rows require operator reconciliation; no attempted row was transferred", stats.conflicts)
	}
	return 0
}

type transferStats struct {
	inspected, replayCandidates, transferred, conflicts int
	more                                                bool
}

func transferOutboxRows(ctx context.Context, source outboxTransferSource, outbox *localstore.Outbox, target string, opts outboxTransferOptions, output io.Writer) (transferStats, error) {
	var stats transferStats
	var cursor *repository.NostrOutboxCursor
	if outbox != nil {
		saved, err := outbox.LoadTransferCursor(target)
		if err != nil {
			return stats, err
		}
		if saved != nil {
			cursor = &repository.NostrOutboxCursor{ReceivedAt: saved.ReceivedAt, ID: saved.ID}
		}
	}
	const pageSize = 100
	for stats.inspected < opts.maxRows {
		limit := pageSize
		if left := opts.maxRows - stats.inspected; left < limit {
			limit = left
		}
		rows, err := source.ListUnpublishedAfter(ctx, target, cursor, limit)
		if err != nil {
			return stats, err
		}
		if len(rows) == 0 {
			if outbox != nil {
				// At end, wrap on the next run so previously reported conflicts and
				// rows inserted after the saved cursor remain visible.
				if err := outbox.SaveTransferCursor(target, nil); err != nil {
					return stats, err
				}
			}
			return stats, nil
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			stats.inspected++
			reason := ""
			ev, err := nostradapter.SignedEventFromRecord(row)
			if err != nil {
				reason = err.Error()
			}
			if row.PublishAttempts != 0 || row.LastPublishError != "" {
				reason = "SQL relay-attempt state cannot be preserved"
			}
			if row.PublishTarget != target || row.PublishState != repository.NostrPublishStatePending {
				reason = "source state or target changed"
			}
			if reason == "" {
				stats.replayCandidates++
			}
			if reason == "" && outbox != nil {
				counts, err := outbox.Counts()
				if err != nil {
					return stats, err
				}
				existing, found, err := outbox.Get(ev.ID)
				if err != nil {
					return stats, err
				}
				if found {
					if existing.Target != target || !reflect.DeepEqual(existing.Event, ev) || existing.State != localstore.OutboxPending {
						reason = "local event ID conflicts with source or is already settled"
					}
				} else if counts.Pending >= opts.maxPending {
					return stats, fmt.Errorf("local pending admission cap %d reached before event %s", opts.maxPending, row.ID)
				} else {
					entry := localstore.OutboxEntry{Event: ev, Target: target, EntityType: row.EntityType, EnqueuedAt: row.ReceivedAt}
					if row.EntityID != nil {
						entry.EntityID = row.EntityID.String()
					}
					if _, err := outbox.Enqueue(entry); err != nil {
						return stats, err
					}
				}
				if reason == "" {
					changed, err := source.TransferUnattemptedToLocalOutbox(ctx, row.ID, target)
					if err != nil {
						return stats, err
					}
					if !changed {
						return stats, fmt.Errorf("source row %s changed after local admission; reconcile before retry", row.ID)
					}
					stats.transferred++
				}
			}
			if reason != "" {
				stats.conflicts++
				if _, err := fmt.Fprintf(output, "conflict event_id=%s reason=%s\n", row.ID, reason); err != nil {
					return stats, err
				}
			}
			cursor = &repository.NostrOutboxCursor{ReceivedAt: row.ReceivedAt, ID: row.ID}
			if outbox != nil {
				if err := outbox.SaveTransferCursor(target, &localstore.TransferCursor{ReceivedAt: row.ReceivedAt, ID: row.ID}); err != nil {
					return stats, err
				}
			}
		}
		if len(rows) < limit {
			if outbox != nil {
				if err := outbox.SaveTransferCursor(target, nil); err != nil {
					return stats, err
				}
			}
			return stats, nil
		}
	}
	stats.more = true
	return stats, nil
}

// outboxInteropRelays mirrors the daemon's default-target pool policy. The
// confirmation must describe the pool that will drain the transferred entry.
func outboxInteropRelays(cfg *config.Config) []string {
	var relays []string
	add := func(url string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		for _, existing := range relays {
			if existing == url {
				return
			}
		}
		relays = append(relays, url)
	}
	if cfg.Nostr.Sidecar.Enabled {
		for _, url := range f74aControlPlaneRelays(cfg.Nostr) {
			add(url)
		}
	}
	if !cfg.Nostr.Sidecar.Enabled || !cfg.Nostr.Sidecar.MirrorExternal {
		for _, url := range cfg.Nostr.Relays {
			add(url)
		}
	}
	for _, url := range cfg.Loom.Relays {
		add(url)
	}
	return relays
}
