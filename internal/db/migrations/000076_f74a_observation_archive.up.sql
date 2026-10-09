-- Lossless, queryable observation archive. The archive deliberately has no
-- service/environment/unit foreign keys: deleting a projection must not erase
-- audit history.
CREATE TABLE f74a_observation_compaction_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  cutoff TIMESTAMPTZ NOT NULL,
  batch_size INTEGER NOT NULL CHECK (batch_size BETWEEN 1 AND 1000),
  cursor_service_id UUID,
  cursor_environment_id UUID,
  cursor_observed_at TIMESTAMPTZ,
  cursor_id UUID,
  examined_count BIGINT NOT NULL DEFAULT 0,
  archived_count BIGINT NOT NULL DEFAULT 0,
  complete BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((cursor_service_id IS NULL AND cursor_environment_id IS NULL AND cursor_observed_at IS NULL AND cursor_id IS NULL)
      OR (cursor_service_id IS NOT NULL AND cursor_environment_id IS NOT NULL AND cursor_observed_at IS NOT NULL AND cursor_id IS NOT NULL))
);

CREATE FUNCTION f74a_guard_archive_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.cutoff <> OLD.cutoff OR NEW.batch_size <> OLD.batch_size OR NEW.created_at <> OLD.created_at
     OR NEW.examined_count < OLD.examined_count OR NEW.archived_count < OLD.archived_count
     OR (OLD.complete AND NOT NEW.complete)
     OR (OLD.cursor_id IS NOT NULL AND NEW.cursor_id IS NULL)
     OR (OLD.cursor_id IS NOT NULL AND
         (NEW.cursor_service_id,NEW.cursor_environment_id,NEW.cursor_observed_at,NEW.cursor_id)
         < (OLD.cursor_service_id,OLD.cursor_environment_id,OLD.cursor_observed_at,OLD.cursor_id)) THEN
    RAISE EXCEPTION 'F74a archive run cutoff and progress are immutable/monotonic';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_archive_run_guard BEFORE UPDATE ON f74a_observation_compaction_runs
FOR EACH ROW EXECUTE FUNCTION f74a_guard_archive_run();

CREATE TABLE f74a_observation_archive_batches (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id UUID NOT NULL REFERENCES f74a_observation_compaction_runs(id),
  examined_count INTEGER NOT NULL CHECK (examined_count BETWEEN 0 AND 1000),
  archived_count INTEGER NOT NULL CHECK (archived_count BETWEEN 0 AND examined_count),
  cursor_service_id UUID,
  cursor_environment_id UUID,
  cursor_observed_at TIMESTAMPTZ,
  cursor_id UUID,
  committed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE runtime_observation_archive (
  LIKE runtime_observations INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
  archive_batch_id UUID NOT NULL REFERENCES f74a_observation_archive_batches(id) DEFERRABLE INITIALLY DEFERRED,
  archived_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  row_digest BYTEA NOT NULL CHECK (length(row_digest) = 32),
  PRIMARY KEY (id)
);

CREATE INDEX idx_runtime_observations_ordered ON runtime_observations(service_id, environment_id, observed_at, id);
CREATE INDEX idx_runtime_observation_archive_ordered ON runtime_observation_archive(service_id, environment_id, observed_at, id);
CREATE INDEX idx_runtime_observation_archive_unit ON runtime_observation_archive(deployment_unit_id) WHERE deployment_unit_id IS NOT NULL;

-- Exclude provenance and normalize the only timestamp to UTC epoch microseconds.
-- JSONB has a canonical persisted representation; this digest covers every
-- original observation column, including nullable and JSONB columns.
CREATE FUNCTION f74a_observation_digest(row_value JSONB, observed_time TIMESTAMPTZ)
RETURNS BYTEA LANGUAGE sql IMMUTABLE STRICT AS $$
  SELECT digest(convert_to((row_value - 'archive_batch_id' - 'archived_at' - 'row_digest' - 'observed_at'
    || jsonb_build_object('observed_at_epoch_us', extract(epoch FROM observed_time) * 1000000))::text, 'UTF8'), 'sha256')
$$;

CREATE FUNCTION f74a_archive_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'runtime observation archive is immutable';
END $$;
CREATE TRIGGER f74a_archive_no_update BEFORE UPDATE OR DELETE ON runtime_observation_archive
FOR EACH ROW EXECUTE FUNCTION f74a_archive_immutable();
CREATE TRIGGER f74a_archive_batch_immutable BEFORE UPDATE OR DELETE ON f74a_observation_archive_batches
FOR EACH ROW EXECUTE FUNCTION f74a_archive_immutable();

CREATE FUNCTION f74a_check_archive_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE hot_digest BYTEA;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.id::text, 7401));
  IF NEW.row_digest <> f74a_observation_digest(to_jsonb(NEW), NEW.observed_at) THEN
    RAISE EXCEPTION 'archive digest mismatch for observation %', NEW.id;
  END IF;
  SELECT f74a_observation_digest(to_jsonb(h), h.observed_at) INTO hot_digest
    FROM runtime_observations h WHERE h.id = NEW.id;
  IF FOUND AND hot_digest <> NEW.row_digest THEN
    RAISE EXCEPTION 'observation % conflicts with hot row', NEW.id;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_archive_insert_identity BEFORE INSERT ON runtime_observation_archive
