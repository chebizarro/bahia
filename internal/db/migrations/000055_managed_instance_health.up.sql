-- 000055_managed_instance_health: managed runtime health, recovery, and maintenance state.

CREATE TABLE managed_instance_health (
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE CASCADE,
  runtime_target_name TEXT NOT NULL,
  host TEXT NOT NULL DEFAULT '',
  supervisor_type TEXT NOT NULL,
  status TEXT NOT NULL,
  failure_reason TEXT NOT NULL DEFAULT '',
  last_observed_at TIMESTAMPTZ NOT NULL,
  failure_generation_at TIMESTAMPTZ,
  restart_count INTEGER NOT NULL DEFAULT 0,
  consecutive_restart_count INTEGER NOT NULL DEFAULT 0,
  memory_current_bytes BIGINT NOT NULL DEFAULT 0,
  memory_peak_bytes BIGINT NOT NULL DEFAULT 0,
  memory_limit_bytes BIGINT NOT NULL DEFAULT 0,
  last_recovery_attempt JSONB,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE NULLS NOT DISTINCT (service_id, environment_id, deployment_unit_id, runtime_target_name),
  CHECK (runtime_target_name <> ''),
  CHECK (supervisor_type IN ('docker', 'compose', 'systemd', 'user-systemd')),
  CHECK (status IN ('healthy', 'running', 'degraded', 'stopped', 'unhealthy', 'oom_killed', 'restart_loop', 'unknown', 'manual_override')),
  CHECK (restart_count >= 0),
  CHECK (consecutive_restart_count >= 0),
  CHECK (memory_current_bytes >= 0),
  CHECK (memory_peak_bytes >= 0),
  CHECK (memory_limit_bytes >= 0)
);

CREATE INDEX managed_instance_health_environment_idx
  ON managed_instance_health (environment_id, last_observed_at DESC);
CREATE INDEX managed_instance_health_service_idx
  ON managed_instance_health (service_id, last_observed_at DESC);
CREATE INDEX managed_instance_health_unhealthy_idx
  ON managed_instance_health (status, last_observed_at DESC)
  WHERE status NOT IN ('healthy', 'running');

CREATE TABLE managed_instance_health_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE CASCADE,
  runtime_target_name TEXT NOT NULL,
  previous_status TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  evidence TEXT NOT NULL DEFAULT '',
  observed_at TIMESTAMPTZ NOT NULL,
  CHECK (runtime_target_name <> ''),
  CHECK (previous_status = '' OR previous_status IN ('healthy', 'running', 'degraded', 'stopped', 'unhealthy', 'oom_killed', 'restart_loop', 'unknown', 'manual_override')),
  CHECK (status IN ('healthy', 'running', 'degraded', 'stopped', 'unhealthy', 'oom_killed', 'restart_loop', 'unknown', 'manual_override'))
);

CREATE INDEX managed_instance_health_events_target_idx
  ON managed_instance_health_events (service_id, environment_id, deployment_unit_id, runtime_target_name, observed_at DESC);

CREATE TABLE managed_instance_recovery_attempts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE CASCADE,
  runtime_target_name TEXT NOT NULL,
  correlation_id TEXT NOT NULL UNIQUE,
  requested_at TIMESTAMPTZ NOT NULL,
  result TEXT NOT NULL,
  evidence TEXT NOT NULL DEFAULT '',
  CHECK (runtime_target_name <> ''),
  CHECK (correlation_id <> ''),
  CHECK (result IN ('pending', 'success', 'degraded', 'failed', 'budget_exhausted', 'skipped_override'))
);

CREATE INDEX managed_instance_recovery_attempts_target_idx
  ON managed_instance_recovery_attempts (service_id, environment_id, deployment_unit_id, runtime_target_name, requested_at DESC);

CREATE TABLE managed_instance_overrides (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  deployment_unit_id UUID REFERENCES deployment_units(id) ON DELETE CASCADE,
  runtime_target_name TEXT NOT NULL,
  actor TEXT NOT NULL,
  reason TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ,
  UNIQUE NULLS NOT DISTINCT (service_id, environment_id, deployment_unit_id, runtime_target_name),
  CHECK (runtime_target_name <> ''),
  CHECK (actor <> ''),
  CHECK (reason <> ''),
  CHECK (expires_at IS NULL OR expires_at > created_at)
);

CREATE INDEX managed_instance_overrides_active_idx
  ON managed_instance_overrides (expires_at);
