package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/jackc/pgx/v5"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// boltOpenTimeout bounds the wait for a bbolt file lock: a store the daemon
// still holds fails fast instead of hanging.
const boltOpenTimeout = 2 * time.Second

func leakedBy(oldPubkey nostr.PubKey) func(nostr.Event) bool {
	return func(ev nostr.Event) bool { return ev.PubKey == oldPubkey && isLeakedKind(ev.Kind) }
}

func printLocalEvent(w io.Writer, id nostr.ID, kind nostr.Kind, createdAt nostr.Timestamp, extra string) {
	fmt.Fprintf(w, "  %s kind=%d created_at=%s%s\n", id.Hex(), kind, createdAt.Time().UTC().Format(time.RFC3339), extra)
}

// purgeEventStore removes the leaked identity's status events, and the
// undelivered markers on their coordinates (whose key embeds the d value),
// from the daemon's local event store, then compacts the file so bbolt's
// freed pages do not keep the removed bytes. The daemon must be stopped: the
// store's file lock refuses a second process. Without execute it only lists.
func purgeEventStore(w io.Writer, path string, oldPubkey nostr.PubKey, execute bool) error {
	if err := requireBoltFile(path); err != nil {
		return fmt.Errorf("event store: %w", err)
	}
	store, err := localstore.Open(path)
	if err != nil {
		return fmt.Errorf("event store: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()
	events := slices.Collect(store.QueryEvents(nostr.Filter{Kinds: leakedKinds, Authors: []nostr.PubKey{oldPubkey}}))
	allMarkers, err := store.ListUndelivered()
	if err != nil {
		return fmt.Errorf("event store: %w", err)
	}
	var markers []nostr.Event
	for _, marker := range allMarkers {
		if ev, ok := markerEvent(marker, oldPubkey); ok {
			markers = append(markers, ev)
		}
	}
	fmt.Fprintf(w, "event store %s: %d leaked events, %d undelivered markers\n", path, len(events), len(markers))
	for _, ev := range events {
		printLocalEvent(w, ev.ID, ev.Kind, ev.CreatedAt, "")
	}
	for _, ev := range markers {
		printLocalEvent(w, ev.ID, ev.Kind, ev.CreatedAt, " (undelivered marker)")
	}
	if !execute {
		return nil
	}
	for _, ev := range markers {
		if _, err := store.ClearUndelivered(ev); err != nil {
			return fmt.Errorf("event store: %w", err)
		}
	}
	for _, ev := range events {
		if err := store.DeleteEvent(ev.ID); err != nil {
			return fmt.Errorf("event store: delete %s: %w", ev.ID.Hex(), err)
		}
	}
	closed = true
	if err := store.Close(); err != nil {
		return fmt.Errorf("event store: close: %w", err)
	}
	if err := compactBolt(path); err != nil {
		return fmt.Errorf("event store: %w", err)
	}
	fmt.Fprintf(w, "event store %s: removed %d events and %d markers; compacted\n", path, len(events), len(markers))
	return warnSetAside(w, path)
}

// markerEvent rebuilds the event an undelivered marker names, enough for
// ClearUndelivered to find the marker, when it marks a leaked status event of
// the old identity. The coordinate's d part is used only in memory.
func markerEvent(marker localstore.Undelivered, oldPubkey nostr.PubKey) (nostr.Event, bool) {
	if !isLeakedKind(marker.Kind) {
		return nostr.Event{}, false
	}
	parts := strings.SplitN(marker.Coordinate, ":", 3)
	if len(parts) != 3 || parts[0] != fmt.Sprint(int(marker.Kind)) || parts[1] != oldPubkey.Hex() {
		return nostr.Event{}, false
	}
	return nostr.Event{ID: marker.EventID, Kind: marker.Kind, PubKey: oldPubkey, CreatedAt: marker.CreatedAt, Tags: nostr.Tags{{"d", parts[2]}}}, true
}

// purgeOutbox removes the leaked identity's status events from the daemon's
// publish outbox in every state, then compacts the file. Without execute it
// opens the outbox read-only and only lists.
func purgeOutbox(w io.Writer, path string, oldPubkey nostr.PubKey, execute bool) error {
	if err := requireBoltFile(path); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	var outbox *localstore.Outbox
	if execute {
		outbox, err = localstore.OpenExistingOutboxStrict(abs)
	} else {
		outbox, err = localstore.OpenOutboxReadOnly(abs)
	}
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = outbox.Close()
		}
	}()
	entries, err := outbox.ListEntries(nil, math.MaxInt)
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	match := leakedBy(oldPubkey)
	var leaked []localstore.OutboxEntry
	for _, entry := range entries {
		if match(entry.Event) {
			leaked = append(leaked, entry)
		}
	}
	fmt.Fprintf(w, "outbox %s: %d leaked entries\n", path, len(leaked))
	for _, entry := range leaked {
		printLocalEvent(w, entry.Event.ID, entry.Event.Kind, entry.Event.CreatedAt, " state="+entry.State)
	}
	if !execute {
		return nil
	}
	removed, err := outbox.PurgeEvents(match)
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	closed = true
	if err := outbox.Close(); err != nil {
		return fmt.Errorf("outbox: close: %w", err)
	}
	if err := compactBolt(abs); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	fmt.Fprintf(w, "outbox %s: removed %d entries; compacted\n", path, len(removed))
	return warnSetAside(w, path)
}

// requireBoltFile refuses a missing path, a symlink or a file bbolt cannot
// read. localstore.Open would create a missing store, and would move an
// unreadable one aside and start empty, leaving any leaked copies on disk
// under another name.
func requireBoltFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s must be a nonempty regular file (no symlinks)", path)
	}
	probe, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true, Timeout: boltOpenTimeout})
	if err != nil {
		return fmt.Errorf("open %s read-only (is the daemon still running?): %w", path, err)
	}
	return probe.Close()
}

