-- SBOM records for artifact supply-chain analysis.
CREATE TABLE IF NOT EXISTS artifact_sboms (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_id          UUID NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    format               TEXT NOT NULL CHECK (format IN ('spdx', 'cyclonedx')),
    source_url           TEXT,
    package_count        INT NOT NULL DEFAULT 0,
    vulnerability_count  INT NOT NULL DEFAULT 0,
    critical_count       INT NOT NULL DEFAULT 0,
    high_count           INT NOT NULL DEFAULT 0,
    raw_hash             TEXT,
    metadata             JSONB DEFAULT '{}',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_artifact_sboms_artifact ON artifact_sboms(artifact_id);
CREATE UNIQUE INDEX idx_artifact_sboms_raw_hash ON artifact_sboms(raw_hash) WHERE raw_hash IS NOT NULL;

-- Individual packages within SBOMs.
CREATE TABLE IF NOT EXISTS sbom_packages (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sbom_id   UUID NOT NULL REFERENCES artifact_sboms(id) ON DELETE CASCADE,
    name      TEXT NOT NULL,
    version   TEXT NOT NULL DEFAULT '',
    ecosystem TEXT,
    license   TEXT,
    purl      TEXT,
    cpe       TEXT
);

CREATE INDEX idx_sbom_packages_sbom ON sbom_packages(sbom_id);
CREATE INDEX idx_sbom_packages_name ON sbom_packages(name);
CREATE INDEX idx_sbom_packages_purl ON sbom_packages(purl) WHERE purl IS NOT NULL;
