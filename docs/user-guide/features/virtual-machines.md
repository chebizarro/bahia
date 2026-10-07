# Virtual Machines and Execution Planes

Bahia exposes typed virtualization hosts, immutable VM images, persistent VMs, execution planes, checkpoints, exports, and operations. The `virtualization` configuration is opt-in.

## Lifecycle boundary

- `persistent_vm` resources are governed by Bahia.
- Loom owns ephemeral Firecracker and QEMU job guests.
- Bahia manages execution-plane package/config/image pins, capacity, probes, and authenticated capability evidence; it does not take ownership of Loom job domains.

Capability wishes are not grants. Scheduling uses fresh authenticated probe evidence. Failure, expiry, session replacement, or relay closure retracts eligibility until fresh evidence arrives.

## Configuration

Configure explicit operator pubkeys, registered hosts and trusted artifact signers, one persistent provider, the reconciliation pubkey, administrative plane endpoints, and allowed networks/secret references.

Persistent providers require explicit local paths and provider settings. Credentials belong in authorized SecretRefs. Bahia does not fall back from a configured provider to host-shell commands or another provider.

## Reads

Every read requires an authenticated public-key principal and organization `deployments:read` permission.

| Resource | ContextVM method | HTTP path under `/api/v1` |
|---|---|---|
| Host | `virtualization-host/list`, `/get` | `/virtualization-hosts[/{id}]` |
| Image | `vm-image/list`, `/get` | `/vm-images[/{id}]` |
| Persistent VM | `persistent-vm/list`, `/get` | `/persistent-vms[/{id}]` |
| Execution plane | `execution-plane/list`, `/get` | `/execution-planes[/{id}]` |
| Checkpoint | `vm-checkpoint/list`, `/get` | `/vm-checkpoints[/{id}]` |
| Export | `vm-export/list`, `/get` | `/vm-exports[/{id}]` |
| Operation | `vm-operation/get` | `/vm-operations/{id}` |

Supply `org_id` for every query. Lists accept `limit` (default 50, maximum 100) and a nonnegative `offset`. HTTP provides reads only.

Public DTOs separate desired power, observed runtime, drift, ownership, and guest health. They expose connection metadata and fingerprints, never credentials or resolved secret values.

## Mutations and approval

Registered ContextVM methods include `vm-image/register`, `persistent-vm/create`, `persistent-vm/register-adoption`, `persistent-vm/update`, `persistent-vm/operate`, `execution-plane/create`, `execution-plane/update`, `execution-plane/reconcile`, and `vm-operation/approve`, `approve-plan`, or `cancel`.

Writes require organization `deployments:write`, an allowed operator, current generation, and an idempotency key. Destructive or capacity-changing operations use a second authorized operator. The approval binds the exact request, resource generation, provider fingerprint, requester, and reason; it is single-use.

An `accepted` result contains correlation identifiers, not provider completion. Follow the operation's canonical state and audit facts.

## Measured enrollment

Enrollment of an existing VM is explicit:

1. Register the public resource description.
2. Measure identity and configuration from the provider.
3. Have a second operator approve the exact adopt plan.
4. Submit the identical `adopt` operation with the approval.
5. Follow operation state and remeasurement through recovery.

A name or matching directory is not sufficient identity.

## Canonical evidence

Virtualization state uses service-authored `30900` records with `schema=bahia.state.virtualization.v1`. Audit uses `4903` with `schema=bahia.audit.virtualization.v1`. Addressable coordinates identify hosts, images, VMs, planes, checkpoints, exports, and operations.

`bahia_virtualization_*` metrics summarize repository and operation state. Metrics are observability, not authorization or capability evidence.

## Related

- [Environments](environments.md)
- [Workers](workers.md)
- [Nostr Integration](../nostr-integration.md)
