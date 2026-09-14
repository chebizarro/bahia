CREATE TABLE agent_runtime_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository TEXT NOT NULL,
    branch TEXT NOT NULL,
    release_channel TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, repository, branch, release_channel),
    UNIQUE (id, org_id),
    CHECK (btrim(repository) <> ''),
    CHECK (btrim(branch) <> ''),
    CHECK (btrim(release_channel) <> '')
);

CREATE TABLE agent_runtime_releases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    image_repo TEXT NOT NULL,
    image_digest TEXT NOT NULL,
    provenance JSONB NOT NULL,
    verified_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, image_repo, image_digest),
    UNIQUE (id, org_id),
    FOREIGN KEY (source_id, org_id) REFERENCES agent_runtime_sources(id, org_id) ON DELETE RESTRICT,
    CHECK (image_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(provenance) = 'object'),
    CHECK (btrim(provenance->>'provider') <> ''),
    CHECK (btrim(provenance->>'release_event_id') <> ''),
    CHECK (btrim(provenance->>'workflow_run_event_id') <> ''),
    CHECK ((provenance->>'manifest_digest') = image_digest),
    CHECK ((provenance->>'sbom_digest') ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((provenance->>'provenance_digest') ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (btrim(provenance->>'attestor_pubkey') <> '')
);

CREATE INDEX agent_runtime_releases_source_idx ON agent_runtime_releases(source_id, created_at DESC);

CREATE TABLE agent_service_runtime_release_bindings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL,
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    release_id UUID NOT NULL,
    release_channel TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    previous_binding_id UUID REFERENCES agent_service_runtime_release_bindings(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, agent_id, service_id, release_channel, release_id),
    UNIQUE (source_event_id),
    FOREIGN KEY (release_id, org_id) REFERENCES agent_runtime_releases(id, org_id) ON DELETE RESTRICT,
    CHECK (btrim(agent_id) <> ''),
    CHECK (btrim(release_channel) <> ''),
    CHECK (btrim(source_event_id) <> '')
);

CREATE INDEX agent_service_runtime_release_history_idx
    ON agent_service_runtime_release_bindings(org_id, agent_id, service_id, release_channel, created_at DESC, id DESC);
