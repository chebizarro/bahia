ALTER TABLE workers
  ADD COLUMN IF NOT EXISTS ml_capabilities JSONB NOT NULL DEFAULT '{}'::jsonb;
