-- Extend worker advertisements with hardware resources and runtime targets.
ALTER TABLE workers
  ADD COLUMN resources JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN accelerators JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN runtime_target JSONB NOT NULL DEFAULT '{}'::jsonb;

