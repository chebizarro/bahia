CREATE TABLE nostr_migration_cursors (
    name TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL,
    event_id TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
