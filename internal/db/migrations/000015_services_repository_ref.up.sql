-- Add nullable JSONB column for structured repository metadata
ALTER TABLE services
  ADD COLUMN IF NOT EXISTS repository JSONB;

-- Backfill legacy rows with repo_url into repository object
UPDATE services
SET repository = jsonb_build_object(
  'source', 'manual',
  'clone_url', repo_url
)
WHERE (repo_url IS NOT NULL AND repo_url <> '')
  AND repository IS NULL;
