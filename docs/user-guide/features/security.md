# Security

The Security dashboard provides visibility into vulnerability scanning powered by the [OSV](https://osv.dev) database. Bahia scans SBOMs, packages, PURLs, and Git commits for known vulnerabilities and surfaces the results in a unified view.

## How Scanning Works

Security scans are triggered in three ways:

1. **Automatic SBOM scans** — when an SBOM is created, imported, or updated, Bahia automatically submits it for vulnerability scanning.
2. **Scheduled rescans** — policies can configure recurring scans on a cadence (e.g., every 24 hours) to catch newly disclosed vulnerabilities.
3. **Manual scans** — operators can trigger a rescan of any target directly from the Security dashboard.

All scan operations use encrypted ContextVM methods (`security/scan`, `security/rescan`, `security/findings-list`, `security/schedules-list`) over Nostr. There are no REST endpoints for security data — all communication is end-to-end encrypted.

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

A **Rescan Target** button at the top right lets you trigger a new scan for the same target.

## Rescan

From both the dashboard and detail pages, you can trigger a rescan of any target. This submits a new scan request via ContextVM and the results will appear once the scan completes. Scan progress is published as NIP-38 status events on the relay.

## Notifications

Security scan breaches (findings that violate policy thresholds) are routed through the [Notifications](/notifications) system. Configure notification channels to receive alerts when critical or high-severity vulnerabilities are detected.

## Nostr Event Semantics

Security scan operations follow Bahia's Nostr-native architecture:

- **Mutations**: ContextVM kind `25910` wrapped in NIP-59 gift-wrap (`1059`/`21059`)
- **Scan status**: NIP-38 kind `30315` status events with `schema=bahia.status.security-scan.v1`
- **State projections**: Kind `30900` with security-specific schemas

The ContextVM acknowledgment (`security/scan` returning `accepted` with a `run_id`) is not completion — subscribe to the corresponding NIP-38 status events to track scan progress to terminal state.
