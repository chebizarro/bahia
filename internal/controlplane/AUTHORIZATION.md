# ContextVM authorization boundaries

`nostr.authorized_pubkeys` is a mandatory transport allowlist, not sufficient
method authorization. When empty, all requests are rejected before dispatch,
including tenant handlers. Fleet operator methods additionally require the
same list explicitly and deny an empty or missing gate. A single-operator dev
installation must list that operator's public key; there is no allow-any mode
and enabling Assistant does not grant its service signer operator authority.

`RegisterOperatorContextVMHandler` checks its gate before replay-cache reads,
progress acknowledgments, or handler execution. Unknown/removed registrations
return JSON-RPC `-32601`, even if a gate or cached result previously existed.
Signature validation and the mandatory transport allowlist still precede all
dispatch. No permissive configuration option is introduced by this change.

### Historical correction

Commit `ca348cf1cb1684889749f33b2a7ce4c09c8ba5e3` (2026-09-12),
`security: enforce fail-closed platform RBAC`, deliberately changed
`EncryptedRequestTransport.authorized()` from empty-means-allow to
empty-means-deny and added `TestContextVMTransport_EmptyAllowlistDeniesAll`.
The open-when-empty premise in bahia-4yhej and bahia-9sav5's earlier notes is
stale at base `6661c0eb`. This change leaves that function and test intact.

Browser service mutations do not bypass the allowlist: service creation and
deployment preview use `public-controlplane.svelte.js`'s `publishCommand`,
then `encrypted-controlplane.js`'s `requestEncryptedResult`, then this transport.
`app.New` passes `cfg.Nostr.AuthorizedPubkeys` directly. Tenant RBAC runs only
after admission. A regression using the registered service route-attach handler
proves an empty list denies even a tenant admin, while an explicitly admitted
admin succeeds and an admitted non-member still fails RBAC. This is source and
local transport evidence, not a live browser deployment acceptance result.
Any separately authorized tenant-RBAC opt-in needs an explicit configuration
design; it must not be inferred from an omitted list.

## Method classification in this change

| Methods | Scope and reason |
| --- | --- |
| `settings/relay-policy.apply`, `settings/relay-admin.call`, `config/reconcile`, `config/reload`, `config/status` | Operator: change fleet policy or invoke NIP-86 using service credentials, including status reads. The four mutation gates predated this change. |
| `settings/relay-policy.get` | Admitted read: serves the relay-policy projection, does not invoke NIP-86 or publish commands. No additional method-level operator gate. |
| `backup/repository-register`, `backup/policy-apply`, `backup/recipe-apply`, `backup/definition-apply`, `backup/run`, `backup/verification`, `backup/restore`, `backup/retention`, `approval/backup-restore-approve`, `backup/repository-probe` | Operator **and** existing tenant `backups:manage` check. Backup resource IDs do not yet have an enforced tenant-ownership model; claiming a tenant must not permit arbitrary repository operations. |
| `loom/submit` | Operator: arbitrary container workloads on fleet workers; service/environment labels are not tenant authorization. |
| `loom/cancel` | Recorded submitter or explicit `loom.authorized_pubkeys` operator, with a known submitter required. This is requester ownership, not tenant RBAC. An empty Loom operator list allows only the recorded submitter; transport admission is always required first. |
| `assistant/prompt`, `assistant/approval` | Operator plus existing session-participant checks; Assistant can issue service-signed fleet operations. |
| `service/deploy-preview`, `service/deploy`, `service/route-attach`, `service/rollback` | Tenant RBAC: verified signer needs deployment-write permission on both the service and environment, with matching organization ownership. Unchanged. |
| `approval/approve`, `approval/reject` | Tenant RBAC: resolve the intent and require deployment-approval permission on its service and environment. Existing release-promotion policy remains intact. Unchanged. |

DNS, workers, SBOM, security, continuity, policy, package and tool approval retain
their existing operator gates. `service/action` and adoption retain their
separate operator allowlists; they are not the tenant service methods above.

## Backup delegation

The existing `bahia.backup.delegation.v1` record and matching tags are generated
from the verified ContextVM event after operator and tenant authorization.
Caller-supplied requester/delegation fields cannot replace that record. The
service signs the downstream command as its issuer, not as the requester.

The consumer validates the issuer against the configured service signer, the
event ID/signature, and the record/tag agreement. It then checks the **recorded
requester** against its allowlist, not the outer signer. The service key cannot
be the delegated requester or issue an undelegated backup command, even if
Assistant wiring placed that key in the reactor allowlist. Durable run,
restore, retention and approval attribution continues to use the requester.
This does not introduce a new backup ownership model or a two-person approval
policy.

## Loom ownership

Only the verified event signer supplies the submitter. Record that identity
before starting canonical projection. Cancellation ignores claimed submitter
or requester fields and uses the client's recorded owner and the existing
`loom.authorized_pubkeys` operator list. Admission via `nostr.authorized_pubkeys`
does not by itself grant cancellation of another submitter's job. The service signer
cannot submit or cancel on its own authority, even if explicitly allowlisted.

The existing client ownership map is in-memory. After a restart or for an
untracked job, ownership is unknown and cancellation fails closed, including
for operators. Durable ownership recovery and tenant service linkage remain
outside this control-plane change.
