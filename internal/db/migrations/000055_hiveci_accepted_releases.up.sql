CREATE TABLE hiveci_accepted_releases (
    release_identity TEXT PRIMARY KEY,
    result_event_id TEXT NOT NULL UNIQUE,
    content_digest TEXT NOT NULL,
    attestor_pubkey TEXT NOT NULL,
    workflow_run_event_id TEXT NOT NULL,
    workflow_path TEXT NOT NULL,
    branch TEXT NOT NULL,
    policy_id UUID REFERENCES hiveci_pipeline_policies(id) ON DELETE RESTRICT,
    manifest_digest TEXT NOT NULL,
    sbom_digest TEXT NOT NULL,
    provenance_digest TEXT NOT NULL,
    result_json JSONB NOT NULL,
    signed_event_json JSONB NOT NULL,
    accepted_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (release_identity ~ '^hiveci-release:v1:[0-9a-f]{64}$'),
    CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (sbom_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (provenance_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE INDEX idx_hiveci_accepted_releases_run
    ON hiveci_accepted_releases(workflow_run_event_id);

CREATE TABLE hiveci_release_conflicts (
    id BIGSERIAL PRIMARY KEY,
    release_identity TEXT NOT NULL REFERENCES hiveci_accepted_releases(release_identity) ON DELETE RESTRICT,
    accepted_content_digest TEXT NOT NULL,
    conflicting_content_digest TEXT NOT NULL,
    result_event_id TEXT NOT NULL,
    signed_event_json JSONB NOT NULL,
    quarantined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (release_identity, conflicting_content_digest)
);
