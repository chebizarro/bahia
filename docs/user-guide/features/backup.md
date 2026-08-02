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
target:
  type: "postgresql"
  connection: "postgres://..."
schedule: "0 2 * * *"  # Daily at 2 AM
policy_id: "policy-123"
repository_id: "repo-456"
```

### Backup Policy

A **Backup Policy** defines retention and verification:

```yaml
name: "production-policy"
retention:
  daily: 7
  weekly: 4
  monthly: 12
verification:
  required: true
  frequency: "weekly"
```

### Backup Repository

A **Backup Repository** is where backups are stored:

```yaml
name: "s3-backup-repo"
type: "s3"
config:
  bucket: "company-backups"
  region: "us-east-1"
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
| `verified` | Verified restorable |

## Verification

Verify backups are restorable:

### Trigger verification

Call `request_backup_verification` or `bahia_request_backup_verification` with `backup_run_id`, an `idempotency_key`, and optional `mode: "kopia_snapshot_verify"`.

### Verification Process

1. Download backup from repository
2. Restore to test environment
3. Validate data integrity
4. Report verification status

### Verification Results

```yaml
verification:
  run_id: "run-123"
  status: "verified"
  verified_at: "2024-01-15T10:00:00Z"
  details:
    tables_checked: 42
    rows_sampled: 10000
    integrity: "pass"
```

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

### Retention Rules

```yaml
retention:
  daily: 7      # Keep 7 daily backups
  weekly: 4     # Keep 4 weekly backups
  monthly: 12   # Keep 12 monthly backups
  yearly: 3     # Keep 3 yearly backups
```

### Verification Requirements

```yaml
verification:
  required: true
  frequency: "weekly"  # Verify at least weekly
  auto_verify: true    # Verify immediately after backup
```

## Backup Repositories

### Types

| Type | Description |
|------|-------------|
| `s3` | Amazon S3 or compatible |
| `gcs` | Google Cloud Storage |
| `azure` | Azure Blob Storage |
| `local` | Local filesystem |
| `blossom` | Blossom blob storage |

### Creating a repository

Use the web mutation panel or `apply_backup_repository` / `bahia_apply_backup_repository`.

### Repository health

Use `probe_backup_repository` / `bahia_probe_backup_repository` with a repository identity and idempotency key.

## Nostr Event Kinds

| Kind | Name | Description |
|------|------|-------------|
| 38400 | BackupRunRequest | Trigger backup |
| 38401 | BackupVerificationRequest | Verify backup |
| 38402 | BackupRestoreRequest | Restore backup |
| 38403 | BackupRestoreApproval | Approve restore |
| 6981 | BackupRunStatus | Run progress |
| 6982 | BackupRestoreStatus | Restore progress |
| 6983 | BackupVerificationStatus | Verification progress |
| 31310 | BackupRunAttestation | Signed attestation |

## Read Models

| Kind | d-tag | Content |
|------|-------|---------|
| 31991 | `backup-definition:<name>` | Definition |
| 31992 | `backup-policy:<id>` | Policy |
| 31993 | `backup-repository:<id>` | Repository |
| 31996 | `backup-run:<id>` | Run state |
| 31997 | `backup-verification:<id>` | Verification state |
| 31998 | `backup-restore:<id>` | Restore state |

## Best Practices

1. **Test restores regularly** — Don't assume backups work
2. **Use policies** — Consistent retention and verification
3. **Multiple repositories** — Geographic redundancy
4. **Monitor failures** — Alert on backup issues
5. **Document recovery** — Know how to restore

## Troubleshooting

### Backup Failed

- Check target connectivity
- Verify credentials
- Review backup logs
- Check repository space

### Verification Failed

- Check restore target availability
- Verify backup integrity
- Review verification logs

### Restore Failed

- Verify backup exists
- Check target permissions
- Review restore logs

## Related

- [Services](services.md) — Backup targets
- [Workers](workers.md) — Backup execution
- [Notifications](notifications.md) — Backup alerts
