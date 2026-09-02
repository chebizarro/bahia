-- Reverse 000051_hiveci_pstf_gate.

DROP INDEX IF EXISTS idx_hiveci_workflow_results_pstf_gate;

ALTER TABLE hiveci_workflow_results
  DROP COLUMN IF EXISTS pstf_gate_status,
  DROP COLUMN IF EXISTS pstf_gate_name;
