CREATE TABLE IF NOT EXISTS oci_repositories (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    namespace TEXT,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oci_manifests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID NOT NULL REFERENCES oci_repositories(id) ON DELETE CASCADE,
    digest TEXT NOT NULL,
    media_type TEXT NOT NULL,
    artifact_type TEXT,
    subject_digest TEXT,
    content BYTEA NOT NULL,
    size_bytes BIGINT NOT NULL,
    annotations JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repository_id, digest)
);

CREATE TABLE IF NOT EXISTS oci_tags (
    repository_id UUID NOT NULL REFERENCES oci_repositories(id) ON DELETE CASCADE,
    tag TEXT NOT NULL,
    manifest_id UUID NOT NULL REFERENCES oci_manifests(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repository_id, tag)
);

CREATE TABLE IF NOT EXISTS oci_blobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    digest TEXT NOT NULL UNIQUE,
    media_type TEXT,
    size_bytes BIGINT NOT NULL,
    storage_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oci_repo_blobs (
    repository_id UUID NOT NULL REFERENCES oci_repositories(id) ON DELETE CASCADE,
    blob_id UUID NOT NULL REFERENCES oci_blobs(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repository_id, blob_id)
);

CREATE TABLE IF NOT EXISTS oci_blob_uploads (
    upload_id UUID PRIMARY KEY,
    repository_id UUID NOT NULL REFERENCES oci_repositories(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'pending',
    spool_path TEXT NOT NULL,
    offset_bytes BIGINT NOT NULL DEFAULT 0,
    digest TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_oci_repositories_name ON oci_repositories(name);
CREATE INDEX idx_oci_manifests_repo_digest ON oci_manifests(repository_id, digest);
CREATE INDEX idx_oci_manifests_subject_digest ON oci_manifests(subject_digest) WHERE subject_digest IS NOT NULL;
CREATE INDEX idx_oci_tags_manifest_id ON oci_tags(manifest_id);
CREATE INDEX idx_oci_blobs_digest ON oci_blobs(digest);
CREATE INDEX idx_oci_repo_blobs_blob_id ON oci_repo_blobs(blob_id);
CREATE INDEX idx_oci_blob_uploads_repository_id ON oci_blob_uploads(repository_id);
CREATE INDEX idx_oci_blob_uploads_state ON oci_blob_uploads(state);
CREATE INDEX idx_oci_blob_uploads_expires_at ON oci_blob_uploads(expires_at);
