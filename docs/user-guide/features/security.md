# Security

Manual `security/scan-run` is a client-signed request intent. Its bounded `30315` acceptance identifies the run and target hash; follow the existing security scan status, summary, findings, and audit records for progress and completion. See the [D80 wire fixture](../../../web/tests/fixtures/d80-intent-content.json).

The Security dashboard provides visibility into vulnerability scanning powered by the [OSV](https://osv.dev) database. Bahia scans SBOMs, packages, PURLs, and Git commits for known vulnerabilities and surfaces the results in a unified view.

## How Scanning Works

Security scans are triggered in three ways:

1. **Automatic SBOM scans** — when an SBOM is created, imported, or updated, Bahia automatically submits it for vulnerability scanning.
2. **Scheduled rescans** — policies can configure recurring scans on a cadence (e.g., every 24 hours) to catch newly disclosed vulnerabilities.
3. **Manual scans** — operators submit a complete target from the Security dashboard.

Manual submissions use signed kind-`30900` `security/scan-run` intents, with bounded acceptance data in requester-scoped `30315` status. Findings, schedules, and finding details are read from service-authored kind-`30900` relay events, encrypted with the fleet operator content key; the browser does not issue ContextVM or REST list requests.

## Dashboard

Navigate to **Security** in the sidebar (under Operations) to access the dashboard. The page loads schedule-derived scopes first, then loads findings for the currently selected target or run scope.

### Severity Summary

When findings exist, the top of the page shows colored summary cards with counts by severity level:

| Severity | Color | Description |
|----------|-------|-------------|
| **Critical** | Red | Actively exploited or trivially exploitable vulnerabilities |
| **High** | Orange | Serious vulnerabilities that should be addressed promptly |
| **Moderate** | Yellow | Vulnerabilities with limited exploitability or impact |
| **Low** | Green | Minor issues with minimal security impact |

### Findings Tab

The Findings tab shows a table of vulnerability findings for the currently selected target or run scope, with:

- **OSV ID** — the OSV database identifier (e.g., `GHSA-xxxx-yyyy`)
- **CVE** — the CVE identifier when available
- **Package** — the affected package in `ecosystem/name@version` format
- **Severity** — color-coded severity badge
- **Summary** — brief description of the vulnerability

Click any row to view the full scan run detail page.

### Schedules Tab

Switch to the Schedules tab to view configured scan schedules:

- **Target** — the hashed target identifier
- **Enabled** — whether the schedule is active
- **Interval** — how often the scan runs (e.g., `24h`, `7d`)
- **Next Due** — when the next scan is scheduled
- **Last Run** — when the schedule last dispatched a scan

Schedules are derived from policies. To create or modify scan schedules, configure security rules in your [Policies](/policies).

## Scan Run Detail

Click a finding row on the dashboard to navigate to `/security/{run_id}`, which shows:

- Severity summary cards for the specific scan run
- Target metadata (hash, total findings)
- Full findings table with additional columns:
  - **Aliases** — alternative identifiers for the vulnerability
  - **References** — links to advisories and patches (opens in new tab)
  - **OSV ID** links directly to the OSV database entry

A **Rescan Target** button can reuse a complete target only when one is available in the canonical schedule metadata. Otherwise use **Run a manual scan** on the Security dashboard and provide the target details.

## Rescan

Use **Run a manual scan** with a complete package, PURL, commit, or SBOM target JSON object. The accepted `30315` status identifies the run; progress and findings arrive through signed scan observables. A hash-only rescan cannot be reconstructed from the bounded schedule projection and fails closed rather than submitting a different target.

## Notifications

Security scan breaches (findings that violate policy thresholds) are routed through the [Notifications](/notifications) system. Configure notification channels to receive alerts when critical or high-severity vulnerabilities are detected.

## Authentication and route access

A persisted NIP-07 or NIP-46 signer session can render relay state immediately; signer verification and fleet key discovery continue in the background. Only a fleet operator who can unwrap the fleet content key can read encrypted findings and schedules. Other users see **not readable with this key**, not a failed read request. Route visibility never grants mutation authority; scan and rescan still require backend authorization.

## Nostr Event Semantics

Security scan operations follow Bahia's Nostr-native architecture:

- **Manual scan mutation**: client-signed kind-`30900` `security/scan-run` intent
- **Scan status**: NIP-38 kind `30315` status events with `schema=bahia.status.security-scan.v1`
- **Encrypted state projections**: Kind `30900` topics `security-target` (legacy kind `32020`), `security-run` (`32021`), `security-finding` (`32012`), `security-schedule` (`32013`), and `security-finding-detail` (`32014`). Targets and runs make scans restartable without Postgres. Large details publish fixed `:part:<n>` chunks followed by a base-coordinate `total_parts` manifest; readers ignore stale parts outside the current manifest.

The requester-scoped `30315` acceptance for `security/scan-run` is not completion — subscribe to the corresponding scan status events to track progress to terminal state.
