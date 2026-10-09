-- A backdated insert can turn an archived no-op successor into a material
-- transition. Keep that successor hot; the immutable archive remains its audit
-- copy. Both triggers take the same coordinate lock before an ID lock.
CREATE FUNCTION f74a_rehydrate_backdated_successor() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE successor_id UUID;
BEGIN
  PERFORM f74a_lock_observation_coordinate(NEW.service_id, NEW.environment_id);
  -- Restoring an archived row is not a new observation. In particular, do not
  -- recursively restore every later archived row in the coordinate.
  IF EXISTS (SELECT 1 FROM runtime_observation_archive WHERE id = NEW.id) THEN
    RETURN NEW;
  END IF;

  SELECT h.id INTO successor_id
  FROM runtime_observation_history h
  WHERE h.service_id = NEW.service_id AND h.environment_id = NEW.environment_id
    AND (h.observed_at, h.id) > (NEW.observed_at, NEW.id)
  ORDER BY h.observed_at, h.id LIMIT 1;
  IF successor_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM runtime_observation_archive WHERE id = successor_id
  ) THEN
    RETURN NEW;
  END IF;

  INSERT INTO runtime_observations
    (id, service_id, environment_id, deployment_unit_id, observed_image_digest,
     observed_image_repo, observed_container_id, observed_host, observed_version,
     health_status, source, metadata, normalized_state, normalized_hash, observed_at)
  SELECT id, service_id, environment_id, deployment_unit_id, observed_image_digest,
         observed_image_repo, observed_container_id, observed_host, observed_version,
         health_status, source, metadata, normalized_state, normalized_hash, observed_at
  FROM runtime_observation_archive WHERE id = successor_id
  ON CONFLICT (id) DO NOTHING;
  RETURN NEW;
END $$;

CREATE TRIGGER f74a_backdated_successor_hot BEFORE INSERT ON runtime_observations
FOR EACH ROW EXECUTE FUNCTION f74a_rehydrate_backdated_successor();
