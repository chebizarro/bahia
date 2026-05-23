-- Bahia-owned schedule dispatch state for backup definitions.
CREATE TABLE backup_schedule_states (
  definition_id UUID PRIMARY KEY REFERENCES backup_definitions(id) ON DELETE CASCADE,
  next_scheduled_run TIMESTAMPTZ,
  last_scheduled_dispatch TIMESTAMPTZ,
  last_scheduled_run_due_at TIMESTAMPTZ,
  missed_run_count INTEGER NOT NULL DEFAULT 0 CHECK (missed_run_count >= 0),
  pause_reason TEXT NOT NULL DEFAULT '',
  paused_by TEXT NOT NULL DEFAULT '',
  paused_at TIMESTAMPTZ,
  disabled_by TEXT NOT NULL DEFAULT '',
  disable_reason TEXT NOT NULL DEFAULT '',
  disabled_at TIMESTAMPTZ,
  maintenance_window TEXT NOT NULL DEFAULT '',
  maintenance_window_timezone TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_backup_schedule_states_next_run ON backup_schedule_states(next_scheduled_run) WHERE next_scheduled_run IS NOT NULL;
CREATE INDEX idx_backup_schedule_states_paused ON backup_schedule_states(paused_at) WHERE paused_at IS NOT NULL;
CREATE INDEX idx_backup_schedule_states_disabled ON backup_schedule_states(disabled_at) WHERE disabled_at IS NOT NULL;
