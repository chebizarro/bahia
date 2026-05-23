DROP INDEX IF EXISTS idx_workers_capabilities_gin;
DROP INDEX IF EXISTS idx_workers_labels_gin;
DROP INDEX IF EXISTS idx_workers_scheduling_state;

ALTER TABLE workers
  DROP CONSTRAINT IF EXISTS workers_scheduling_state_check;

ALTER TABLE workers
  DROP COLUMN IF EXISTS capabilities,
  DROP COLUMN IF EXISTS labels,
  DROP COLUMN IF EXISTS scheduling_note,
  DROP COLUMN IF EXISTS scheduling_state;
