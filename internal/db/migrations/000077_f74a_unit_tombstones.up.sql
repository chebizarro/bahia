-- A retired unit remains a durable FK target for archived observations and
-- hot-table rehydration. An existing orphan is not enough information to
-- reconstruct the unit; refuse the migration instead of fabricating one.
DO $$ BEGIN
  IF EXISTS (
    SELECT 1 FROM runtime_observation_archive a
    LEFT JOIN deployment_units du ON du.id = a.deployment_unit_id
    WHERE a.deployment_unit_id IS NOT NULL AND du.id IS NULL
  ) THEN
    RAISE EXCEPTION 'F74a archive has deployment-unit orphans; restore original units from a verified backup before migration';
  END IF;
END $$;

ALTER TABLE deployment_units ADD COLUMN retired_at TIMESTAMPTZ;
ALTER TABLE deployment_units DROP CONSTRAINT deployment_units_environment_id_unit_key_key;
CREATE UNIQUE INDEX deployment_units_active_environment_key
  ON deployment_units(environment_id, unit_key) WHERE retired_at IS NULL;

CREATE FUNCTION f74a_guard_retired_unit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.retired_at IS NOT NULL THEN
    IF NEW IS DISTINCT FROM OLD THEN
      RAISE EXCEPTION 'retired deployment unit % is immutable', OLD.id;
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_retired_unit_immutable BEFORE UPDATE ON deployment_units
  FOR EACH ROW EXECUTE FUNCTION f74a_guard_retired_unit();

ALTER TABLE runtime_observation_archive ADD CONSTRAINT fk_f74a_archive_deployment_unit
  FOREIGN KEY (deployment_unit_id) REFERENCES deployment_units(id) ON DELETE RESTRICT;
