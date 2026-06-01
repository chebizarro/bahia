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

1. Navigate to **Backup** in the sidebar
2. Click **New Definition**
3. Configure:
   - **Name**: Definition identifier
   - **Target**: What to back up
   - **Schedule**: When to run
   - **Policy**: Retention and verification rules
   - **Repository**: Where to store
4. Click **Create**

### CLI

```bash
bahia backup definitions create \
  --name "database-daily" \
  --target type=postgresql,connection="postgres://..." \
  --schedule "0 2 * * *" \
  --policy-id policy-123 \
  --repository-id repo-456
```

### MCP Tool

```json
{
  "tool": "bahia_backup_definition_apply",
  "arguments": {
    "name": "database-daily",
    "target": {
      "type": "postgresql"
    },
    "schedule": "0 2 * * *"
  }
}
```

## Backup Runs

### Manual Trigger

```bash
bahia backup run database-daily
```

```json
{
  "tool": "bahia_backup_run",
  "arguments": {
    "definition": "database-daily"
  }
}
```

### Viewing Runs

```bash
bahia backup runs list
bahia backup runs get run-123
```

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

### Trigger Verification

```bash
bahia backup verify run-123
```

```json
{
  "tool": "bahia_backup_verify",
  "arguments": {
    "run_id": "run-123"
  }
}
```

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

### Initiating Restore

```bash
bahia backup restore run-123 \
  --target "postgres://restore-db/..."
```

```json
{
  "tool": "bahia_backup_restore",
  "arguments": {
    "run_id": "run-123",
    "target": "postgres://restore-db/..."
  }
}
```

### Restore Approval

Production restores may require approval:

```bash
bahia backup restore approve restore-456
bahia backup restore reject restore-456 --reason "Wrong target"
```

### Restore Status

```bash
bahia backup restores list
bahia backup restores get restore-456
```

## Backup Policies

### Creating Policies

```bash
bahia backup policies create \
  --name "production-policy" \
  --retention daily=7,weekly=4,monthly=12 \
  --verification required=true,frequency=weekly
```

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

### Creating Repository

```bash
bahia backup repositories create \
  --name "s3-backups" \
  --type s3 \
  --config bucket=my-backups,region=us-east-1
```

### Repository Health

```bash
bahia backup repositories probe repo-456
```

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
