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
--         workflow_path: ".gitea/workflows/release.yml"
--         service_name: bahia
--         environment_name: edge-01
--         metadata:
--           workflow_digest: "<sha256-hex>"
--           policy_digest: "<signed-policy-digest>"
--           review_policy: "<signed-review-policy>"
--           source_repo_identity: "<gitea-host/org/repo>"
--           release_image_repository: "<harbor-host/project/repo>"
--           release_attestors: ["<release-attestor-pubkey>"]
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
--   WORKFLOW_DIGEST="<sha256-hex-from-5401>"
--   POLICY_DIGEST="<policy-digest-from-5401>"
--   REVIEW_POLICY="<review-policy-from-5401>"
--   SOURCE_REPO="<gitea-host/org/repo>"
--   IMAGE_REPO="<harbor-host/project/repo>"
--   RELEASE_ATTESTOR="<release-attestor-pubkey>"
--   PREVIOUS_DIGEST="sha256:<currently-staged-manifest>"
--   HEALTH_PATH="/health"
--   READINESS_PATH="/ready"
--
--   sed -e "s|:REPO_COORDINATE:|$REPO_COORD|g" \
--       -e "s|:SERVICE_NAME:|$SERVICE_NAME|g" \
--       -e "s|:ENV_NAME:|$ENV_NAME|g" \
--       -e "s|:WORKFLOW_DIGEST:|$WORKFLOW_DIGEST|g" \
--       -e "s|:POLICY_DIGEST:|$POLICY_DIGEST|g" \
--       -e "s|:REVIEW_POLICY:|$REVIEW_POLICY|g" \
--       -e "s|:SOURCE_REPO:|$SOURCE_REPO|g" \
--       -e "s|:IMAGE_REPO:|$IMAGE_REPO|g" \
--       -e "s|:RELEASE_ATTESTOR:|$RELEASE_ATTESTOR|g" \
--       -e "s|:PREVIOUS_DIGEST:|$PREVIOUS_DIGEST|g" \
--       -e "s|:HEALTH_PATH:|$HEALTH_PATH|g" \
--       -e "s|:READINESS_PATH:|$READINESS_PATH|g" \
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
-- Step 3: reconcile or insert the policy row (idempotent)
--
-- Placeholders:
--   :REPO_COORDINATE:  — NIP-34 repo coordinate from Step 0
--   :SERVICE_NAME:     — Bahia service name (default: bahia)
--   :ENV_NAME:         — Target environment name (default: edge-01)
--   :WORKFLOW_DIGEST:   — Exact workflow digest from signed kind-5401
--   :POLICY_DIGEST:     — Exact repository policy digest from signed kind-5401
--   :REVIEW_POLICY:     — Exact review policy from signed kind-5401
--   :SOURCE_REPO:       — Exact source repository identity from signed kind-5401
--   :IMAGE_REPO:        — Fully-qualified Harbor release repository
--   :RELEASE_ATTESTOR:  — Authorized release-attestor pubkey
--   :PREVIOUS_DIGEST:   — Immutable manifest allowed as rollback predecessor
--   :HEALTH_PATH:       — Concrete HTTP health endpoint path
--   :READINESS_PATH:    — Concrete HTTP readiness endpoint path
-- ============================================================
WITH resolved_target AS (
    SELECT
        s.id AS service_id,
        e.id AS environment_id,
        jsonb_build_object(
            'workflow_digest', ':WORKFLOW_DIGEST:',
            'policy_digest', ':POLICY_DIGEST:',
            'review_policy', ':REVIEW_POLICY:',
            'source_repo_identity', ':SOURCE_REPO:',
            'release_image_repository', ':IMAGE_REPO:',
            'release_attestors', jsonb_build_array(':RELEASE_ATTESTOR:'),
            'rollback_compatibility', jsonb_build_object(
                'compatible_from_digests', jsonb_build_array(':PREVIOUS_DIGEST:')
            ),
            'health_contract', jsonb_build_object(
                'type', 'http', 'path', ':HEALTH_PATH:', 'timeout_seconds', 10
            ),
            'readiness_contract', jsonb_build_object(
                'type', 'http', 'path', ':READINESS_PATH:', 'timeout_seconds', 15
            )
        ) AS metadata
    FROM services s, environments e
    WHERE s.name = ':SERVICE_NAME:'
      AND e.name = ':ENV_NAME:'
),
reconciled AS (
    UPDATE hiveci_pipeline_policies p
       SET enabled = TRUE,
           metadata = target.metadata,
           updated_at = now()
      FROM resolved_target target
     WHERE p.repo_coordinate = ':REPO_COORDINATE:'
       AND p.workflow_path = '.gitea/workflows/release.yml'
       AND COALESCE(p.branch_pattern, '') = ''
       AND p.service_id = target.service_id
       AND p.environment_id = target.environment_id
    RETURNING p.id
)
INSERT INTO hiveci_pipeline_policies
    (repo_coordinate, workflow_path, branch_pattern,
     service_id, environment_id, enabled, metadata)
SELECT
    ':REPO_COORDINATE:',
    '.gitea/workflows/release.yml',
    NULL,
    target.service_id,
    target.environment_id,
    TRUE,
    target.metadata
FROM resolved_target target
WHERE NOT EXISTS (SELECT 1 FROM reconciled)
  AND NOT EXISTS (
      SELECT 1
      FROM hiveci_pipeline_policies p
      WHERE p.repo_coordinate = ':REPO_COORDINATE:'
        AND p.workflow_path = '.gitea/workflows/release.yml'
        AND COALESCE(p.branch_pattern, '') = ''
        AND p.service_id = target.service_id
        AND p.environment_id = target.environment_id
  )
RETURNING id, repo_coordinate, workflow_path, service_id, environment_id, enabled;

-- CI policies bind accepted release evidence to a Bahia service and staged
-- environment only. They never authorize deployment. Promotion requires a
-- separately signed ContextVM service/deploy mutation using bahia.deploy.v2.
