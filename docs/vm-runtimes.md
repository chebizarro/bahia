# VM Runtimes: Operating vm-qemu and vm-firecracker Environments

Bahia can deploy and monitor long-lived VM instances as environment services with
the same lifecycle, observation, drift, and log surfaces containers get. Two
runtime types are available:

| Runtime type | Mechanism | Guests |
|---|---|---|
| `vm-qemu` | Persistent QEMU/KVM domains via libvirt (`virsh define` + `start`) | Linux and Windows (UEFI) |
| `vm-firecracker` | Long-lived supervised Firecracker microVMs | Linux microVMs |

These map to the fleet's canonical isolation mechanisms `vm/qemu-kvm` and
`vm/firecracker`.

**V1 scope.** VMs run on the host where bahia executes (local `virsh`, local
state dir). Instances are vsock+console only — no network interfaces are
attached, and deploy options that imply container semantics (ports, volumes,
env vars, command overrides, restart policies, `pull_always`) are rejected
explicitly. Image releases are pre-provisioned on the host out-of-band;
bahia never pulls VM images.

## Host prerequisites

### vm-qemu

- Linux host with KVM (`/dev/kvm`), libvirt daemon, `virsh`, and `qemu-img`.
- OVMF firmware for UEFI/Windows guests (default firmware code path:
  `/usr/share/OVMF/OVMF_CODE.fd`).
- `vhost_vsock` kernel module loaded (guest agent transport).
- The bahia user must be able to talk to the configured libvirt URI
  (default `qemu:///system`) and to create AF_VSOCK sockets.

### vm-firecracker

- Linux host with KVM and the `firecracker` binary on `PATH`.
- No jailer/network setup is required in v1; each instance gets a
  hybrid-vsock unix socket under its state directory.

## Configuring an environment

VM runtimes are selected per environment (or as the default target) in the
`runtime:` block of `config.yaml`. All `vm.*` fields are only valid for the
`vm-qemu` and `vm-firecracker` types; setting them on any other type is a
configuration error, as is `vm.libvirt_uri` on `vm-firecracker`.

### vm-qemu environment

```yaml
runtime:
  environments:
    windows-lab:
      type: vm-qemu
      vm:
        state_dir: /var/lib/bahia/vm/qemu          # per-instance state (overlays, nvram, console logs, metadata)
        image_root: /var/lib/bahia/vm-images       # release channels live under here
        libvirt_uri: qemu:///system                # optional; this is the default
        vsock_guest_port: 5000                     # guest-agent port; omit to disable guest probing
        vcpus: 4                                   # default instance size
        memory_mb: 8192
```

### vm-firecracker environment

```yaml
runtime:
  environments:
    microvms:
      type: vm-firecracker
      vm:
        state_dir: /var/lib/bahia/vm/firecracker
        image_root: /var/lib/bahia/vm-images
        vsock_guest_port: 5000
        vcpus: 2
        memory_mb: 2048
```

`state_dir` and `image_root` are required; `vcpus`/`memory_mb` default to
2/2048. Instances are named `bahia-<envID-short>-<serviceName>` (this is the
libvirt domain name / firecracker instance directory name).

After a bahia restart, the firecracker runtime reconciles its on-disk
instance registry automatically: still-running VMMs are adopted, dead ones
are reaped so their instances read as cleanly stopped. Persistent libvirt
domains survive restarts natively.

## VM image releases and artifact publishing

VM artifacts keep bahia's normal artifact identity (`ImageRepo` +
`ImageDigest`) but point at a **VM image release manifest** instead of an
OCI image:

- **`ImageRepo`** is the release *channel* path relative to the host's
  `image_root` (e.g. `cascadia/base-linux`).
- **`ImageDigest`** is `sha256:<hex>` over the release's canonical
  `manifest.json` bytes.

A channel directory contains hash-pinned release directories plus an atomic
`current` symlink:

```
<image_root>/cascadia/base-linux/
├── current -> fc-x86_64-20260808-abcdef
└── fc-x86_64-20260808-abcdef/
    ├── manifest.json
    ├── kernel            # firecracker-rootfs format
    └── rootfs.ext4
```

At deploy time bahia resolves `<image_root>/<repo>/current` (following the
symlink at most once), recomputes the manifest hash, and **fails explicitly**
when it does not match the artifact's pinned digest, when the manifest
format does not match the runtime type, or when any file hash in
`manifest.sha256` does not verify. Tag-only artifact references are
rejected — VM deploys require digest pinning.

### Publishing flow

1. **Build** a release with the cascadia-go packaging pipeline
   (`packaging/firecracker/` — `make all ARCH=x86_64` builds kernel, rootfs,
   guest agent, and `manifest.json`), or hand-build a qcow2 release
   (below).
2. **Install** it onto the VM host with the atomic installer, using the
   channel directory as the install root:

   ```sh
   packaging/firecracker/scripts/install-image.sh \
     out/releases/<image_id> /var/lib/bahia/vm-images/cascadia/base-linux
   ```

   The installer re-verifies every hash, copies into
   `<channel>/<image_id>/`, and atomically repoints `current`.
