CREATE TABLE service_secret_data_keys (
    id UUID PRIMARY KEY,
    service_pubkey CHAR(64) NOT NULL,
    wrapped_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(wrapped_key) BETWEEN 132 AND 16384)
);
