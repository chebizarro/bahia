-- Rollback is refused while archived duplicate selectors remain: restoring the
-- former global uniqueness would erase historical release FK targets.
DROP INDEX hiveci_pipeline_policies_live_selector_key;
ALTER TABLE hiveci_pipeline_policies
  ADD CONSTRAINT hiveci_pipeline_policies_selector_key
  UNIQUE (repo_coordinate, workflow_path, branch_pattern, service_id, environment_id);
ALTER TABLE hiveci_pipeline_policies DROP COLUMN archived_at;
