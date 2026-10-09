DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM deployment_units WHERE retired_at IS NOT NULL)
     OR EXISTS (SELECT 1 FROM runtime_observation_archive WHERE deployment_unit_id IS NOT NULL) THEN
    RAISE EXCEPTION 'cannot roll back F74a unit tombstones while retired units or archived unit references exist';
  END IF;
END $$;

ALTER TABLE runtime_observation_archive DROP CONSTRAINT fk_f74a_archive_deployment_unit;
DROP TRIGGER f74a_retired_unit_immutable ON deployment_units;
DROP FUNCTION f74a_guard_retired_unit();
DROP INDEX deployment_units_active_environment_key;
ALTER TABLE deployment_units ADD CONSTRAINT deployment_units_environment_id_unit_key_key UNIQUE (environment_id, unit_key);
ALTER TABLE deployment_units DROP COLUMN retired_at;
