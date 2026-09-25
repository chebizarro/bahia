-- Local idempotency authority. Never rebuild or discard from relay projections.
CREATE TABLE hiveci_initiations (
    source_event_id TEXT PRIMARY KEY CHECK (source_event_id <> ''),
    build_id UUID NOT NULL UNIQUE,
    stage TEXT NOT NULL CHECK (stage IN (
        'claimed', 'request_ready', 'request_unconfirmed', 'request_published',
        'job_unconfirmed', 'job_published', 'evidence_ready',
        'evidence_unconfirmed', 'evidence_published'
    )),
    document BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
