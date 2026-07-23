package relaysidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	_ "modernc.org/sqlite"
)

// sqliteStore is the durable source of relay history. Nostr subscribers may
// disconnect and replay at any time, so accepted events must survive relay
// process and container restarts.
type sqliteStore struct {
	db *sql.DB
}

func newSQLiteStore(dataDir string) (*sqliteStore, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("relay sidecar data_dir is required")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create relay sidecar data directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "events.sqlite"))
	if err != nil {
		return nil, fmt.Errorf("open relay sidecar event store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			kind INTEGER NOT NULL,
			pubkey TEXT NOT NULL,
			replaceable_key TEXT,
			event_json BLOB NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS events_replaceable_key
			ON events(replaceable_key) WHERE replaceable_key IS NOT NULL;
		CREATE INDEX IF NOT EXISTS events_created_at ON events(created_at DESC);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize relay sidecar event store: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Save(ctx context.Context, event nostr.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode relay event: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO events (id, created_at, kind, pubkey, replaceable_key, event_json)
		VALUES (?, ?, ?, ?, NULL, ?)`,
		event.ID.Hex(), int64(event.CreatedAt), int(event.Kind), event.PubKey.Hex(), encoded)
	if err != nil {
		return fmt.Errorf("store relay event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check stored relay event: %w", err)
	}
	if affected == 0 {
		return eventstore.ErrDupEvent
	}
	return nil
}

func (s *sqliteStore) Replace(ctx context.Context, event nostr.Event) error {
	key := replaceableKey(event)
	if key == "" {
		return s.Save(ctx, event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode replaceable relay event: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO events (id, created_at, kind, pubkey, replaceable_key, event_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(replaceable_key) WHERE replaceable_key IS NOT NULL DO UPDATE SET
			id = excluded.id,
			created_at = excluded.created_at,
			kind = excluded.kind,
			pubkey = excluded.pubkey,
			event_json = excluded.event_json
		WHERE excluded.created_at > events.created_at`,
		event.ID.Hex(), int64(event.CreatedAt), int(event.Kind), event.PubKey.Hex(), key, encoded)
	if err != nil {
		return fmt.Errorf("replace relay event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check replaced relay event: %w", err)
	}
	if affected == 0 {
		return eventstore.ErrDupEvent
	}
	return nil
}

func (s *sqliteStore) Delete(ctx context.Context, id nostr.ID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id.Hex()); err != nil {
		return fmt.Errorf("delete relay event: %w", err)
	}
	return nil
}

func (s *sqliteStore) Count(ctx context.Context, filter nostr.Filter) uint32 {
	var count uint32
	for event := range s.Query(ctx, filter, 0) {
		if filter.Matches(event) {
			count++
		}
	}
	return count
}

func (s *sqliteStore) Query(ctx context.Context, filter nostr.Filter, maxLimit int) iter.Seq[nostr.Event] {
	rows, err := s.db.QueryContext(ctx, `SELECT event_json FROM events ORDER BY created_at DESC, id`)
	if err != nil {
		return func(func(nostr.Event) bool) {}
	}
	defer rows.Close()
	events := make([]nostr.Event, 0)
	for rows.Next() {
		var encoded []byte
		var event nostr.Event
		if rows.Scan(&encoded) == nil && json.Unmarshal(encoded, &event) == nil && filter.Matches(event) {
			events = append(events, event)
		}
	}

	limit := maxLimit
	if filter.Limit > 0 && (limit <= 0 || filter.Limit < limit) {
		limit = filter.Limit
	}
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}

	return func(yield func(nostr.Event) bool) {
		for i := 0; i < limit; i++ {
			if !yield(events[i]) {
				return
			}
		}
	}
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

func replaceableKey(event nostr.Event) string {
	if event.Kind.IsReplaceable() {
		return fmt.Sprintf("%d:%s", event.Kind, event.PubKey.Hex())
	}
	if event.Kind.IsAddressable() {
		return fmt.Sprintf("%d:%s:%s", event.Kind, event.PubKey.Hex(), event.Tags.GetD())
	}
	return ""
}
