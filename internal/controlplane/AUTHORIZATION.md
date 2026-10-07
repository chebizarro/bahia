# ContextVM authorization boundaries

`nostr.authorized_pubkeys` is a mandatory transport allowlist, not sufficient
method authorization. When empty, all requests are rejected before dispatch,
including tenant handlers. Fleet operator methods additionally require the
same list explicitly and deny an empty or missing gate. A single-operator dev
installation must list that operator's public key; there is no allow-any mode
and enabling Assistant does not grant its service signer operator authority.

`RegisterOperatorContextVMHandler` checks its gate before replay-cache reads,
progress acknowledgments, or handler execution. Unknown or removed
registrations return JSON-RPC `-32601`, even if a gate or cached result exists
for the name. Signature validation and the mandatory transport allowlist
precede all dispatch; there is no permissive configuration option.

Browser service mutations do not bypass the allowlist: service creation and
deployment preview use `public-controlplane.svelte.js`'s `publishCommand`,
then `encrypted-controlplane.js`'s `requestEncryptedResult`, then this
transport. `app.New` passes `cfg.Nostr.AuthorizedPubkeys` directly. Tenant
RBAC runs only after transport admission, and an empty list denies even a
tenant admin. Any tenant-RBAC opt-in requires an explicit configuration
design; it is never inferred from an omitted list.

## Method classification

| Methods | Scope and reason |
| --- | --- |
| `settings/relay-policy.apply`, `settings/relay-admin.call`, `config/reconcile`, `config/reload`, `config/status` | Operator: change fleet policy or invoke NIP-86 using service credentials, including status reads |
| `settings/relay-policy.get` | Admitted read: serves the relay-policy projection, does not invoke NIP-86 or publish commands. No additional method-level operator gate |
| `backup/repository-register`, `backup/policy-apply`, `backup/recipe-apply`, `backup/definition-apply`, `backup/run`, `backup/verification`, `backup/restore`, `backup/retention`, `approval/backup-restore-approve`, `backup/repository-probe` | Operator **and** the tenant `backups:manage` check. Backup resource IDs have no enforced tenant-ownership model; claiming a tenant must not permit arbitrary repository operations |
| `loom/submit` | Operator: arbitrary container workloads on fleet workers; service/environment labels are not tenant authorization |
| `loom/cancel` | Recorded submitter or explicit `loom.authorized_pubkeys` operator, with a known submitter required. This is requester ownership, not tenant RBAC. An empty Loom operator list allows only the recorded submitter; transport admission is always required first |
| `assistant/prompt`, `assistant/approval` | Operator plus session-participant checks; Assistant can issue service-signed fleet operations |
| `service/deploy-preview`, `service/deploy`, `service/route-attach`, `service/rollback` | Tenant RBAC: verified signer needs deployment-write permission on both the service and environment, with matching organization ownership |
| `approval/approve`, `approval/reject` | Tenant RBAC: resolve the intent and require deployment-approval permission on its service and environment, under release-promotion policy |

DNS, workers, SBOM, security, continuity, policy, package and tool approval
carry their own operator gates. `service/action` and adoption use their
separate operator allowlists; they are not the tenant service methods above.

## Backup delegation

The `bahia.backup.delegation.v1` record and matching tags are generated from
the verified ContextVM event after operator and tenant authorization.
Caller-supplied requester/delegation fields cannot replace that record. The
service signs the downstream command as its issuer, not as the requester.

The consumer validates the issuer against the configured service signer, the
event ID/signature, and the record/tag agreement. It then checks the
**recorded requester** against its allowlist, not the outer signer. The
service key cannot be the delegated requester or issue an undelegated backup
command, even when Assistant wiring places that key in the reactor allowlist.
Durable run, restore, retention and approval attribution uses the recorded
requester. Delegation does not imply a backup ownership model or a two-person
approval policy.

## Loom ownership

Only the verified event signer supplies the submitter. That identity is
recorded before canonical projection starts. Cancellation ignores claimed
submitter or requester fields and uses the client's recorded owner and the
`loom.authorized_pubkeys` operator list. Admission via
`nostr.authorized_pubkeys` does not by itself grant cancellation of another
submitter's job. The service signer cannot submit or cancel on its own
authority, even when explicitly allowlisted.

The client ownership map is in-memory. After a restart or for an untracked
job, ownership is unknown and cancellation fails closed, including for
operators.
