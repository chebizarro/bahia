ALTER TABLE workers
  ADD COLUMN IF NOT EXISTS standby_assignments JSONB NOT NULL DEFAULT '[]'::jsonb;
