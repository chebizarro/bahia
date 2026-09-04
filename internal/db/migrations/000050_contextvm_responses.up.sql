CREATE TABLE contextvm_responses (
    requester_pubkey TEXT NOT NULL,
    method TEXT NOT NULL,
    progress_token TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (requester_pubkey, method, progress_token)
);

CREATE INDEX idx_contextvm_responses_created_at ON contextvm_responses (created_at);
