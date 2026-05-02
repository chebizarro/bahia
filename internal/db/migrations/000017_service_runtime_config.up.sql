-- Add service-scoped runtime configuration for adopted workloads.
ALTER TABLE services
  ADD COLUMN IF NOT EXISTS runtime_config JSONB NOT NULL DEFAULT '{}'::jsonb;
