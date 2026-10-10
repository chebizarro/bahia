DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM f74a_confirmed_deletion_batches WHERE deleted_count > 0) THEN
    RAISE EXCEPTION 'cannot roll back F74a confirmed deletion provenance after a hot row was removed';
  END IF;
END $$;
DROP TABLE f74a_confirmed_deletion_batches;
DROP TRIGGER f74a_confirmed_deletion_run_guard ON f74a_confirmed_deletion_runs;
DROP FUNCTION f74a_guard_confirmed_deletion_run();
DROP TABLE f74a_confirmed_deletion_runs;
