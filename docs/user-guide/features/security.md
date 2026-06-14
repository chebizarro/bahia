# Security (OSV Vulnerability Scanning)

**Security** in Bahia provides automated vulnerability scanning using the [OSV](https://osv.dev/) database. It observes SBOM creation and import events, supports explicit scans for packages, PURLs, and Git commits, evaluates policy-scoped thresholds, and dispatches notifications only for new or materially changed policy breaches.

## Overview

Security features include:

- **SBOM-triggered scans** — Automatic scanning when SBOMs are generated or imported
- **Explicit scans** — On-demand scanning for SBOM references, package coordinates, PURLs, and Git commits
- **Scheduled rescans** — Repository-backed periodic rescans derived from policy settings
- **Policy-scoped thresholds** — Vulnerability count limits that gate deployments
- **Breach notifications** — Fingerprinted alerts dispatched only for actionable changes
- **Nostr observables** — Durable scan status, summaries, findings, and audit events

## How It Works

Security is an event-driven observer of canonical SBOM truth. It does **not** run inline with SBOM generation or block deployments while scanning.

```
SBOM generated/imported
  → Security observes 30078/30004 events
    → Verifies payload SHA-256 hash
      → Normalizes packages/PURLs to OSV queries
        → Persists findings
          → Publishes Security observables
            → Evaluates policy breaches
              → Dispatches notifications (if breach is new/changed)
```

### Scan Triggers

| Trigger | Description |
|---------|-------------|
| **SBOM observable** | Security subscribes to SBOM `30078` reference and `30004` availability events and scans automatically |
| **Explicit scan** | Operator requests a scan via ContextVM `security/scan` method |
| **Rescan** | Operator requests a rescan for a known target via `security/rescan` |
| **Scheduled** | Policy-derived periodic rescans run on cadence ticks |

### Target Types

Security supports four target types, each with a deterministic canonical key:

| Target | Key Format | Example |
|--------|-----------|---------|
| SBOM | `sbom:<subject_type>:<subject_key>:<format>:<payload_sha256>:<ref_d_tag>` | `sbom:artifact:sha256:abc123:spdx:def456:sbom-ref-art-1` |
| Package | `package:<ecosystem>:<name>:<version>` | `package:npm:lodash:4.17.21` |
| PURL | `purl:<normalized_purl>` | `purl:pkg:npm/lodash@4.17.21` |
| Commit | `commit:<repo_url_hash>:<commit_hash>` | `commit:abc123:def456` |

Each target key also has a SHA-256 hash form used for `d` tags, indexes, breach fingerprints, and subscription filters.

## SBOM-Triggered Scans

When an SBOM is generated or imported, Bahia publishes canonical `30078` SBOM reference events and `30004` availability lists. The Security service:

1. **Subscribes** to SBOM events with narrow filters (`#domain=sbom`, exact schema tags)
2. **Processes historical events** until `EOSE`, then keeps subscriptions open for realtime updates
3. **Resolves** the referenced SBOM payload from Blossom storage
4. **Verifies** the payload SHA-256 hash before parsing — hash mismatches fail the scan immediately
5. **Normalizes** SBOM packages to OSV query targets:
   - Prefers PURL when present (does not send top-level `version` if PURL includes one)
   - Falls back to `{ecosystem, name, version}` when PURL is absent
   - Skips CPE-only packages with a recorded `unsupported_coordinate` reason
   - Deduplicates identical coordinates before batch submission
6. **Queries** OSV via `/v1/querybatch` with chunking and retry/backoff
7. **Persists** findings and updates target latest state
8. **Publishes** Security observables with relay OK verification

SBOM-triggered scans never block the SBOM generation/import lifecycle.

## Explicit Scans

Request a scan for any supported target through encrypted ContextVM:

### Scan an SBOM Reference

```json
{
  "jsonrpc": "2.0",
  "id": "scan-sbom-1",
  "method": "security/scan",
  "params": {
    "idempotencyKey": "scan-sbom-1",
    "target": {
      "type": "sbom",
      "subject": { "type": "artifact", "id": "art-456", "digest": "sha256:abc123" },
      "reference_d_tag": "sbom-ref-art-456-spdx"
    }
  }
}
```

### Scan a Package Coordinate

```json
{
  "jsonrpc": "2.0",
  "id": "scan-pkg-1",
  "method": "security/scan",
  "params": {
    "idempotencyKey": "scan-pkg-1",
    "target": {
      "type": "package",
      "package": { "ecosystem": "npm", "name": "lodash", "version": "4.17.20" }
    }
  }
}
```

### Scan a PURL

```json
{
  "jsonrpc": "2.0",
  "id": "scan-purl-1",
  "method": "security/scan",
  "params": {
    "idempotencyKey": "scan-purl-1",
    "target": {
      "type": "purl",
      "purl": "pkg:npm/lodash@4.17.20"
    }
  }
}
```

### Scan a Git Commit

```json
{
  "jsonrpc": "2.0",
  "id": "scan-commit-1",
  "method": "security/scan",
  "params": {
    "idempotencyKey": "scan-commit-1",
    "target": {
      "type": "commit",
      "repository_url": "https://github.com/example/repo",
      "commit_hash": "abc123def456"
    }
  }
}
```

### Rescan a Known Target

```json
{
  "jsonrpc": "2.0",
  "id": "rescan-1",
  "method": "security/rescan",
  "params": {
    "idempotencyKey": "rescan-1",
    "target_key_hash": "<sha256-of-target-key>",
    "force": true
  }
}
```

> **Important:** The ContextVM response is an **acknowledgment only**. Terminal scan progress and results are visible through Security observables (see [Following Scan Progress](#following-scan-progress)).

## Scheduled Rescans

Security schedules are derived from enabled policy-scoped `security_osv_scan` rules. The scheduler:

1. Runs **once at startup** then on **cadence ticks**
2. **Claims due schedules** from the repository using lease-based coordination
3. **Skips** disabled policies and targets with active duplicate scans
4. **Submits** scheduled scan intents
5. **Records** the next due time from the policy interval
6. Uses **timers only for cadence** — never for event delivery or completion detection

Schedule and freshness state can be read via:

```json
{
  "jsonrpc": "2.0",
  "id": "sched-list-1",
  "method": "security/schedules-list",
  "params": {
    "policy_id": "policy-123",
    "enabled_only": true
  }
}
```

## Policy Integration

### Security OSV Scan Rule

Add the `security_osv_scan` rule to any deployment policy:

```json
{
  "rules": [
    { "type": "require_sbom" },
    {
      "type": "security_osv_scan",
      "params": {
        "enabled": true,
        "interval_seconds": 86400,
        "freshness_seconds": 604800,
        "no_scan": "block",
        "stale": "block",
        "failed": "block"
      }
    },
    { "type": "max_critical_vulns", "params": { "max": 0 } },
    { "type": "max_high_vulns", "params": { "max": 5 } }
  ],
  "enforcement": "block"
}
```

### Policy Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `enabled` | bool | Enable Security OSV scanning for this policy scope |
| `interval_seconds` | int | Rescan interval in seconds (e.g., `86400` = daily) |
| `freshness_seconds` | int | Maximum age of a scan before it is considered stale |
| `no_scan` | string | Behavior when no scan exists: `"block"`, `"warn"`, or `"pass"` |
| `stale` | string | Behavior when the latest scan is stale: `"block"`, `"warn"`, or `"pass"` |
| `failed` | string | Behavior when the latest scan failed: `"block"`, `"warn"`, or `"pass"` |

### Deployment Gate Behavior

Deployment gates evaluate the **latest completed Security scan** for the target. They do **not** trigger a new scan or wait for one to complete.

| Scan State | `"block"` | `"warn"` | `"pass"` |
|------------|-----------|----------|----------|
| No scan exists | Deployment blocked | Warning logged | Deployment allowed |
| Scan is stale | Deployment blocked | Warning logged | Deployment allowed |
| Scan failed | Deployment blocked | Warning logged | Deployment allowed |
| Scan completed, thresholds breached | Deployment blocked | Warning logged | N/A (use threshold rules) |
| Scan completed, thresholds met | Deployment allowed | Deployment allowed | Deployment allowed |

### Vulnerability Count Preferences

`max_critical_vulns`, `max_high_vulns`, and `require_scan_status` rules prefer **latest successful Security OSV counts** when available. If no Security scan exists, they fall back to existing SBOM aggregate counts for compatibility.

## Breach Notifications

### How Breach Fingerprinting Works

Security evaluates policy-scoped thresholds after each scan. When a breach is detected:

1. A **deterministic fingerprint** is computed from:
   - Policy ID + target key hash
   - Policy revision/update timestamp
   - Enforcement level
   - Violated rules
   - Severity counts
   - Sorted OSV IDs
2. The fingerprint is compared to any existing active breach for that policy + target
3. Notifications are dispatched **only when**:
   - The breach is **new** (no prior breach exists)
   - The breach is **materially changed** (fingerprint differs)
4. **Unchanged recurring breaches** do not trigger duplicate notifications
5. Scans that **resolve** a breach (no longer violating) mark it resolved without notification

### Configuring Breach Notifications

Add `security.policy_breached` to your notification channel events:

```json
{
  "type": "webhook",
  "name": "Security Alerts",
  "config": {
    "url": "https://hooks.example.com/security"
  },
  "events": ["security.policy_breached"]
}
```

Breach notifications flow through the existing notification dispatcher and support all channel types (webhook, email, Slack, Nostr DM).

### Breach Audit Facts

Every new or changed breach also publishes a `4903` audit fact with:
- `domain=security`
- `schema=bahia.audit.security.v1`
- `type=security-policy-breach`
- Policy, target, enforcement, and violation metadata

## Following Scan Progress

### Nostr Observables

Security publishes four kinds of durable events:

| Kind | Schema | Purpose |
|------|--------|---------|
| `30315` | `bahia.status.security-scan.v1` | Scan status updates (`accepted` → `running` → `completed`/`failed`/`cancelled`) |
| `30900` | `bahia.security.scan-summary.v1` | Per-run scan summary with severity counts |
| `30900` | `bahia.security.target-summary.v1` | Latest target state across all scans |
| `30078` | `bahia.security.findings.v1` | Normalized finding details (public-safe, no raw OSV cache) |
| `4903` | `bahia.audit.security.v1` | Audit facts for lifecycle, breaches, and publication events |

### Subscription Filter

Follow a specific scan with:

```json
{
  "kinds": [30315, 30900, 30078, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["security"],
  "#target_key_hash": ["<target-key-hash>"],
  "#e": ["<contextvm-request-event-id>"]
}
```

Process historical events until `EOSE`, keep the subscription open for realtime convergence, deduplicate by event ID, and handle `CLOSED`/`AUTH` relay outcomes.

### Reading Persisted Findings

Query persisted findings without subscribing:

```json
{
  "jsonrpc": "2.0",
  "id": "findings-1",
  "method": "security/findings-list",
  "params": {
    "target_key_hash": "<target-key-hash>",
    "severity": "critical",
    "limit": 50
  }
}
```

> **Note:** Read responses expose persisted projections. They do not launch polling loops or claim that in-flight scans are complete.

## Compatibility with SBOM Aggregates

When a Security scan completes successfully, Bahia updates the SBOM compatibility projections (`SBOMManifest` and `ArtifactSBOM`) with the latest vulnerability counts:
- `vulnerability_count`, `critical_count`, `high_count` are refreshed from Security scan severity counts
- Canonical SBOM `30078` references and `30004` availability lists are **never mutated**
- If no Security scan exists, original SBOM aggregate counts remain visible

This ensures existing policy/UI consumers continue to see current vulnerability data without requiring Security-aware code paths.

## Access Model

Security scan and read operations use **encrypted ContextVM** (`25910` inside `1059`/`21059` gift-wrap). There are no REST endpoints or MCP tools for Security operations. Clients must use signed Nostr events and subscriptions.

| Method | Type | Description |
|--------|------|-------------|
| `security/scan` | Mutation | Request an explicit scan |
| `security/rescan` | Mutation | Request a rescan for a known target |
| `security/findings-list` | Read | Query persisted findings |
| `security/schedules-list` | Read | Query scan schedules and freshness |

## OSV Provider Behavior

The OSV adapter handles production concerns:

- **Batch queries** via `/v1/querybatch` with preserved result ordering
- **PURL queries** — version stays only in the PURL, not duplicated at top level
- **Commit queries** via `/v1/query` with `commit` field
- **Vulnerability hydration** via `GET /v1/vulns/{id}` when policy or notification payloads need details
- **Chunking** for large SBOM package sets
- **Retry/backoff** for transient 429/5xx failures with `Retry-After` header support
- **Non-retryable** handling for 400/404 (invalid request, unknown vulnerability)
- **Cache** with bounded retention (default 30 days for raw hydrated data)
- **Deduplication** of identical coordinates before batch submission

## Failure Handling

| Failure | Behavior |
|---------|----------|
| SBOM payload hash mismatch | Scan fails immediately; no OSV queries are sent |
| OSV transient error (429/5xx) | Retried with backoff; scan fails after max retries |
| OSV invalid request (400) | Non-retryable error; scan records failure reason |
| Relay publish rejection | Publication state set to `failed_retryable`; retry path available |
| Relay CLOSED/AUTH | Authentication attempted; if unavailable, relay excluded per policy |
| Active duplicate scan | New scan suppressed; existing scan continues |
| CPE-only package | Skipped with `unsupported_coordinate` reason; not an error |

Failed scans produce:
- Terminal `failed` status in `30315` status events
- `4903` audit fact with error details
- Persisted error state in the scan run record

## Migration and Rollback

### Database Migration

Security uses additive migration `000044_security_osv` which creates 8 new tables:

| Table | Purpose |
|-------|---------|
| `security_scan_targets` | Canonical target identity and metadata |
| `security_scan_runs` | Scan lifecycle, status, severity counts, publication state |
| `security_target_latest` | Latest scan state per target for fast lookup |
| `security_findings` | Normalized OSV findings per run |
| `security_scan_schedules` | Policy-derived rescan schedules with lease coordination |
| `security_policy_breaches` | Breach fingerprints and notification state |
| `security_osv_vulnerability_cache` | Bounded cache for hydrated OSV data |
| `security_observable_publications` | Publication state tracking for retry |

### Rollback Safety

- **Additive only** — no existing tables or columns are modified
- **Down migration** drops only the 8 Security tables in reverse dependency order
- **Old code** ignores Security rows and events entirely
- **New code** falls back to SBOM aggregate counts when Security state is absent
- **Canonical SBOM events** (`30078`, `30004`) are never mutated by Security

To rollback:
1. Disable the Security feature in policy settings
2. Run `000044_security_osv.down.sql` to drop Security tables
3. Existing SBOM aggregate counts and SBOM observables remain intact

## Troubleshooting

### Scan Not Triggering After SBOM Import

- Verify Security is enabled in policy settings (`security_osv_scan.enabled: true`)
- Check that SBOM import published `30078` and `30004` events with relay OK
- Check Bahia service logs for Security subscription status
- Verify the service key can subscribe to SBOM events with `#domain=sbom` filters

### Scan Shows "Failed" Status

- Check the `error` field in the scan run status event or `4903` audit fact
- Common causes:
  - **Payload hash mismatch** — SBOM payload in Blossom doesn't match the hash in the `30078` reference
  - **OSV unreachable** — Network issues reaching `api.osv.dev`; retries exhausted
  - **Invalid PURL** — Malformed PURL rejected by OSV as 400
- For transient failures, a rescan via `security/rescan` will attempt again

### No Vulnerability Counts in SBOM View

- Check if a Security scan has completed for the artifact target
- If no scan exists, the SBOM view shows original aggregate counts
- Use `security/findings-list` to query the target's findings directly

### Breach Notification Not Received

- Verify the notification channel includes `security.policy_breached` in its event filter
- Check notification logs for delivery attempts
- Confirm the breach is **new or changed** — unchanged recurring breaches are intentionally suppressed
- Check `4903` audit facts for `type=security-policy-breach` to confirm the breach was detected

### Scheduled Rescan Not Running

- Verify the policy has `security_osv_scan.enabled: true` and `interval_seconds` set
- Check that the schedule is not leased by another instance (multi-instance coordination)
- Verify no active duplicate scan exists for the same target
- Check service logs for scheduler tick output

### Policy Blocking Deployments Unexpectedly

- Check which `no_scan`, `stale`, or `failed` behavior is configured
- If `no_scan: "block"` and no Security scan exists, deployments will be blocked
- Consider `"warn"` during initial rollout before switching to `"block"`
- Use `security/schedules-list` to check scan freshness for the target

## Related

- [Artifacts](artifacts.md) — SBOM generation and import
- [Policies](policies.md) — Policy rules and deployment gates
- [Notifications](notifications.md) — Channel configuration
- [Nostr Integration](../nostr-integration.md) — Security observable subscription patterns
