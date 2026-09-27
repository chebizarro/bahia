-- Index the authoritative first-d coordinate for every recorded revision, in
-- winner order. The expression covers existing rows and future inserts without
-- changing event-writing call sites or rewriting the audit table.
CREATE FUNCTION nostr_coordinate_d_tag(event_kind integer, event_tags jsonb)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE WHEN event_kind BETWEEN 30000 AND 39999 THEN
        COALESCE((
            SELECT tag->>1
            FROM jsonb_array_elements(event_tags) WITH ORDINALITY AS t(tag, ord)
            WHERE tag->>0 = 'd'
            ORDER BY ord
            LIMIT 1
        ), '')
    ELSE '' END
$$;

CREATE INDEX idx_nostr_events_coordinate_winner
    ON nostr_events (kind, pubkey, nostr_coordinate_d_tag(kind, tags), created_at DESC, id ASC);
