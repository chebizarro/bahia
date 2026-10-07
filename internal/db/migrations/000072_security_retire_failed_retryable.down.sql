-- Explicit operator rollback only (never run at startup).
--
-- Restores the 000044 CHECK constraints that accept failed_retryable. Every
-- existing row already satisfies the wider constraint, so it is added NOT
-- VALID and nothing scans the tables under ACCESS EXCLUSIVE.
--
-- idx_security_observable_publications_retry is deliberately not rebuilt: it
-- only served the Security retry loop removed in irsry.28/.29, the code this
-- rollback returns to never reads it, and building it would block writes to
-- security_observable_publications for the duration of the build. Publications
-- that 000072 moved to failed_terminal stay terminal: nothing would retry them.

ALTER TABLE security_observable_publications
    DROP CONSTRAINT IF EXISTS security_observable_publications_publish_state_check;
ALTER TABLE security_observable_publications
    ADD CONSTRAINT security_observable_publications_publish_state_check
    CHECK (publish_state IN ('pending', 'published', 'failed_retryable', 'failed_terminal'))
    NOT VALID;

ALTER TABLE security_scan_runs
    DROP CONSTRAINT IF EXISTS security_scan_runs_publish_state_check;
ALTER TABLE security_scan_runs
    ADD CONSTRAINT security_scan_runs_publish_state_check
    CHECK (publish_state IN ('pending', 'published', 'failed_retryable', 'failed_terminal'))
    NOT VALID;
