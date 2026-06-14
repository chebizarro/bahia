-- Durable Security OSV persistence. Canonical public truth remains Nostr observables;
-- these tables provide idempotency, retry, and read-model state for Security scans.
CREATE TABLE IF NOT EXISTS security_scan_targets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_type     TEXT NOT NULL CHECK (target_type IN ('sbom', 'package', 'purl', 'commit')),
    target_key      TEXT NOT NULL,
    target_key_hash TEXT NOT NULL,
    display         TEXT NOT NULL,
    subject         JSONB,
    package         JSONB,
    purl            TEXT,
    repository_url  TEXT,
    commit_hash     TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_security_scan_targets_key ON security_scan_targets(target_key);
CREATE UNIQUE INDEX idx_security_scan_targets_hash ON security_scan_targets(target_key_hash);
CREATE INDEX idx_security_scan_targets_type ON security_scan_targets(target_type);

CREATE TABLE IF NOT EXISTS security_scan_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_id           UUID NOT NULL REFERENCES security_scan_targets(id) ON DELETE RESTRICT,
    target_key_hash     TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('accepted', 'running', 'completed', 'failed', 'cancelled')),
    trigger_kind        TEXT NOT NULL CHECK (trigger_kind IN ('sbom_observable', 'manual', 'scheduled', 'policy')),
    requested_by        TEXT,
    request_event_id    TEXT,
    request_d_tag       TEXT,
    osv_query_count     INT NOT NULL DEFAULT 0,
    finding_count       INT NOT NULL DEFAULT 0,
    critical_count      INT NOT NULL DEFAULT 0,
    high_count          INT NOT NULL DEFAULT 0,
    moderate_count      INT NOT NULL DEFAULT 0,
    low_count           INT NOT NULL DEFAULT 0,
    unknown_count       INT NOT NULL DEFAULT 0,
    unsupported_count   INT NOT NULL DEFAULT 0,
    unsupported_reasons JSONB NOT NULL DEFAULT '{}',
    publish_state       TEXT NOT NULL DEFAULT 'pending' CHECK (publish_state IN ('pending', 'published', 'failed_retryable', 'failed_terminal')),
    error               TEXT,
    metadata            JSONB NOT NULL DEFAULT '{}',
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_security_scan_runs_target_created ON security_scan_runs(target_key_hash, created_at DESC);
CREATE INDEX idx_security_scan_runs_status ON security_scan_runs(status);
CREATE UNIQUE INDEX idx_security_scan_runs_active_target
    ON security_scan_runs(target_key_hash)
    WHERE status IN ('accepted', 'running');

