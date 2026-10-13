-- A policy index row is retained as historical release evidence after its
-- canonical ID is replaced or tombstoned. Only live heads own selectors.
ALTER TABLE hiveci_pipeline_policies ADD COLUMN archived_at TIMESTAMPTZ;
DO $$
DECLARE legacy_unique TEXT;
BEGIN
  SELECT conname INTO legacy_unique FROM pg_constraint
    WHERE conrelid = 'hiveci_pipeline_policies'::regclass AND contype = 'u'
      AND pg_get_constraintdef(oid) LIKE 'UNIQUE (repo_coordinate, workflow_path, branch_pattern, service_id, environment_id)%';
  IF legacy_unique IS NULL THEN
    RAISE EXCEPTION 'hiveci policy selector uniqueness constraint is missing';
  END IF;
  EXECUTE format('ALTER TABLE hiveci_pipeline_policies DROP CONSTRAINT %I', legacy_unique);
END $$;
CREATE UNIQUE INDEX hiveci_pipeline_policies_live_selector_key
  ON hiveci_pipeline_policies (repo_coordinate, workflow_path, branch_pattern, service_id, environment_id)
  WHERE archived_at IS NULL;
