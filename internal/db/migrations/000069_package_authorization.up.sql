-- Local authority: never rebuild these tables from relay projections.
CREATE TABLE package_request_claims (
    requester TEXT NOT NULL,
    method TEXT NOT NULL,
    token TEXT NOT NULL CHECK (token <> ''),
    event_id TEXT NOT NULL UNIQUE,
    fingerprint TEXT NOT NULL,
    completed BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (requester, method, token)
);

CREATE TABLE package_approvals (
    id UUID PRIMARY KEY,
    requester TEXT NOT NULL,
    approver TEXT NOT NULL CHECK (approver <> requester),
    method TEXT NOT NULL CHECK (method IN ('package/publish', 'package/promote')),
    plan_hash TEXT NOT NULL,
    event_id TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
    consumed_at TIMESTAMPTZ
);
