-- Index to optimize latest-run-per-repo lookup
CREATE INDEX IF NOT EXISTS idx_hiveci_workflow_runs_repo_created_at
  ON hiveci_workflow_runs (repo_coordinate, event_created_at DESC, created_at DESC);
