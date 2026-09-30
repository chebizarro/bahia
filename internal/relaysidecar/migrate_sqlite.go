package relaysidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"go.etcd.io/bbolt"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

// legacySQLiteFile is the pre-eventstore store: one SQLite table of event JSON.
const legacySQLiteFile = "events.sqlite"

// sqliteMigrationKey marks, in sidecarMetaBucket, that events.sqlite was
// imported. Its value is the JSON sqliteMigrationResult.
var sqliteMigrationKey = []byte("sqlite_migration_v1")

// sqliteMigrationResult reports one import of events.sqlite.
type sqliteMigrationResult struct {
	Source          string    `json:"source"`
	CompletedAt     time.Time `json:"completed_at"`
	Rows            int       `json:"rows"`
	Imported        int       `json:"imported"`
	AlreadyPresent  int       `json:"already_present"`
	SkippedInvalid  int       `json:"skipped_invalid"`
	SkippedOversize int       `json:"skipped_oversize"`
	Skipped         bool      `json:"-"` // nothing to do: no source, or already migrated
}

// migrateLegacySQLite imports every event from dataDir/events.sqlite into the
// eventstore, once. It runs on the first open of the data_dir, before the
// relay serves anything. Regular events are saved and replaceable/addressable
// ones go through latest-wins replacement, so a rerun, including one after a
// crash mid-import, converges on the same set. The source file is left in
// place as a rollback copy; completion is recorded in the store so later
// starts skip the scan. Events are copied as stored: they were verified on
// admission, and ids are rechecked here.
func migrateLegacySQLite(ctx context.Context, store *eventStore, dataDir string, logger *zap.Logger) (sqliteMigrationResult, error) {
	source := filepath.Join(dataDir, legacySQLiteFile)
	result := sqliteMigrationResult{Source: source, Skipped: true}
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("stat legacy relay sidecar store %s: %w", source, err)
	}
	backend := store.backend()
	var done bool
	if err := backend.DB.View(func(tx *bbolt.Tx) error {
		done = tx.Bucket(sidecarMetaBucket).Get(sqliteMigrationKey) != nil
		return nil
	}); err != nil {
		return result, fmt.Errorf("read relay sidecar migration state: %w", err)
	}
	if done {
		return result, nil
	}
	result.Skipped = false
	logger.Info("relay sidecar importing legacy SQLite event store", zap.String("source", source))

	db, err := sql.Open("sqlite", source+"?_pragma=busy_timeout%3d30000")
	if err != nil {
		return result, fmt.Errorf("open legacy relay sidecar store %s: %w", source, err)
	}
	defer func() { _ = db.Close() }()
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'`).Scan(&tables); err != nil {
		return result, fmt.Errorf("inspect legacy relay sidecar store %s: %w", source, err)
	}

	// One fsync per event would make a large import take hours. Sync once at
	// the end instead; the completion marker is written only after that, so a
	// crash before it just reruns the idempotent import.
	backend.DB.NoSync = true
	defer func() { backend.DB.NoSync = false }()
	if tables > 0 {
		if err := importSQLiteRows(ctx, db, store, &result); err != nil {
			return result, err
		}
	}
	if err := backend.DB.Sync(); err != nil {
		return result, fmt.Errorf("sync imported relay events: %w", err)
	}
	backend.DB.NoSync = false

	result.CompletedAt = time.Now().UTC()
	marker, err := json.Marshal(result)
	if err != nil {
		return result, fmt.Errorf("encode relay sidecar migration state: %w", err)
	}
	if err := backend.DB.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(sidecarMetaBucket).Put(sqliteMigrationKey, marker)
	}); err != nil {
		return result, fmt.Errorf("record relay sidecar migration state: %w", err)
	}
	fields := []zap.Field{
		zap.String("source", source), zap.Int("rows", result.Rows), zap.Int("imported", result.Imported),
		zap.Int("already_present", result.AlreadyPresent), zap.Int("skipped_invalid", result.SkippedInvalid),
		zap.Int("skipped_oversize", result.SkippedOversize),
	}
	if result.SkippedInvalid > 0 || result.SkippedOversize > 0 {
		logger.Warn("relay sidecar imported legacy SQLite store with skipped events; the source file is kept", fields...)
	} else {
		logger.Info("relay sidecar imported legacy SQLite store; the source file is kept as a rollback copy", fields...)
	}
	return result, nil
}

func importSQLiteRows(ctx context.Context, db *sql.DB, store *eventStore, result *sqliteMigrationResult) error {
	// Oldest first, so that if the table ever held several versions of a
	// coordinate, latest-wins replacement ends on the newest.
	rows, err := db.QueryContext(ctx, `SELECT event_json FROM events ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return fmt.Errorf("read legacy relay sidecar events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	backend := store.backend()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Rows++
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return fmt.Errorf("scan legacy relay sidecar event: %w", err)
		}
		var event nostr.Event
		if json.Unmarshal(encoded, &event) != nil || !event.CheckID() || event.Kind.IsEphemeral() {
			result.SkippedInvalid++
			continue
		}
		if storableEvent(event) != nil {
			result.SkippedOversize++
			continue
		}
		stored := true
		if event.Kind.IsReplaceable() || event.Kind.IsAddressable() {
			if current, found := store.latest(coordinateFilter(event.Kind, event.PubKey, event.Tags.GetD())); found && !nostr.IsOlder(current, event) {
				stored = false
			} else if _, err := backend.ReplaceEvent(event); err != nil {
				return fmt.Errorf("import legacy relay event %s: %w", event.ID.Hex(), err)
			}
		} else if err := backend.SaveEvent(event); errors.Is(err, eventstore.ErrDupEvent) {
			stored = false
		} else if err != nil {
			return fmt.Errorf("import legacy relay event %s: %w", event.ID.Hex(), err)
		}
		if !stored {
			result.AlreadyPresent++
			continue
		}
		if err := store.indexExpiration(event); err != nil {
			return err
		}
		result.Imported++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read legacy relay sidecar events: %w", err)
	}
	return nil
}
