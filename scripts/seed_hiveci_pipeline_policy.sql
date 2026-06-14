-- scripts/seed_hiveci_pipeline_policy.sql
--
-- Operator tool: seed or inspect hiveci_pipeline_policies rows.
--
-- This script is the manual/operator complement to the config-driven
-- policy seeding that runs at Bahia startup when hiveci.policies is
-- configured.  Use it for:
--
--   1. Bootstrapping a policy before the app config is deployed
--   2. Inspecting existing policies and workflow runs
--   3. Ad-hoc policy creation outside the config lifecycle
--
-- The config-driven path (preferred):
-- ------------------------------------
-- Add to bahia.yml (or equivalent env vars):
--
--   hiveci:
--     enabled: true
--     policies:
--       - repo_coordinate: "30617:<pubkey>:chebizarro/bahia"
--         workflow_path: ".github/workflows/hive-ci-build.yml"
--         service_name: bahia
--         environment_name: edge-01
--         metadata: {}
--
-- Bahia resolves service/environment by name and idempotently ensures
-- the policy row exists on every startup.
--
-- Manual path (this script):
-- --------------------------
-- From the edge-01 host:
--
--   REPO_COORD="30617:<grasp-gitea-pubkey>:chebizarro/bahia"
--   SERVICE_NAME="bahia"
--   ENV_NAME="edge-01"
--
--   sed -e "s|:REPO_COORDINATE:|$REPO_COORD|g" \
--       -e "s|:SERVICE_NAME:|$SERVICE_NAME|g" \
--       -e "s|:ENV_NAME:|$ENV_NAME|g" \
--     scripts/seed_hiveci_pipeline_policy.sql \
--     | docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
--         exec -T postgres psql -U bahia -d bahia
--
-- All parameters are substituted via sed; no psql \set variables needed.
--
-- Idempotency: INSERT uses a NOT EXISTS guard with COALESCE on
-- branch_pattern to handle NULLs.  Safe to run multiple times.

-- ============================================================
-- Step 0: discover repo coordinates from ingested 5401 events
-- ============================================================
SELECT
    repo_coordinate,
    workflow_path,
    count(*)                AS run_count,
    max(event_created_at)   AS last_seen
FROM hiveci_workflow_runs
GROUP BY repo_coordinate, workflow_path
ORDER BY last_seen DESC;

-- ============================================================
-- Step 1: inspect available services and environments
-- ============================================================
SELECT id, name, artifact_repo FROM services ORDER BY name;
SELECT id, name, protected     FROM environments ORDER BY name;

-- ============================================================
-- Step 2: inspect existing policies
-- ============================================================
SELECT
    p.id,
    p.repo_coordinate,
    p.workflow_path,
    p.branch_pattern,
    p.enabled,
    p.metadata,
    s.name AS service_name,
    e.name AS environment_name,
    p.created_at
FROM hiveci_pipeline_policies p
JOIN services     s ON s.id = p.service_id
JOIN environments e ON e.id = p.environment_id
ORDER BY p.created_at;

-- ============================================================
-- Step 3: insert the policy row (idempotent)
--
-- Placeholders:
--   :REPO_COORDINATE:  — NIP-34 repo coordinate from Step 0
--   :SERVICE_NAME:     — Bahia service name (default: bahia)
--   :ENV_NAME:         — Target environment name (default: edge-01)
-- ============================================================
INSERT INTO hiveci_pipeline_policies
    (repo_coordinate, workflow_path, branch_pattern,
     service_id, environment_id, enabled, metadata)
SELECT
    ':REPO_COORDINATE:',
    '.github/workflows/hive-ci-build.yml',
    NULL,
    s.id,
    e.id,
    TRUE,
    '{}'::jsonb
FROM services     s,
     environments e
WHERE s.name = ':SERVICE_NAME:'
  AND e.name = ':ENV_NAME:'
  AND NOT EXISTS (
      SELECT 1 FROM hiveci_pipeline_policies p
      WHERE p.repo_coordinate = ':REPO_COORDINATE:'
        AND p.workflow_path   = '.github/workflows/hive-ci-build.yml'
        AND COALESCE(p.branch_pattern, '') = ''
        AND p.service_id  = s.id
        AND p.environment_id = e.id
  )
RETURNING id, repo_coordinate, workflow_path, service_id, environment_id, enabled;

-- ============================================================
-- Step 4 (later): enable auto-deploy after artifact registration
--         is verified stable (runbook implementation order step 8)
--
-- UPDATE hiveci_pipeline_policies
-- SET    metadata = '{"auto_deploy_staging": true, "staging_environment": "edge-01"}'::jsonb,
--        updated_at = now()
-- WHERE  repo_coordinate = ':REPO_COORDINATE:'
--   AND  workflow_path   = '.github/workflows/hive-ci-build.yml';
-- ============================================================
