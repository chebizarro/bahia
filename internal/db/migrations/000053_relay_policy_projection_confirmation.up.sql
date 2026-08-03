ALTER TABLE relay_policy_projections
  ADD COLUMN IF NOT EXISTS relay_confirmed_at TIMESTAMPTZ;

UPDATE relay_policy_projections
SET relay_confirmed_at = event_accepted_at
WHERE relay_confirmed_at IS NULL;
