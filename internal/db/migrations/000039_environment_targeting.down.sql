-- Reverse 000039_environment_targeting.

ALTER TABLE deployment_units
  DROP COLUMN IF EXISTS network_profile,
  DROP COLUMN IF EXISTS namespace,
  DROP COLUMN IF EXISTS compose_dir,
  DROP COLUMN IF EXISTS endpoint_ref;

ALTER TABLE environments
  DROP COLUMN IF EXISTS targeting;
