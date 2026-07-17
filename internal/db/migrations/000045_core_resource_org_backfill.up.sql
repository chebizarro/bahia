-- Repair the single-organization ownership backfill skipped by migration 21.
-- Migration 21 ran before migration 22 created organizations, so upgraded
-- databases need this additive data repair after the tenant schema exists.
WITH single_org AS (
    SELECT id
    FROM organizations
    ORDER BY created_at, id
    LIMIT 1
), org_count AS (
    SELECT count(*) AS n
    FROM organizations
)
UPDATE services
SET org_id = (SELECT id FROM single_org)
WHERE org_id IS NULL
  AND (SELECT n FROM org_count) = 1;

WITH single_org AS (
    SELECT id
    FROM organizations
    ORDER BY created_at, id
    LIMIT 1
), org_count AS (
    SELECT count(*) AS n
    FROM organizations
)
UPDATE environments
SET org_id = (SELECT id FROM single_org)
WHERE org_id IS NULL
  AND (SELECT n FROM org_count) = 1;
