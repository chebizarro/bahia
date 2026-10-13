# Backup

Bahia models backup repositories, policies, recipes, definitions, runs, restores, verification, and retention as signed control-plane records. Open **Backup** (`/backup`) to configure the system and follow operations.

## Configure backup

A usable backup plan combines:

- **Repository** — the storage destination and its credential references.
- **Policy** — schedule, retention, verification, and approval requirements.
- **Recipe** — the workload-specific commands or adapter behavior.
- **Definition** — the service/environment resource set governed by the repository, policy, and recipe.

Credentials stay in the secret store. Published records contain references and redacted operational metadata.
Repository, policy, recipe, and definition intents include the owning
organization UUID in both `org` tag and `content.org_id`. The organization is
fixed at creation. A legacy record without a canonical owner is not made
usable by editing its SQL index; an operator must republish owner-attested
canonical state. There is currently no supported ownerless-coordinate
republish command: the normal `apply_backup_repository`,
`apply_backup_policy`, `apply_backup_recipe`, and `apply_backup_definition`
MCP paths reject an existing ownerless head. Do not retry them with a guessed
`org_id` or use a SQL row as the claim. Keep the affected coordinate blocked
until the operator-approved org-to-coordinate attestation and signing path is
specified and implemented.
Configuration create, update, and delete intents read the owning record and
their referenced records from verified service-signed local events. They
publish canonical state before attempting a best-effort PostgreSQL index
update; a database outage does not block configuration intents. Repository,
policy, recipe, and definition IDs are minted by the author and are not
resolved from SQL names. A signed tombstone makes a reference unusable. The
optional SQL index carries `org_id` and is replayed from canonical heads after
relay warm-start; its rows do not prove ownership.

The MCP registry exposes apply, list, and inspect tools for each resource under both a base name such as `apply_backup_policy` and a `bahia_` alias such as `bahia_apply_backup_policy`. See [MCP Tools](../mcp-tools.md) for the complete names.

## Run and verify

Use the Backup UI or:

- `request_backup_run` / `bahia_request_backup_run`
- `request_backup_verification` / `bahia_request_backup_verification`
- `request_backup_retention` / `bahia_request_backup_retention`

Backup run intake is disabled by default. When an operator enables it with
signer-enforced single-writer custody, canonical credential recovery, local
outbox, and relay quorum, the supported effect is one unfiltered Kopia
snapshot without verification effects; a non-verifying policy may be pinned.
A run request needs an exact
operator-signed event that binds `org_id`, `definition_id`, the definition
coordinate, and service-signed configuration event IDs. The daemon checks
the org on every pinned configuration record, retains a run admission receipt,
and reports `pending` until both run state and accepted status reach relay
quorum. SQL rows and local relay observations are not admission receipts.
Unsupported backup effects and retention request intake remain unavailable.
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
