-- Remove service-scoped runtime configuration.
ALTER TABLE services
  DROP COLUMN IF EXISTS runtime_config;
