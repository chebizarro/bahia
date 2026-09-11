ALTER TABLE contextvm_responses
    ADD COLUMN request_fingerprint TEXT NULL
    CHECK (request_fingerprint IS NULL OR request_fingerprint ~ '^[0-9a-f]{64}$');