3. **Compute the artifact digest** from the installed manifest:

   ```sh
   printf 'sha256:%s\n' "$(sha256sum /var/lib/bahia/vm-images/cascadia/base-linux/current/manifest.json | cut -d' ' -f1)"
   ```
4. **Register the artifact** in bahia with
   `image_repo = cascadia/base-linux` and the digest from step 3 as
   `image_digest`, then attach it to the service intent as usual. The
   observed image digest reported by Observe is this same manifest hash, so
   drift detection works unchanged.

Rolling a channel forward is: install the new release (repoints `current`),
publish a new artifact with the new manifest digest, deploy. A deploy
pinned to an older digest fails loudly rather than silently booting the
wrong release.

### Manifest formats

`manifest.json` carries `image_id`, `arch`, `format`, `sha256.*`, and
`agent_protocol_version`:

- **`firecracker-rootfs`** (vm-firecracker): `kernel` + `rootfs.ext4`,
  hashes under `sha256.kernel` / `sha256.rootfs`. The base rootfs is cloned
  per instance.
- **`qcow2`** (vm-qemu): `disk.qcow2` (hash `sha256.disk`) and optionally
  `uefi-vars.fd` (hash `sha256.uefi_vars`). Each instance boots a qcow2
  overlay backed by the read-only base disk, so the release is never
  written to.

### Windows image preparation (vm-qemu, UEFI)

Windows guests boot UEFI. Prepare a golden image once, then package it:

1. Install Windows into a qcow2 disk under OVMF (e.g. with virt-install or
   virt-manager: OVMF_CODE + writable OVMF_VARS copy, virtio disk drivers
   loaded during setup). Generalize with `sysprep` if instances should not
   share machine identity.
2. Keep the **UEFI vars file** from that installation — it holds the boot
   entries. This becomes the release's `uefi-vars.fd` template; every
   deployed instance gets its own private copy of it (per-instance NVRAM).
3. Assemble the release directory:

   ```
   disk.qcow2      # the golden Windows disk
   uefi-vars.fd    # NVRAM/vars template from the install
   manifest.json
   ```

   with a manifest like:

   ```json
   {
     "image_id": "win2022-20260808",
     "arch": "x86_64",
     "format": "qcow2",
     "agent_protocol_version": 1,
     "sha256": {
       "disk": "<sha256 of disk.qcow2>",
       "uefi_vars": "<sha256 of uefi-vars.fd>"
     }
   }
   ```
4. Install into a channel with `install-image.sh` and publish as above.

When a release ships `uefi_vars`, bahia defines the domain with
`FirmwareCodePath` as the read-only pflash loader and a per-instance copy
of the vars template as NVRAM. Releases without `uefi_vars` boot BIOS.

**Windows guest agent:** until the guest agent is cross-compiled for
Windows, declare `agent_protocol_version: 1` (or omit it). Bahia then
treats hypervisor-running as sufficient for `healthy` instead of demanding
a guest-agent ping that can never succeed.

## Observation, health, and guest metrics

`Observe` composes two sources per instance:

1. **Hypervisor state** (`virsh domstate` / VMM process liveness) decides
   running vs stopped: `running → healthy`, `shut off/absent → stopped`,
   `paused/crashed → unhealthy`.
2. **Guest agent** (images declaring `agent_protocol_version >= 2`, with
   `vsock_guest_port` configured): bahia dials the guest over vsock and
   runs the protocol-v2 probe — hello handshake, `ping`/`pong`, and a
   metrics request. A running instance whose agent answers the ping is
   `healthy` and its observation metadata gains a `guest_metrics` map
   (`cpu_percent`, `memory_used_bytes`, `memory_total_bytes`,
   `disk_used_bytes`, `disk_total_bytes`, `uptime_seconds`). A running
   instance whose agent is unreachable degrades to `unhealthy`, with the
   probe error recorded under `guest_agent_error`.

Guest probing degrades gracefully: images without a v2 agent, runtimes
without `vsock_guest_port`, and probe failures never make Observe itself
fail — the hypervisor-state observation is always recorded.

For the probe to work, guest images must run `cascadia-guest-agent
--service` listening on the configured vsock port (the cascadia-go
firecracker rootfs pipeline wires this in).

## Logs

`StreamLogs` tails the instance's serial console log (libvirt serial/console
file, firecracker console log) with the same semantics as the container
collectors: `tail` bounds historical lines (default 100), `follow` streams
appended lines until the client disconnects, and per-line timestamps are
parsed when the guest prefixes lines with RFC3339 timestamps (falling back
to read time). Follow mode survives console log rotation/truncation. Live
logs are served over the same SSE endpoint as containers:

```
GET /services/{id}/environments/{envId}/logs?tail=200&follow=true
```

Guest-agent-based structured log streaming is out of scope for v1.

## Lifecycle

`Restart` and `Stop` request a graceful (ACPI) shutdown and wait, bounded
by the request context, before reporting success; `Undeploy` force-stops,
removes the hypervisor definition (including per-instance NVRAM), and
deletes the instance state directory. Redeploying a service replaces its
existing instance atomically from the freshly resolved release.