FOR EACH ROW EXECUTE FUNCTION f74a_check_archive_insert();

CREATE FUNCTION f74a_check_hot_against_archive() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE archived_digest BYTEA;
BEGIN
  -- This is also the lock used by the mover and state-link rehydration.
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.id::text, 7401));
  SELECT row_digest INTO archived_digest FROM runtime_observation_archive WHERE id = NEW.id;
  IF FOUND AND archived_digest <> f74a_observation_digest(to_jsonb(NEW), NEW.observed_at) THEN
    RAISE EXCEPTION 'observation % conflicts with immutable archive value', NEW.id;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_hot_archive_identity BEFORE INSERT OR UPDATE ON runtime_observations
FOR EACH ROW EXECUTE FUNCTION f74a_check_hot_against_archive();

CREATE FUNCTION f74a_rehydrate_state_observation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE archived_digest BYTEA;
DECLARE hot_digest BYTEA;
BEGIN
  IF NEW.current_observation_id IS NULL THEN RETURN NEW; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.current_observation_id::text, 7401));
  SELECT row_digest INTO archived_digest FROM runtime_observation_archive WHERE id = NEW.current_observation_id;
  IF NOT FOUND THEN RETURN NEW; END IF;
  INSERT INTO runtime_observations
    (id, service_id, environment_id, deployment_unit_id, observed_image_digest,
     observed_image_repo, observed_container_id, observed_host, observed_version,
     health_status, source, metadata, normalized_state, normalized_hash, observed_at)
  SELECT id, service_id, environment_id, deployment_unit_id, observed_image_digest,
         observed_image_repo, observed_container_id, observed_host, observed_version,
         health_status, source, metadata, normalized_state, normalized_hash, observed_at
  FROM runtime_observation_archive WHERE id = NEW.current_observation_id
  ON CONFLICT (id) DO NOTHING;
  SELECT f74a_observation_digest(to_jsonb(o), o.observed_at) INTO hot_digest
  FROM runtime_observations o WHERE o.id = NEW.current_observation_id;
  IF hot_digest IS DISTINCT FROM archived_digest THEN
    RAISE EXCEPTION 'rehydrated observation % differs from immutable archive', NEW.current_observation_id;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_state_observation_hot BEFORE INSERT OR UPDATE OF current_observation_id
ON environment_service_state FOR EACH ROW EXECUTE FUNCTION f74a_rehydrate_state_observation();

CREATE VIEW runtime_observation_history AS
  SELECT id, service_id, environment_id, deployment_unit_id, observed_image_digest,
    observed_image_repo, observed_container_id, observed_host, observed_version,
    health_status, source, metadata, normalized_state, normalized_hash, observed_at
  FROM runtime_observations
  UNION ALL
  SELECT a.id, a.service_id, a.environment_id, a.deployment_unit_id, a.observed_image_digest,
    a.observed_image_repo, a.observed_container_id, a.observed_host, a.observed_version,
    a.health_status, a.source, a.metadata, a.normalized_state, a.normalized_hash, a.observed_at
  FROM runtime_observation_archive a
  WHERE NOT EXISTS (SELECT 1 FROM runtime_observations h WHERE h.id = a.id);