CREATE TABLE IF NOT EXISTS security_target_latest (
    target_key_hash TEXT PRIMARY KEY,
    target_id       UUID NOT NULL REFERENCES security_scan_targets(id) ON DELETE RESTRICT,
    run_id          UUID NOT NULL REFERENCES security_scan_runs(id) ON DELETE RESTRICT,
    status          TEXT NOT NULL CHECK (status IN ('accepted', 'running', 'completed', 'failed', 'cancelled')),
    finding_count   INT NOT NULL DEFAULT 0,
    critical_count  INT NOT NULL DEFAULT 0,
    high_count      INT NOT NULL DEFAULT 0,
    moderate_count  INT NOT NULL DEFAULT 0,
    low_count       INT NOT NULL DEFAULT 0,
    unknown_count   INT NOT NULL DEFAULT 0,
    scanned_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_security_target_latest_status ON security_target_latest(status);

CREATE TABLE IF NOT EXISTS security_findings (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id           UUID NOT NULL REFERENCES security_scan_runs(id) ON DELETE CASCADE,
    target_key_hash  TEXT NOT NULL,
    finding_key      TEXT NOT NULL,
    finding_key_hash TEXT NOT NULL,
    osv_id           TEXT NOT NULL,
    cve              TEXT,
    summary          TEXT,
    details          TEXT,
    severity         TEXT NOT NULL CHECK (severity IN ('unknown', 'low', 'moderate', 'high', 'critical')),
    package          JSONB NOT NULL DEFAULT '{}',
    aliases          JSONB NOT NULL DEFAULT '[]',
    references       JSONB NOT NULL DEFAULT '[]',
    withdrawn_at     TIMESTAMPTZ,
    raw_modified     TEXT,
    metadata         JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_security_findings_run_hash ON security_findings(run_id, finding_key_hash);
CREATE INDEX idx_security_findings_target ON security_findings(target_key_hash);
CREATE INDEX idx_security_findings_osv_id ON security_findings(osv_id);
CREATE INDEX idx_security_findings_severity ON security_findings(severity);

CREATE TABLE IF NOT EXISTS security_scan_schedules (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id           UUID NOT NULL,
    target_id           UUID NOT NULL REFERENCES security_scan_targets(id) ON DELETE CASCADE,
    target_key_hash     TEXT NOT NULL,
    enabled             BOOLEAN NOT NULL DEFAULT true,
    interval_seconds    INT NOT NULL CHECK (interval_seconds > 0),
    next_due_at         TIMESTAMPTZ NOT NULL,
    lease_until         TIMESTAMPTZ,
    leased_by           TEXT,
    last_dispatched_at  TIMESTAMPTZ,
    last_run_id         UUID REFERENCES security_scan_runs(id) ON DELETE SET NULL,
    metadata            JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_security_scan_schedules_policy_target ON security_scan_schedules(policy_id, target_key_hash);
CREATE INDEX idx_security_scan_schedules_due ON security_scan_schedules(enabled, next_due_at);
CREATE INDEX idx_security_scan_schedules_lease ON security_scan_schedules(lease_until);

CREATE TABLE IF NOT EXISTS security_policy_breaches (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id            UUID NOT NULL,
    target_key_hash      TEXT NOT NULL,
    fingerprint          TEXT NOT NULL,
    previous_fingerprint TEXT,
    enforcement          TEXT NOT NULL,
    violated_rules       JSONB NOT NULL DEFAULT '[]',
    critical_count       INT NOT NULL DEFAULT 0,
    high_count           INT NOT NULL DEFAULT 0,
    moderate_count       INT NOT NULL DEFAULT 0,
    low_count            INT NOT NULL DEFAULT 0,
    unknown_count        INT NOT NULL DEFAULT 0,
    osv_ids              JSONB NOT NULL DEFAULT '[]',
    notification_status  TEXT NOT NULL CHECK (notification_status IN ('pending', 'dispatched', 'failed', 'suppressed', 'not_required')),
    first_seen_at        TIMESTAMPTZ NOT NULL,
    last_seen_at         TIMESTAMPTZ NOT NULL,
    resolved_at          TIMESTAMPTZ,
    metadata             JSONB NOT NULL DEFAULT '{}',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_security_policy_breaches_active
    ON security_policy_breaches(policy_id, target_key_hash)
    WHERE resolved_at IS NULL;
CREATE INDEX idx_security_policy_breaches_fingerprint ON security_policy_breaches(fingerprint);
CREATE INDEX idx_security_policy_breaches_target ON security_policy_breaches(target_key_hash);

CREATE TABLE IF NOT EXISTS security_osv_vulnerability_cache (
    osv_id       TEXT PRIMARY KEY,
    summary      TEXT,
    severity     TEXT NOT NULL CHECK (severity IN ('unknown', 'low', 'moderate', 'high', 'critical')),
    aliases      JSONB NOT NULL DEFAULT '[]',
    raw          JSONB NOT NULL DEFAULT '{}',
    cached_at    TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    withdrawn_at TIMESTAMPTZ
);
CREATE INDEX idx_security_osv_vulnerability_cache_expires ON security_osv_vulnerability_cache(expires_at);

CREATE TABLE IF NOT EXISTS security_observable_publications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    observable_type TEXT NOT NULL,
    run_id          UUID REFERENCES security_scan_runs(id) ON DELETE CASCADE,
    target_key_hash TEXT,
    finding_id      UUID REFERENCES security_findings(id) ON DELETE CASCADE,
    breach_id       UUID REFERENCES security_policy_breaches(id) ON DELETE CASCADE,
    event_kind      INT NOT NULL,
    d_tag           TEXT NOT NULL,
    schema          TEXT NOT NULL,
    publish_state   TEXT NOT NULL CHECK (publish_state IN ('pending', 'published', 'failed_retryable', 'failed_terminal')),
    event_id        TEXT,
    attempt_count   INT NOT NULL DEFAULT 0,
    last_error      TEXT,
    next_retry_at   TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_security_observable_publications_coordinate
    ON security_observable_publications(observable_type, d_tag, event_kind);
CREATE INDEX idx_security_observable_publications_retry
    ON security_observable_publications(publish_state, next_retry_at)
    WHERE publish_state = 'failed_retryable';
CREATE INDEX idx_security_observable_publications_run ON security_observable_publications(run_id);
