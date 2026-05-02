ALTER TABLE workers
  DROP COLUMN IF EXISTS runtime_target,
  DROP COLUMN IF EXISTS accelerators,
  DROP COLUMN IF EXISTS resources;

