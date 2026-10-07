# Security

**Security** (`/security`) turns artifact SBOMs into OSV vulnerability findings and policy evidence.

## Scan workflow

1. An artifact publishes a readable SBOM reference and availability record.
2. Bahia submits the supported package coordinates to the configured OSV scanner.
3. A signed `security` `scan-run` intent can start or repeat a scan.
4. The daemon publishes encrypted canonical target, run, finding, schedule, and finding-detail records.
5. Policies and notification channels consume the resulting evidence.

The scan acknowledgement is bounded. Follow the scan-run and finding records for completion.

## Dashboard

The main page shows severity totals, current findings, schedules, and scan status. Select a finding or scan run for detail. **Rescan** publishes a new signed scan intent; it does not edit the earlier result.

Finding detail may be split into fixed parts plus a manifest. Readers use only the parts named by the current manifest.

## Authorization and confidentiality

Security routes require an authenticated operator with access to the owning organization. Finding records are OCK-encrypted. The browser verifies signatures and authors, unwraps the OCK with the active signer, and shows unreadable state when no envelope is available.

## Notifications and policy

Channels can subscribe to `security.policy_breached`. Bahia deduplicates breach fingerprints so an unchanged finding set does not create a new alert on every pass. Blocking policies may require a completed scan or constrain accepted severity.

## Troubleshooting

- Confirm the SBOM reference and availability records both exist.
- Confirm the scanner is enabled and its readiness check passes.
- Re-run the scan after SBOM or policy evidence changes.
- Treat stale findings as stale evidence, not a clean bill of health.

## Related

- [Artifacts](artifacts.md)
- [Policies](policies.md)
- [Notifications](notifications.md)
