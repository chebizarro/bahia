# VM runtimes

Bahia manages persistent virtual machines through explicit virtualization
resources and also supports `vm-qemu` and `vm-firecracker` service runtime
types. Both paths share the libvirt/Firecracker provider machinery, immutable
image releases and signed operation records.

## Resource model

Virtualization resources are tenant-scoped:

- host;
- VM image;
- persistent VM deployment;
- execution plane;
- checkpoint;
- export;
- operation.

Current state is published as kind-`30900` virtualization topics and indexed in
PostgreSQL when available. Authenticated HTTP GET routes expose projections;
mutations use the registered virtualization control methods:

`virtualization-host/list|get`, `vm-image/list|get|register`,
`persistent-vm/list|get|create|register-adoption|update|operate`,
`execution-plane/list|get|create|update|reconcile`,
`vm-checkpoint/list|get`, `vm-export/list|get`, and
`vm-operation/get|approve|approve-plan|cancel`.

Every mutation is tied to the requesting principal, organization, resource
generation and idempotency key. Provider operations record an operation ID and
verify observed state before completion.

## Providers

| Runtime/provider | Host requirements | Image format |
|---|---|---|
| `vm-qemu` / `libvirt` | libvirt, `virsh`, `qemu-img`; `qemu:///system` or `qemu:///session` | `qcow2` release with verified digest |
| `vm-firecracker` / `firecracker` | Linux, KVM, Firecracker binary, instance/image state directories | `firecracker-rootfs` release with verified kernel and rootfs digests |

Firecracker persistent networking is isolated and uses vsock/console. Libvirt
builds a per-instance qcow2 overlay over the verified base image and stores a
Bahia ownership marker in domain metadata.

## Configuration

The resource-managed persistent provider is opt-in:

```yaml
virtualization:
  operator_pubkeys:
    - <64-hex-operator-pubkey>
  reconcile_pubkey: <64-hex-reconciler-pubkey>
  hosts:
    - org_id: <organization-uuid>
      host_id: <host-uuid>
      trust_policy_ref: <policy-uuid>
      trusted_signers: [<64-hex>]
      networks: []
      plane_secret_refs: []
  persistent_vm:
    enabled: true
    state_dir: /var/lib/bahia/persistent-vms
    image_root: /var/lib/bahia/vm-images
    libvirt_uri: qemu:///system
    event_socket: /run/libvirt/libvirt-sock
    firecracker_binary: /usr/bin/firecracker
```

Host and resource UUIDs are explicit; Bahia does not infer them from domain or
container names. Runtime settings for service environments use `runtime.vm`
and are valid only for `vm-qemu` or `vm-firecracker`.

## Image releases

A VM image release is immutable and digest-pinned. `qcow2` manifests identify
the disk; `firecracker-rootfs` manifests identify both kernel and rootfs and
carry SHA-256 values for each. Bahia verifies the manifest and component
digests before defining an instance.

Publish release bytes to the configured artifact storage, register the image
resource under the tenant, and reference its resource ID/generation from the
persistent VM. Do not point desired state at an unversioned mutable path.

## Lifecycle and safety

Persistent operations include create/define, start, stop, restart, update,
checkpoint, restore/transfer, export and delete according to provider support.
Destructive and high-risk operations require the configured approval tier.

Bahia refuses ownership mismatches, stale generations, invalid provider/image
pairs and unconfirmed provider outcomes. An `unconfirmed` result is not
success; reconcile observation before retrying a destructive operation.

Adoption (`persistent-vm/register-adoption`) records a measured existing
instance without claiming a different owner's resource. The libvirt and
Firecracker adapters verify provider identifiers, image/config evidence and
Bahia ownership markers before accepting later mutations.

Deletion can require an export and digest verification before provider
teardown. Preserve operation, checkpoint and export records until the
corresponding canonical events and provider observations agree.

## Observation

Observations report availability, power, guest health, drift, ownership and
optional measured resource pressure. Metrics label only bounded provider and
lifecycle values. Guest logs/console paths remain provider-specific and must
not be copied into public relay content.

Troubleshoot in this order:

1. resource generation and tenant authorization;
2. host enabled state, capacity observation freshness and trust policy;
3. image manifest/component digests;
4. provider socket/binary and storage permissions;
5. operation record, provider correlation ID and latest observation;
6. canonical `30900` projection and `/ready` subsystem checks.
