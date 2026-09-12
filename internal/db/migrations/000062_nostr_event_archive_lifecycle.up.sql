-- Crash-safe, two-phase archival metadata for the append-only Nostr event store.
--
-- Large indexes are deliberately not built in this startup migration. The
-- archive maintenance command creates them CONCURRENTLY before claiming rows,
-- so a Bahia restart never waits for a 19GB table rewrite or blocks writers.
CREATE TABLE IF NOT EXISTS nostr_event_archive_batches (
    id UUID PRIMARY KEY,
    cutoff_at TIMESTAMPTZ NOT NULL,
    kinds INTEGER[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'claimed',
    row_count BIGINT NOT NULL DEFAULT 0,
    exported_path TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL DEFAULT '',
    compressed_bytes BIGINT NOT NULL DEFAULT 0,
    object_uri TEXT NOT NULL DEFAULT '',
    object_version TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    exported_at TIMESTAMPTZ,
    protected_at TIMESTAMPTZ,
    pruned_at TIMESTAMPTZ,
    CONSTRAINT nostr_event_archive_batches_status_check
        CHECK (status IN ('claimed', 'exported', 'protected', 'pruned')),
    CONSTRAINT nostr_event_archive_batches_sha256_check
        CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT nostr_event_archive_batches_protected_receipt_check
        CHECK (status NOT IN ('protected', 'pruned') OR
               (object_uri <> '' AND object_version <> '' AND sha256 <> ''))
);

ALTER TABLE nostr_events
    ADD COLUMN IF NOT EXISTS archive_batch_id UUID;

DO $$
BEGIN
    ALTER TABLE nostr_events
        ADD CONSTRAINT nostr_events_archive_batch_id_fkey
        FOREIGN KEY (archive_batch_id)
        REFERENCES nostr_event_archive_batches(id)
        ON DELETE RESTRICT
        NOT VALID;
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;
