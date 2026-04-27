-- Artifact signature records for supply-chain verification.
CREATE TABLE IF NOT EXISTS artifact_signatures (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_id     UUID NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    signer_identity TEXT NOT NULL,
    signature_type  TEXT NOT NULL CHECK (signature_type IN ('cosign', 'nostr', 'sigstore', 'notary')),
    signature_ref   TEXT NOT NULL,
    verified        BOOLEAN NOT NULL DEFAULT false,
    verified_at     TIMESTAMPTZ,
    verification_error TEXT,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_artifact_signatures_artifact ON artifact_signatures(artifact_id);
CREATE INDEX idx_artifact_signatures_signer ON artifact_signatures(signer_identity);
CREATE INDEX idx_artifact_signatures_type ON artifact_signatures(signature_type);
