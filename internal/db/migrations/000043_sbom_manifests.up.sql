-- Subject-neutral SBOM manifest projections. Canonical truth remains Nostr 30078/30004 events.
CREATE TABLE IF NOT EXISTS sbom_manifests (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type          TEXT NOT NULL CHECK (subject_type IN ('artifact', 'deployment', 'package', 'repository')),
    subject_id            TEXT NOT NULL,
    subject_name          TEXT,
    subject_digest        TEXT NOT NULL,
    format                TEXT NOT NULL CHECK (format IN ('spdx', 'cyclonedx')),
    media_type            TEXT,
    storage_type          TEXT NOT NULL CHECK (storage_type IN ('blossom', 'oci-referrer', 'package-backend')),
    storage_uri           TEXT NOT NULL,
    payload_sha256        TEXT NOT NULL,
    generator_id          TEXT NOT NULL,
    generator_version     TEXT,
    generator_pubkey      TEXT,
    package_count         INT NOT NULL DEFAULT 0,
    vulnerability_count   INT NOT NULL DEFAULT 0,
    critical_count        INT NOT NULL DEFAULT 0,
    high_count            INT NOT NULL DEFAULT 0,
    ntia_status           TEXT CHECK (ntia_status IS NULL OR ntia_status IN ('compliant', 'partial', 'unknown')),
    ntia_metadata         JSONB DEFAULT '{}',
    reference_event_id    TEXT,
    reference_d_tag       TEXT,
    availability_event_id TEXT,
    availability_d_tag    TEXT,
    publish_state         TEXT NOT NULL DEFAULT 'draft' CHECK (publish_state IN ('draft', 'published', 'failed')),
    publish_error         TEXT,
    source_kind           TEXT NOT NULL CHECK (source_kind IN ('generated', 'imported', 'external')),
    metadata              JSONB DEFAULT '{}',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at          TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_sbom_manifests_dedupe
    ON sbom_manifests(subject_type, subject_id, subject_digest, format, generator_id, payload_sha256);
CREATE INDEX idx_sbom_manifests_subject
    ON sbom_manifests(subject_type, subject_id, subject_digest, created_at DESC);
CREATE INDEX idx_sbom_manifests_publish_state
    ON sbom_manifests(publish_state);
CREATE INDEX idx_sbom_manifests_payload_sha256
    ON sbom_manifests(payload_sha256);

CREATE TABLE IF NOT EXISTS sbom_manifest_packages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    manifest_id UUID NOT NULL REFERENCES sbom_manifests(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    version     TEXT NOT NULL DEFAULT '',
    ecosystem   TEXT,
    license     TEXT,
    purl        TEXT,
    cpe         TEXT
);

CREATE UNIQUE INDEX idx_sbom_manifest_packages_dedupe
    ON sbom_manifest_packages(manifest_id, name, version, COALESCE(ecosystem, ''), COALESCE(purl, ''), COALESCE(cpe, ''));
CREATE INDEX idx_sbom_manifest_packages_manifest ON sbom_manifest_packages(manifest_id);
CREATE INDEX idx_sbom_manifest_packages_name ON sbom_manifest_packages(name);
CREATE INDEX idx_sbom_manifest_packages_purl ON sbom_manifest_packages(purl) WHERE purl IS NOT NULL;
