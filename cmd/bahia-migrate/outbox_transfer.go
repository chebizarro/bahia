package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
)

// outbox-transfer is deliberately read-only. PostgreSQL cannot prove prior
// per-relay acceptance, and a SQL-to-bbolt handoff cannot be made atomic by
// enqueueing a drainable entry before a conditional SQL update.
type outboxTransferOptions struct {
	target  string
	after   string
	maxRows int
}

type outboxInventorySource interface {
	ListUnpublishedAfter(context.Context, string, *repository.NostrOutboxCursor, int) ([]repository.NostrEventRecord, error)
}

type outboxInventoryCursor struct {
	Target     string    `json:"target"`
	ReceivedAt time.Time `json:"received_at"`
	ID         string    `json:"id"`
	Conflicts  int       `json:"conflicts"`
}

func decodeOutboxInventoryCursor(token, target string) (*outboxInventoryCursor, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) > 2048 {
		return nil, fmt.Errorf("invalid --after token: too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid --after token: %w", err)
	}
	var cursor outboxInventoryCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("invalid --after token: %w", err)
	}
	if cursor.Target != target || cursor.ReceivedAt.IsZero() || cursor.ID == "" || cursor.Conflicts < 0 {
		return nil, fmt.Errorf("--after token does not match a valid %q inventory cursor", target)
	}
	return &cursor, nil
}
func encodeOutboxInventoryCursor(cursor outboxInventoryCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode inventory cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func runOutboxTransfer(ctx context.Context, source outboxInventorySource, opts outboxTransferOptions, stdout, stderr io.Writer) int {
	target := repository.NostrPublishTargetDefault
	if opts.target == "control-plane" {
		target = repository.NostrPublishTargetControlPlane
	}
	cursor, err := decodeOutboxInventoryCursor(opts.after, target)
	if err != nil {
		return reportError(stderr, "outbox-transfer: %v", err)
	}
	stats, err := inventoryOutboxRows(ctx, source, target, cursor, opts.maxRows, stdout)
	if err != nil {
		return reportError(stderr, "outbox-transfer inventory: %v", err)
	}
	if _, err := fmt.Fprintf(stdout, "window_inspected=%d window_conflicts=%d cumulative_conflicts=%d signed_unattempted=%d next_after=%s\n", stats.inspected, stats.conflicts, stats.cumulativeConflicts, stats.signedUnattempted, stats.nextAfter); err != nil {
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "read-only inventory: no row was enqueued, re-targeted, or published; no page proves prior relay acceptance"); err != nil {
		return 1
	}
	if stats.cumulativeConflicts > 0 {
		return reportError(stderr, "outbox-transfer inventory includes %d conflicts across supplied cursor chain", stats.cumulativeConflicts)
	}
	return 0
}

type outboxInventoryStats struct {
	inspected, conflicts, cumulativeConflicts, signedUnattempted int
	nextAfter                                                    string
}

func inventoryOutboxRows(ctx context.Context, source outboxInventorySource, target string, after *outboxInventoryCursor, maxRows int, output io.Writer) (outboxInventoryStats, error) {
	var stats outboxInventoryStats
	var cursor *repository.NostrOutboxCursor
	if after != nil {
		cursor = &repository.NostrOutboxCursor{ReceivedAt: after.ReceivedAt, ID: after.ID}
		stats.cumulativeConflicts = after.Conflicts
	}
	const pageSize = 100
	for stats.inspected < maxRows {
		limit := pageSize
		if remaining := maxRows - stats.inspected; remaining < limit {
			limit = remaining
		}
		rows, err := source.ListUnpublishedAfter(ctx, target, cursor, limit)
		if err != nil {
			return stats, err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			stats.inspected++
			reason := ""
			if _, err := nostradapter.SignedEventFromRecord(row); err != nil {
				reason = err.Error()
			}
			if row.PublishAttempts != 0 || row.LastPublishError != "" {
				reason = "SQL relay-attempt state cannot be preserved"
			}
			if row.PublishTarget != target || row.PublishState != repository.NostrPublishStatePending {
				reason = "source state or target changed"
			}
			if reason != "" {
				stats.conflicts++
				stats.cumulativeConflicts++
				if _, err := fmt.Fprintf(output, "conflict event_id=%s reason=%s\n", row.ID, reason); err != nil {
					return stats, err
				}
			} else {
				stats.signedUnattempted++
			}
			cursor = &repository.NostrOutboxCursor{ReceivedAt: row.ReceivedAt, ID: row.ID}
		}
		if len(rows) < limit {
			return stats, nil
		}
	}
	if cursor != nil {
		token, err := encodeOutboxInventoryCursor(outboxInventoryCursor{Target: target, ReceivedAt: cursor.ReceivedAt, ID: cursor.ID, Conflicts: stats.cumulativeConflicts})
		if err != nil {
			return stats, err
		}
		stats.nextAfter = token
	}
	return stats, nil
}
