DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM runtime_observation_archive LIMIT 1) THEN
    RAISE EXCEPTION 'cannot drop nonempty runtime observation archive';
  END IF;
END $$;
DROP VIEW runtime_observation_history;
DROP TRIGGER f74a_state_observation_hot ON environment_service_state;
DROP FUNCTION f74a_rehydrate_state_observation();
DROP TRIGGER f74a_hot_archive_identity ON runtime_observations;
DROP FUNCTION f74a_check_hot_against_archive();
DROP TRIGGER f74a_archive_insert_identity ON runtime_observation_archive;
DROP FUNCTION f74a_check_archive_insert();
DROP TRIGGER f74a_archive_no_update ON runtime_observation_archive;
DROP TRIGGER f74a_archive_batch_immutable ON f74a_observation_archive_batches;
DROP FUNCTION f74a_archive_immutable();
DROP TABLE runtime_observation_archive;
DROP TABLE f74a_observation_archive_batches;
DROP TABLE f74a_observation_compaction_runs;
DROP FUNCTION f74a_guard_archive_run();
DROP INDEX idx_runtime_observations_ordered;
DROP FUNCTION f74a_observation_digest(JSONB, TIMESTAMPTZ);
DROP FUNCTION f74a_lock_observation_coordinate(UUID, UUID);
