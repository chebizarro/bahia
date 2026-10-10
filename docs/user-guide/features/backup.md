# Backup

Bahia models backup repositories, policies, recipes, definitions, runs, restores, verification, and retention as signed control-plane records. Open **Backup** (`/backup`) to configure the system and follow operations.

## Configure backup

A usable backup plan combines:

- **Repository** — the storage destination and its credential references.
- **Policy** — schedule, retention, verification, and approval requirements.
- **Recipe** — the workload-specific commands or adapter behavior.
- **Definition** — the service/environment resource set governed by the repository, policy, and recipe.

Credentials stay in the secret store. Published records contain references and redacted operational metadata.

The MCP registry exposes apply, list, and inspect tools for each resource under both a base name such as `apply_backup_policy` and a `bahia_` alias such as `bahia_apply_backup_policy`. See [MCP Tools](../mcp-tools.md) for the complete names.

## Run and verify

Use the Backup UI or:

- `request_backup_run` / `bahia_request_backup_run`
- `request_backup_verification` / `bahia_request_backup_verification`
- `request_backup_retention` / `bahia_request_backup_retention`

**Backup run intake and execution are currently paused.** The
`request_backup_run` MCP tool still requires an exact operator-signed event,
but a new production request does not stage a queued run or return `pending` or
`accepted`. A retained pending request is also reported as unavailable; an
already-admitted, fully ACKed historical request can be recognized on exact-ID
replay. Do not treat a PostgreSQL run row or local relay observation as an
admission receipt.

The implemented but disabled admission protocol is tested to stage a signed
queued state only after exact configuration proofs, then report `pending`
until both the run state and final signed accepted status reach their required
relay quorums. It must not be enabled before the deployment-wide signer and
outbox fences, authoritative history seal, and terminal status settlement are
available. A queued state would still not start execution or prove repository
credential recovery. Retention request intake remains unavailable.
Verification should prove that stored data can be read and checked, not only
that an upload command exited successfully.

## Restore

A restore has its own record and approval state:

1. Request a restore for a specific backup and destination.
2. Review the requested scope, overwrite behavior, and target.
3. Approve or reject it in the UI or with `approve_backup_restore` / `reject_backup_restore`.
4. Follow the restore record through execution and verification.

The web approval control publishes a signed `backup` `restore-approval` intent when the backup intent domain is enabled. Restore request and approval intake currently reject while atomic canonical execution inputs and recovery are unavailable. Approval never substitutes for post-restore verification.

## Safety

- Test restore procedures on a schedule.
- Keep repository credentials separate from workload credentials.
- Require approval for destructive or in-place restores.
- Set retention from recovery objectives, then monitor retention runs.
- Treat missing or stale run evidence as unknown.

## Related

- [Services](services.md)
- [Workers](workers.md)
- [Notifications](notifications.md)
