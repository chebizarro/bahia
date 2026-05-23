ALTER TABLE workers
  ADD COLUMN IF NOT EXISTS scheduling_state TEXT NOT NULL DEFAULT 'active',
  ADD COLUMN IF NOT EXISTS scheduling_note TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS capabilities JSONB NOT NULL DEFAULT '{}'::jsonb;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'workers_scheduling_state_check'
  ) THEN
    ALTER TABLE workers
      ADD CONSTRAINT workers_scheduling_state_check
      CHECK (scheduling_state IN ('active', 'cordoned', 'draining', 'maintenance', 'disabled'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_workers_scheduling_state ON workers(scheduling_state);
CREATE INDEX IF NOT EXISTS idx_workers_labels_gin ON workers USING GIN (labels);
CREATE INDEX IF NOT EXISTS idx_workers_capabilities_gin ON workers USING GIN (capabilities);
