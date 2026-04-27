CREATE TABLE IF NOT EXISTS service_secrets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id UUID REFERENCES environments(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    encrypted_value BYTEA NOT NULL,
    encryption_method VARCHAR(20) NOT NULL DEFAULT 'nip44',
    version INT NOT NULL DEFAULT 1,
    created_by VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, environment_id, name)
);

CREATE INDEX idx_service_secrets_service ON service_secrets(service_id);
CREATE INDEX idx_service_secrets_env ON service_secrets(environment_id) WHERE environment_id IS NOT NULL;
