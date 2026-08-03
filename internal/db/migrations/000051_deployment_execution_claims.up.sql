-- A deployment intent may have at most one active execution claim.
-- Terminal runs remain historical and do not block an explicit later retry.
CREATE UNIQUE INDEX idx_deployment_runs_active_intent
  ON deployment_runs(deployment_intent_id)
  WHERE status IN ('queued', 'running');
