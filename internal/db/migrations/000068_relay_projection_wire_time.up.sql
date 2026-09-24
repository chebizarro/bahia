-- Ordering watermarks used to contain payload updated_at. Rebase them onto
-- the signed timestamp before comparing same-second NIP-01 replacements.
UPDATE relay_projection_meta
SET updated_at = nostr_events.created_at
FROM nostr_events
WHERE relay_projection_meta.source_event_id = nostr_events.id;

-- A missing source cannot supply a trustworthy wire watermark. Invalidate
-- only its cache metadata; canonical events and projected entities are kept.
DELETE FROM relay_projection_meta
WHERE NOT EXISTS (
  SELECT 1 FROM nostr_events
  WHERE nostr_events.id = relay_projection_meta.source_event_id
);
