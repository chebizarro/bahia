-- Reverting narrows the classification set again, so rows carrying the
-- withdrawn value must be normalized first or the constraint cannot be added.
--
-- health_path_not_discriminating is a warning meaning "the route is serving but
-- the health path proves nothing". Downgrading it to route_ok preserves the
-- operational fact that the route was up and loses only the caveat, which is
-- the least misleading option available when the value can no longer be stored.
-- Lineage rows are deleted rather than rewritten, because an append-only event
-- must not be silently restated as something that was never observed.

UPDATE route_canary_state
  SET classification = 'route_ok', failure_reason = ''
  WHERE classification = 'health_path_not_discriminating';

DELETE FROM route_canary_events
  WHERE classification = 'health_path_not_discriminating'
     OR previous_classification = 'health_path_not_discriminating';

ALTER TABLE route_canary_state DROP CONSTRAINT IF EXISTS route_canary_state_classification_check;
ALTER TABLE route_canary_events DROP CONSTRAINT IF EXISTS route_canary_events_classification_check;

ALTER TABLE route_canary_state
  ADD CONSTRAINT route_canary_state_classification_check
  CHECK (classification IN (
    'route_ok', 'dns_unresolved', 'connect_failed', 'tls_invalid',
    'tls_expiring', 'upstream_error', 'status_mismatch', 'body_mismatch'
  ));

ALTER TABLE route_canary_events
  ADD CONSTRAINT route_canary_events_classification_check
  CHECK (classification IN (
    'route_ok', 'dns_unresolved', 'connect_failed', 'tls_invalid',
    'tls_expiring', 'upstream_error', 'status_mismatch', 'body_mismatch'
  ));
