-- 000060_route_canary: route canary outage state and append-only failure lineage
-- for Bahia-managed public and internal routes.
--
-- Route state is keyed by the same service/environment/deployment-unit
-- coordinate as managed_instance_health so an operator can contrast a healthy
-- container with a broken user-facing route.

CREATE TABLE route_canary_state (
  route_coordinate TEXT PRIMARY KEY,
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE CASCADE,
  hostname TEXT NOT NULL,
  open BOOLEAN NOT NULL DEFAULT FALSE,
  classification TEXT NOT NULL,
  perspective TEXT NOT NULL DEFAULT '',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  consecutive_successes INTEGER NOT NULL DEFAULT 0,
  failure_reason TEXT NOT NULL DEFAULT '',
  tls_not_after TIMESTAMPTZ,
  last_observed_at TIMESTAMPTZ NOT NULL,
  opened_at TIMESTAMPTZ,
  last_recovered_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE NULLS NOT DISTINCT (service_id, environment_id, deployment_unit_id, hostname),
  CHECK (hostname <> ''),
  CHECK (classification IN (
    'route_ok', 'dns_unresolved', 'connect_failed', 'tls_invalid',
    'tls_expiring', 'upstream_error', 'status_mismatch', 'body_mismatch'
  )),
  CHECK (perspective IN ('', 'public_edge', 'internal_lan')),
  CHECK (consecutive_failures >= 0),
  CHECK (consecutive_successes >= 0),
  -- An open outage must record when it started, so lineage always has a
  -- beginning; a closed one must not carry a stale opening timestamp.
  CHECK ((open AND opened_at IS NOT NULL) OR (NOT open AND opened_at IS NULL))
);

CREATE INDEX route_canary_state_environment_idx
  ON route_canary_state (environment_id, last_observed_at DESC);
CREATE INDEX route_canary_state_service_idx
  ON route_canary_state (service_id, last_observed_at DESC);
CREATE INDEX route_canary_state_open_idx
  ON route_canary_state (classification, last_observed_at DESC)
  WHERE open;

CREATE TABLE route_canary_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  route_coordinate TEXT NOT NULL,
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE CASCADE,
  hostname TEXT NOT NULL,
  transition TEXT NOT NULL,
  previous_classification TEXT NOT NULL DEFAULT '',
  classification TEXT NOT NULL,
  perspective TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  evidence TEXT NOT NULL DEFAULT '',
  -- The container-level status observed at the same moment. This is what makes
  -- "service healthy, route broken" a queryable fact rather than an inference.
  observed_instance_status TEXT NOT NULL DEFAULT '',
  observed_at TIMESTAMPTZ NOT NULL,
  CHECK (hostname <> ''),
  CHECK (transition IN ('none', 'opened', 'recovered', 'classification_changed')),
  CHECK (classification IN (
    'route_ok', 'dns_unresolved', 'connect_failed', 'tls_invalid',
    'tls_expiring', 'upstream_error', 'status_mismatch', 'body_mismatch'
  )),
  CHECK (perspective IN ('', 'public_edge', 'internal_lan'))
);

CREATE INDEX route_canary_events_coordinate_idx
  ON route_canary_events (route_coordinate, observed_at DESC);
CREATE INDEX route_canary_events_environment_idx
  ON route_canary_events (environment_id, observed_at DESC);
