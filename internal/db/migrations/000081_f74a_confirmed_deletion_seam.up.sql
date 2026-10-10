-- A restartable cursor for deleting only hot rows already preserved in the
-- immutable F74a archive. No command enables this run without an independent
-- live backup admission provider.
CREATE TABLE f74a_confirmed_deletion_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  receipt_id UUID NOT NULL,
  receipt_sha256 TEXT NOT NULL CHECK (receipt_sha256 ~ '^[0-9a-f]{64}$'),
  source_database_name TEXT NOT NULL,
  source_database_oid TEXT NOT NULL,
  source_system_identifier TEXT NOT NULL,
  backup_object_sha256 TEXT NOT NULL CHECK (backup_object_sha256 ~ '^[0-9a-f]{64}$'),
  cutoff TIMESTAMPTZ NOT NULL,
  batch_size INTEGER NOT NULL CHECK (batch_size BETWEEN 1 AND 250),
  cursor_service_id UUID,
  cursor_environment_id UUID,
  cursor_observed_at TIMESTAMPTZ,
  cursor_id UUID,
  examined_count BIGINT NOT NULL DEFAULT 0,
  deleted_count BIGINT NOT NULL DEFAULT 0,
  complete BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((cursor_service_id IS NULL AND cursor_environment_id IS NULL AND cursor_observed_at IS NULL AND cursor_id IS NULL)
      OR (cursor_service_id IS NOT NULL AND cursor_environment_id IS NOT NULL AND cursor_observed_at IS NOT NULL AND cursor_id IS NOT NULL))
);

CREATE FUNCTION f74a_guard_confirmed_deletion_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.receipt_id <> OLD.receipt_id OR NEW.receipt_sha256 <> OLD.receipt_sha256
     OR NEW.source_database_name <> OLD.source_database_name
     OR NEW.source_database_oid <> OLD.source_database_oid
     OR NEW.source_system_identifier <> OLD.source_system_identifier
     OR NEW.backup_object_sha256 <> OLD.backup_object_sha256
     OR NEW.cutoff <> OLD.cutoff OR NEW.batch_size <> OLD.batch_size
     OR NEW.created_at <> OLD.created_at
     OR NEW.examined_count < OLD.examined_count OR NEW.deleted_count < OLD.deleted_count
     OR (OLD.complete AND NOT NEW.complete)
     OR (OLD.cursor_id IS NOT NULL AND NEW.cursor_id IS NULL)
     OR (OLD.cursor_id IS NOT NULL AND
         (NEW.cursor_service_id,NEW.cursor_environment_id,NEW.cursor_observed_at,NEW.cursor_id)
         < (OLD.cursor_service_id,OLD.cursor_environment_id,OLD.cursor_observed_at,OLD.cursor_id)) THEN
    RAISE EXCEPTION 'F74a confirmed deletion run identity and progress are immutable/monotonic';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_confirmed_deletion_run_guard BEFORE UPDATE ON f74a_confirmed_deletion_runs
FOR EACH ROW EXECUTE FUNCTION f74a_guard_confirmed_deletion_run();

CREATE TABLE f74a_confirmed_deletion_batches (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id UUID NOT NULL REFERENCES f74a_confirmed_deletion_runs(id),
  examined_count INTEGER NOT NULL CHECK (examined_count BETWEEN 0 AND 250),
  deleted_count INTEGER NOT NULL CHECK (deleted_count BETWEEN 0 AND examined_count),
  cursor_service_id UUID NOT NULL,
  cursor_environment_id UUID NOT NULL,
  cursor_observed_at TIMESTAMPTZ NOT NULL,
  cursor_id UUID NOT NULL,
  committed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER f74a_confirmed_deletion_batch_immutable
BEFORE UPDATE OR DELETE ON f74a_confirmed_deletion_batches
FOR EACH ROW EXECUTE FUNCTION f74a_archive_immutable();
