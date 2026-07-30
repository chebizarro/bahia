package relaysidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	_ "modernc.org/sqlite"
)

// sqliteStore is the durable source of relay history. Nostr subscribers may
// disconnect and replay at any time, so accepted events must survive relay
// process and container restarts.
type sqliteStore struct {
	db     *sql.DB
	readDB *sql.DB
}

func newSQLiteStore(dataDir string) (*sqliteStore, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("relay sidecar data_dir is required")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create relay sidecar data directory: %w", err)
	}
	// Connection PRAGMAs belong in the DSN so database/sql applies them to
	// every lazily opened connection, not just the first one.
	dsn := filepath.Join(dataDir, "events.sqlite") +
		"?_pragma=busy_timeout%3d30000&_pragma=journal_mode%3dWAL&_pragma=synchronous%3dFULL"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open relay sidecar event store: %w", err)
	}
	// Keep writes on their own connection pool. Relay replay queries can be
	// long-running, and must never consume the connection needed to persist a
	// publisher event and return its OK.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
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
	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open relay sidecar read pool: %w", err)
	}
	readDB.SetMaxOpenConns(32)
	return &sqliteStore{db: db, readDB: readDB}, nil
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
	limit := maxLimit
	if filter.Limit > 0 && (limit <= 0 || filter.Limit < limit) {
		limit = filter.Limit
	}
	if filter.LimitZero {
		return func(func(nostr.Event) bool) {}
	}

	query, args := relayQuerySQL(filter)
	return func(yield func(nostr.Event) bool) {
		rows, err := s.readDB.QueryContext(ctx, query, args...)
		if err != nil {
			return
		}
		defer rows.Close()

		matched := 0
		for rows.Next() {
			var encoded []byte
			var event nostr.Event
			if rows.Scan(&encoded) != nil || json.Unmarshal(encoded, &event) != nil || !filter.Matches(event) {
				continue
			}
			if !yield(event) {
				return
			}
			matched++
			if limit > 0 && matched >= limit {
				return
			}
		}
	}
}

func relayQuerySQL(filter nostr.Filter) (string, []any) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, len(filter.IDs)+len(filter.Kinds)+len(filter.Authors)+2)
	addSet := func(column string, values []string) {
		if values == nil {
			return
		}
		if len(values) == 0 {
			clauses = append(clauses, "1 = 0")
			return
		}
		placeholders := make([]string, len(values))
		for i, value := range values {
			placeholders[i] = "?"
			args = append(args, value)
		}
		clauses = append(clauses, column+" IN ("+strings.Join(placeholders, ",")+")")
	}

	if filter.IDs != nil {
		ids := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			ids[i] = id.Hex()
		}
		addSet("id", ids)
	}

	if filter.Kinds != nil {
		if len(filter.Kinds) == 0 {
			clauses = append(clauses, "1 = 0")
		} else {
			placeholders := make([]string, len(filter.Kinds))
			for i, kind := range filter.Kinds {
				placeholders[i] = "?"
				args = append(args, int(kind))
			}
			clauses = append(clauses, "kind IN ("+strings.Join(placeholders, ",")+")")
		}
	}

	if filter.Authors != nil {
		authors := make([]string, len(filter.Authors))
		for i, author := range filter.Authors {
			authors[i] = author.Hex()
		}
		addSet("pubkey", authors)
	}

	if filter.Since != 0 {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, int64(filter.Since))
	}
	if filter.Until != 0 {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, int64(filter.Until))
	}

	query := "SELECT event_json FROM events"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC, id"
	return query, args
}

func (s *sqliteStore) Close() error {
	readErr := s.readDB.Close()
	writeErr := s.db.Close()
	if writeErr != nil {
		return writeErr
	}
	return readErr
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
