# Backup

**Backup** in Bahia provides data protection through scheduled backups, verification, and recovery orchestration.

## Overview

Backup features include:
- **Backup definitions** — What to back up and when
- **Backup policies** — Retention, verification requirements
- **Backup repositories** — Where backups are stored
- **Verification** — Ensure backups are restorable
- **Restore orchestration** — Managed recovery process

## Key Concepts

### Backup Definition

A **Backup Definition** specifies what to back up:

```yaml
name: "database-daily"
repository_name: "kopia-main"
policy_name: "production-policy"
recipe_name: "postgres-dump"
recipe_version: "1"
schedule_expression: "0 2 * * *"  # Daily at 2 AM
schedule_enabled: true
environment_id: "<env-uuid>"
```

A definition binds a **recipe** (what and how to back up) to a repository and policy, with an optional schedule and tenant/environment scope.

### Backup Policy

A **Backup Policy** defines verification requirements:

```yaml
name: "production-policy"
require_verification: true
verification_mode: "kopia_snapshot_verify"   # or none
```

### Backup Repository

A **Backup Repository** is where backups are stored:

```yaml
name: "kopia-main"
backend: "kopia"          # kopia or velero
repository_uri: "s3://company-backups/bahia"
credential_profile: "backup-s3"   # server-side credential reference
```

## Creating Backups

### Web UI

1. Navigate to **Backup** in the sidebar.
2. Open **Repositories**, **Policies**, **Recipes**, or **Definitions**.
3. Use the mutation panel at the top of the section to publish the corresponding signed ContextVM command:
   - `backup/repository-register`
   - `backup/policy-apply`
   - `backup/recipe-apply`
   - `backup/definition-apply`
4. Watch the section list and detail pages for projected Nostr read models. The web command response only confirms that Bahia published the canonical backup command event; durable progress and terminal truth are shown by backup status/result projections.

Operational controls are available on list and detail pages:
- **Run now** on recipes and definitions publishes `backup/run`.
- **Verify** on backup runs publishes `backup/verification`.
- **Request restore** on backup runs publishes `backup/restore` and prompts for a restore target.
- **Enforce retention** on definitions publishes `backup/retention` using the definition repository and policy.
- **Probe repository** publishes `backup/repository-probe`.
- **Approve/Reject restore** publishes `approval/backup-restore-approve`.

### CLI and MCP

The current CLI does not register a `bahia backup` group. Use the web UI or signer-first backup operations. In an embedding that explicitly configures external MCP authorization, use `apply_backup_definition` / `bahia_apply_backup_definition`; its schema requires the definition name plus repository, policy, and recipe identities.

## Backup Runs

### Manual trigger

Use `request_backup_run` (or `bahia_request_backup_run`) with a recipe identity and an `idempotency_key`. Use `list_backup_runs` and `inspect_backup_run` for projected run state.

### Run Status

| Status | Description |
|--------|-------------|
| `queued` | Waiting to start |
| `running` | In progress |
| `succeeded` | Completed successfully |
| `failed` | Encountered error |
| `cancelled` / `timeout` | Did not complete |

Verification is tracked separately in `verification_status` (`pending`, `succeeded`, `failed`, `skipped`, `unsupported`), and restores in their own approval status (`pending`, `approved`, `rejected`, `not_required`).

## Verification

Verify backups are restorable:

### Trigger verification

Call `request_backup_verification` or `bahia_request_backup_verification` with `backup_run_id`, an `idempotency_key`, and optional `mode: "kopia_snapshot_verify"`.

### Verification Process

The supported verification mode is `kopia_snapshot_verify`, which runs Kopia's snapshot verification against the run's snapshot. The outcome is recorded on the run as `verification_status` and determines restore eligibility.

## Restore

### Initiating restore

Call `request_backup_restore` or `bahia_request_backup_restore` with `backup_run_id`, `restore_target_ref`, and an `idempotency_key`.

### Restore approval

Production restores may require approval. Use `approve_backup_restore` or `reject_backup_restore` (and their `bahia_` aliases) with the restore ID and idempotency key.

### Restore status

Use `list_backup_restores` and `inspect_backup_restore`.

## Backup Policies

### Creating policies

Use the web mutation panel or `apply_backup_policy` / `bahia_apply_backup_policy`. The CLI does not register a backup group.

### Retention

Retention is enforced on demand with **Enforce retention** on a definition (`backup/retention`, MCP `request_backup_retention`) and tracked as retention runs (`list_backup_retention_runs`, `inspect_backup_retention_run`).

> **NOTE (2026-09-11):** earlier versions of this page showed daily/weekly/monthly/yearly retention tiers and verification frequency settings on policies. The current policy model carries only `require_verification` and `verification_mode`; check the recipe/backend configuration for retention parameters.

## Backup Repositories

### Backends

| Backend | Description |
|---------|-------------|
| `kopia` | Kopia repository (storage location given by `repository_uri`) |
| `velero` | Velero (Kubernetes) backups |

### Creating a repository

Use the web mutation panel or `apply_backup_repository` / `bahia_apply_backup_repository`.

### Repository health

Use `probe_backup_repository` / `bahia_probe_backup_repository` with a repository identity and idempotency key.

## Nostr Methods and Kinds

Backup mutations are ContextVM methods over kind `25910`: `backup/repository-register`, `backup/policy-apply`, `backup/recipe-apply`, `backup/definition-apply`, `backup/run`, `backup/verification`, `backup/restore`, `backup/retention`, `backup/repository-probe`, and `approval/backup-restore-approve`.

| Kind | Purpose |
|------|---------|
| `30900` | Canonical backup state (`domain=backup`; definitions, policies, repositories, recipes, runs, verifications, restores) |
| `30315` / `4903` | Status and audit facts |
| `31310` | Signed backup run attestation |

The historical `38400`-`38403` request kinds, `6981`-`6983` status kinds, and `31991`-`31998` read-model kinds are migration inventory only.

## Best Practices

1. **Test restores regularly** — Don't assume backups work
2. **Use policies** — Consistent retention and verification
3. **Require verification** — Set `require_verification` on production policies
4. **Monitor failures** — Alert on backup issues
5. **Document recovery** — Know how to restore

## Troubleshooting

### Backup Failed

- Check target connectivity
- Verify the repository `credential_profile`
- Use **Probe repository** to check repository health
- Check repository space

### Verification Failed

- Check restore target availability
- Verify backup integrity
- Review verification logs

### Restore Failed

- Verify backup exists
- Check target permissions
- Review restore logs

## Relay-policy projection provenance

Signed backup runs export the current validated relay-policy projection into the
durable run metadata before snapshot execution. The envelope contains only the
public canonical payload and its event ID, payload hash, author, event/acceptance
timestamps, source relay, and sync timestamp; credentials and private keys are
never included.

An approved signer-first backup restore validates the envelope and restores it
as cached last-known-good state. Cached state remains usable across restart or
relay outage, but is not marked relay-confirmed until the hydrator receives the
same or a newer valid canonical event from a relay. A corrupt hash or regressive
restore fails closed.

## Related

- [Services](services.md) — Backup targets
- [Workers](workers.md) — Backup execution
- [Notifications](notifications.md) — Backup alerts
