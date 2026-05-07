-- Package repository control-plane projections.
-- These tables are caches/read models derived from signed Nostr events and backend observations;
-- they are not the authoritative source of desired package state.

CREATE TABLE IF NOT EXISTS package_repositories_projection (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('npm', 'pypi', 'conan', 'deb', 'rpm', 'pub', 'go_modules', 'gradle')),
    backend_ref TEXT NOT NULL,
    backend_type TEXT NOT NULL CHECK (backend_type IN ('nexus', 'pulp', 'filesystem_mock')),
    external_repository_name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    namespace_prefix TEXT NOT NULL DEFAULT '',
    policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    public_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'ready', 'disabled', 'deleting', 'deleted', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    deleted BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_event_id TEXT NOT NULL DEFAULT '',
    last_event_created_at TIMESTAMPTZ NOT NULL DEFAULT to_timestamp(0)
);

CREATE UNIQUE INDEX IF NOT EXISTS package_repositories_projection_name_idx
    ON package_repositories_projection (name);
CREATE INDEX IF NOT EXISTS package_repositories_projection_backend_idx
    ON package_repositories_projection (backend_ref, backend_type);
CREATE INDEX IF NOT EXISTS package_repositories_projection_event_order_idx
    ON package_repositories_projection (last_event_created_at, last_event_id);

CREATE TABLE IF NOT EXISTS package_artifacts_projection (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID NOT NULL REFERENCES package_repositories_projection(id) ON DELETE CASCADE,
    repository_name TEXT NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('npm', 'pypi', 'conan', 'deb', 'rpm', 'pub', 'go_modules', 'gradle')),
    namespace TEXT NOT NULL DEFAULT '',
    package_name TEXT NOT NULL,
    version TEXT NOT NULL,
    filename TEXT NOT NULL,
    source_url TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    download_url TEXT NOT NULL DEFAULT '',
    backend_path TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'available', 'deleting', 'deleted', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    deleted BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_event_id TEXT NOT NULL DEFAULT '',
    last_event_created_at TIMESTAMPTZ NOT NULL DEFAULT to_timestamp(0)
);

CREATE UNIQUE INDEX IF NOT EXISTS package_artifacts_projection_identity_idx
    ON package_artifacts_projection (repository_id, namespace, package_name, version, filename);
CREATE INDEX IF NOT EXISTS package_artifacts_projection_package_idx
    ON package_artifacts_projection (repository_id, package_name, version);
CREATE INDEX IF NOT EXISTS package_artifacts_projection_event_order_idx
    ON package_artifacts_projection (last_event_created_at, last_event_id);

CREATE TABLE IF NOT EXISTS package_publications_projection (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID NOT NULL REFERENCES package_repositories_projection(id) ON DELETE CASCADE,
    artifact_id UUID NOT NULL REFERENCES package_artifacts_projection(id) ON DELETE CASCADE,
    environment TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT '',
    target_repository_id UUID REFERENCES package_repositories_projection(id) ON DELETE SET NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'published', 'promoting', 'promoted', 'rejected', 'rolled_back', 'failed')),
    policy_decision TEXT NOT NULL DEFAULT 'unknown' CHECK (policy_decision IN ('unknown', 'allowed', 'denied', 'requires_approval')),
    policy_ref TEXT NOT NULL DEFAULT '',
    approved_by TEXT NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    promoted_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_event_id TEXT NOT NULL DEFAULT '',
    last_event_created_at TIMESTAMPTZ NOT NULL DEFAULT to_timestamp(0)
);

CREATE INDEX IF NOT EXISTS package_publications_projection_artifact_idx
    ON package_publications_projection (artifact_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS package_publications_projection_repository_idx
    ON package_publications_projection (repository_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS package_publications_projection_event_order_idx
    ON package_publications_projection (last_event_created_at, last_event_id);

CREATE TABLE IF NOT EXISTS package_intents_projection (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_event_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation IN ('repository_apply', 'repository_delete', 'artifact_publish', 'artifact_delete', 'promote')),
    repository_id UUID REFERENCES package_repositories_projection(id) ON DELETE SET NULL,
    repository_name TEXT NOT NULL DEFAULT '',
    artifact_id UUID REFERENCES package_artifacts_projection(id) ON DELETE SET NULL,
    namespace TEXT NOT NULL DEFAULT '',
    package_name TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    filename TEXT NOT NULL DEFAULT '',
    requester_pubkey TEXT NOT NULL DEFAULT '',
    request_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL CHECK (status IN ('accepted', 'executing', 'succeeded', 'failed', 'superseded')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    last_status_event_id TEXT NOT NULL DEFAULT '',
    last_result_event_id TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS package_intents_projection_request_event_idx
    ON package_intents_projection (request_event_id);
CREATE INDEX IF NOT EXISTS package_intents_projection_status_idx
    ON package_intents_projection (status, created_at);
CREATE INDEX IF NOT EXISTS package_intents_projection_repository_idx
    ON package_intents_projection (repository_id, created_at DESC);
