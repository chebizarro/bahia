-- Retire the 'failed_retryable' Security publication state (000044). Relay
-- delivery and its retries belong to the Nostr publish outbox (irsry.28/.29):
-- nothing writes or retries failed_retryable any more, so the state leaves the
-- CHECK constraints and its retry partial index goes away.
--
-- This runs at startup, so it follows the 000062/000071 conventions
-- (docs/designs/nostr-event-store-lifecycle.md): every statement is
-- metadata-only or touches only the retired rows through an index.
--   - The only UPDATE converts leftover failed_retryable publications and is
--     served by idx_security_observable_publications_retry, whose partial
--     predicate holds exactly those rows. It runs before that index is dropped.
--   - Dropping the retired partial index is metadata-only (no build). It needs
--     the ACCESS EXCLUSIVE lock that the ALTER TABLE statements below take on
--     the same table anyway, only until this migration commits.
--   - The narrowed CHECKs are added NOT VALID: new writes are checked at once,
--     existing rows are not scanned here. security_scan_runs has no index on
--     publish_state, so its leftover failed_retryable rows are converted out
--     of band before validation: `bahia-event-archive ensure-indexes`
--     (EnsureOnlineIndexes) runs that UPDATE and then VALIDATE CONSTRAINT for
--     both tables, which takes only SHARE UPDATE EXCLUSIVE.

UPDATE security_observable_publications
SET publish_state = 'failed_terminal',
    next_retry_at = NULL,
    last_error = concat_ws(' | ', NULLIF(last_error, ''), 'failed_retryable retired by migration 000072, the outbox owns retries')
WHERE publish_state = 'failed_retryable';

DROP INDEX IF EXISTS idx_security_observable_publications_retry;

ALTER TABLE security_observable_publications
    DROP CONSTRAINT IF EXISTS security_observable_publications_publish_state_check;
ALTER TABLE security_observable_publications
    ADD CONSTRAINT security_observable_publications_publish_state_check
    CHECK (publish_state IN ('pending', 'published', 'failed_terminal'))
    NOT VALID;

ALTER TABLE security_scan_runs
    DROP CONSTRAINT IF EXISTS security_scan_runs_publish_state_check;
ALTER TABLE security_scan_runs
    ADD CONSTRAINT security_scan_runs_publish_state_check
    CHECK (publish_state IN ('pending', 'published', 'failed_terminal'))
    NOT VALID;
