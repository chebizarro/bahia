-- 000013_hiveci_pipeline.down.sql
-- Rolls back Hive-CI workflow ingestion tables and pipeline mapping policy.

DROP INDEX IF EXISTS idx_deployment_intents_hiveci_run_id;

DROP TABLE IF EXISTS hiveci_pipeline_policies;
DROP TABLE IF EXISTS hiveci_workflow_results;
DROP TABLE IF EXISTS hiveci_workflow_runs;