// compactBolt rewrites path with only its live keys and replaces it, so the
// pages bbolt freed (which still hold the removed bytes) leave the file.
func compactBolt(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	src, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true, Timeout: boltOpenTimeout})
	if err != nil {
		return fmt.Errorf("open %s for compaction: %w", path, err)
	}
	tmp := path + ".bahia-leak-delete-compact"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = src.Close()
		return err
	}
	dst, err := bbolt.Open(tmp, info.Mode().Perm(), &bbolt.Options{Timeout: boltOpenTimeout})
	if err != nil {
		_ = src.Close()
		return fmt.Errorf("create compacted copy of %s: %w", path, err)
	}
	compactErr := bbolt.Compact(dst, src, 64<<20)
	syncErr := dst.Sync()
	closeErr := errors.Join(dst.Close(), src.Close())
	if err := errors.Join(compactErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("compact %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s with its compacted copy: %w", path, err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// warnSetAside lists files localstore moved aside as corrupt next to path:
// they may hold leaked events and are not purged.
func warnSetAside(w io.Writer, path string) error {
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		return err
	}
	for _, match := range matches {
		fmt.Fprintf(w, "WARNING: %s was set aside as corrupt and may hold leaked events; delete it\n", match)
	}
	return nil
}

// pgLeakedRow is one nostr_events row of a leaked status event. Content and
// tags are never read.
type pgLeakedRow struct {
	ID           string
	Kind         int
	CreatedAt    time.Time
	ArchiveBatch string
}

// pgQuerier is the subset of pgx.Tx the PostgreSQL purge uses.
type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func kindInts() []int32 {
	out := make([]int32, 0, len(leakedKinds))
	for _, kind := range leakedKinds {
		out = append(out, int32(kind))
	}
	return out
}

func collectPGRows(rows pgx.Rows) ([]pgLeakedRow, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (pgLeakedRow, error) {
		var out pgLeakedRow
		var batch *string
		if err := row.Scan(&out.ID, &out.Kind, &out.CreatedAt, &batch); err != nil {
			return out, err
		}
		if batch != nil {
			out.ArchiveBatch = *batch
		}
		return out, nil
	})
}

// listPGLeaked lists the leaked identity's status events in nostr_events.
func listPGLeaked(ctx context.Context, q pgQuerier, oldPubkey nostr.PubKey) ([]pgLeakedRow, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, created_at, archive_batch_id::text FROM nostr_events
		WHERE pubkey = $1 AND kind = ANY($2) ORDER BY created_at DESC, id`, oldPubkey.Hex(), kindInts())
	if err != nil {
		return nil, err
	}
	return collectPGRows(rows)
}

// deletePGLeaked deletes them and returns the deleted rows.
func deletePGLeaked(ctx context.Context, q pgQuerier, oldPubkey nostr.PubKey) ([]pgLeakedRow, error) {
	rows, err := q.Query(ctx, `DELETE FROM nostr_events WHERE pubkey = $1 AND kind = ANY($2)
		RETURNING id, kind, created_at, archive_batch_id::text`, oldPubkey.Hex(), kindInts())
	if err != nil {
		return nil, err
	}
	return collectPGRows(rows)
}

func printPGRows(w io.Writer, rows []pgLeakedRow) {
	var batches []string
	for _, row := range rows {
		extra := ""
		if row.ArchiveBatch != "" {
			extra = " archive_batch=" + row.ArchiveBatch
			if !slices.Contains(batches, row.ArchiveBatch) {
				batches = append(batches, row.ArchiveBatch)
			}
		}
		fmt.Fprintf(w, "  %s kind=%d created_at=%s%s\n", row.ID, row.Kind, row.CreatedAt.UTC().Format(time.RFC3339), extra)
	}
	for _, batch := range batches {
		fmt.Fprintf(w, "WARNING: archive batch %s exported copies of leaked events; its archive artifacts are not purged by this tool\n", batch)
	}
}

// purgePostgres removes the leaked identity's status events from the optional
// PostgreSQL nostr_events table named by a Bahia config file's db section.
// Without execute it reads in a read-only transaction and only lists.
func purgePostgres(ctx context.Context, w io.Writer, configPath string, oldPubkey nostr.PubKey, execute bool) error {
	cfg, err := config.LoadReadOnly(configPath)
	if err != nil {
		return fmt.Errorf("postgres: load %s: %w", configPath, err)
	}
	pool, err := db.Connect(ctx, cfg.DB, zap.NewNop())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	options := pgx.TxOptions{AccessMode: pgx.ReadOnly}
	if execute {
		options = pgx.TxOptions{IsoLevel: pgx.Serializable}
	}
	tx, err := pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", cfg.DB.RedactError(err))
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if !execute {
		rows, err := listPGLeaked(ctx, tx, oldPubkey)
		if err != nil {
			return fmt.Errorf("postgres: list nostr_events: %w", cfg.DB.RedactError(err))
		}
		fmt.Fprintf(w, "postgres nostr_events: %d leaked rows\n", len(rows))
		printPGRows(w, rows)
		return nil
	}
	rows, err := deletePGLeaked(ctx, tx, oldPubkey)
	if err != nil {
		return fmt.Errorf("postgres: delete from nostr_events: %w", cfg.DB.RedactError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", cfg.DB.RedactError(err))
	}
	fmt.Fprintf(w, "postgres nostr_events: deleted %d leaked rows\n", len(rows))
	printPGRows(w, rows)
	return nil
}
