-- scripts/seed_hiveci_pipeline_policy.sql
--
-- Seeds the hiveci_pipeline_policies row that maps the Bahia GitHub repo
-- to its Bahia service/environment deployment target.
--
-- Background
-- ----------
-- Bahia's pipeline bridge calls GetPolicyByRepoAndWorkflow(repo_coordinate,
-- workflow_path) for every ingested kind-5402 result event.  If no matching
-- row exists the bridge exits silently with "no pipeline policy match;
-- skipping" and no artifact is ever registered.
--
-- How to run
-- ----------
-- From the edge-01 host, pipe this file into the running postgres container:
--
--   REPO_COORD="30617:<grasp-gitea-pubkey>:chebizarro/bahia"
--
--   sed "s|:REPO_COORDINATE:|$REPO_COORD|g" \
--     scripts/seed_hiveci_pipeline_policy.sql \
--     | docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
--         exec -T postgres psql -U bahia -d bahia
--
-- The only value you need to supply is the NIP-34 repo coordinate that
-- grasp-gitea is emitting in its kind-5401 "a" tag.  Use the discovery
-- query below to find it once 5401 events have been ingested.
--
-- Idempotency
-- -----------
-- The INSERT uses ON CONFLICT DO NOTHING.  Running this script multiple
-- times is safe.
--
-- ============================================================
-- Step 0: discover the repo coordinate already seen in 5401 events
-- ============================================================
SELECT
    repo_coordinate,
    workflow_path,
    count(*)        AS run_count,
    max(event_created_at) AS last_seen
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
-- Step 3: insert the policy row
--
-- Replace :REPO_COORDINATE: with the value from Step 0, or use
-- the sed substitution shown in "How to run" above.
-- ============================================================
INSERT INTO hiveci_pipeline_policies
    (repo_coordinate, workflow_path, branch_pattern,
     service_id, environment_id, enabled, metadata)
SELECT
    ':REPO_COORDINATE:',
    '.github/workflows/hive-ci-build.yml',
    NULL,           -- NULL matches any branch
    s.id,
    e.id,
    TRUE,
    '{}'::jsonb     -- set auto_deploy_staging later (see Step 4)
FROM services     s,
     environments e
WHERE s.name = 'bahia'
  AND e.name = 'edge-01'
ON CONFLICT DO NOTHING
RETURNING id, repo_coordinate, workflow_path, service_id, environment_id, enabled, metadata;

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
