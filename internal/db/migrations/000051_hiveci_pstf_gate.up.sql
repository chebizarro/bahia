-- 000051_hiveci_pstf_gate: persist PSTF gate metadata from Hive-CI 5402 results.

ALTER TABLE hiveci_workflow_results
  ADD COLUMN IF NOT EXISTS pstf_gate_name TEXT,
  ADD COLUMN IF NOT EXISTS pstf_gate_status TEXT;

CREATE INDEX IF NOT EXISTS idx_hiveci_workflow_results_pstf_gate
  ON hiveci_workflow_results(pstf_gate_status)
  WHERE pstf_gate_status IS NOT NULL;
