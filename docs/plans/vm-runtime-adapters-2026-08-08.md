# VM Runtime Adapters: Deploy & Monitor VMs Through Bahia

Status: draft plan
Date: 2026-08-08
Related: `loom-worker/docs/plans/vm-job-isolation-2026-08-01.md`, `docs/plans/desired-state-runtime-architecture-2026-05-26.md`

## Goal

Let bahia deploy and monitor long-lived VM instances — Firecracker microVMs and QEMU/KVM domains via libvirt (including Windows guests) — as environment services, with the same lifecycle, observation, drift, and log surfaces that docker/podman containers get today.

## Background

**Bahia's runtime contract** (what a new adapter must satisfy):
- `Observer.Observe`, `Runtime` (`Type`/`Deploy`/`Undeploy`/`StreamLogs`), optional `LifecycleRuntime` (`Restart`/`Stop`) — `internal/adapters/runtime/docker.go:25-61`; lifecycle type-asserts `LifecycleRuntime` at `internal/service/runtime_lifecycle.go:689-700`.
- Runtime types are a closed set (`docker|compose|kubernetes|podman`) — `internal/domain/models.go:110-118`, `internal/domain/validate.go:87-96`; adapters are constructed by a switch factory `internal/adapters/runtime/factory.go:28-100` from `RuntimeConfig` (`factory.go:13-26`), resolved with layered precedence in `internal/adapters/runtime/resolver.go:50-133`.
- Artifacts are UUID-identified OCI records (`ImageRepo` + `ImageDigest`/`ImageTag`) — `internal/domain/models.go:273-288`; deploy image string built at `internal/service/runtime_lifecycle.go:1261-1269`.
- Observations (`domain.RuntimeObservation`, `models.go:331-348`) are recorded via `RecordObservation` → environment-service state, drift, events (`internal/service/registry.go:1555-1659`); live logs flow through `StreamLogs` → SSE (`internal/api/handlers/logs.go:181-276`).

**cascadia-go VM machinery** (module `git.sharegap.net/cascadia/cascadia-go`):
- `vmexec.Engine.Run` is strictly one-shot job execution (`worker/vmexec/vmexec.go:69-76`); VM create/start/stop/teardown are internal (`worker/vmexec/firecracker/platform.go:13-34`, `worker/vmexec/libvirt/platform.go:18-38`). Bahia needs persistent, observable VMs, so the engines are a pattern source, not a direct dependency for lifecycle.
- Reusable exported packages: `worker/vmexec/protocol` (NDJSON guest protocol v1: hello/execute/stdio/completed — no ping, no metrics; `worker/vmexec/protocol/frames.go:12-77`) and the host engine configs. Guest agent binary is `cmd/cascadia-guest-agent` (vsock listener, one connection, one command).
- Image pipeline: hash-pinned release dirs with `manifest.json` (`image_id`, `arch`, `sha256.*`, `agent_protocol_version`, inventory) and atomic `current` symlink install/rollback — `packaging/firecracker/scripts/build-manifest.sh:8-19`, `install-image.sh:4-20`.
- Libvirt engine already polls `virsh domstate` internally (`worker/vmexec/libvirt/platform_linux.go:359-392`) — the exact primitive Observe needs.

**Prior art**: no VM design exists in bahia. The desired-state runtime architecture plan (2026-05-26) and runtime control-client seam (commits `4d66c9ac`, `114df483`) define the additive-capability pattern to follow. The loom VM-isolation plan canonicalizes `vm/firecracker`; `vm/qemu-kvm` was introduced by cascadia-go's libvirt engine (fp-0y6). Bahia beads use the `bahia-` prefix (own DB); cascadia-go work is tracked under `fp-`.

## Decisions (settled with owner)

1. VM lifecycle lives in **new runtime adapters inside bahia** (`internal/adapters/runtime/vm/...`), not in a cascadia-go daemon.
2. **Both engines** ship behind one shared VM-runtime abstraction: libvirt/QEMU (Linux + Windows/UEFI) and Firecracker (Linux microVMs).
3. **Guest-level metrics are in scope** via cascadia-guest-agent (protocol extension in cascadia-go).
4. Artifact model: recommendation below (Approach §3).
5. **VMs run on the host where bahia executes** — local `virsh`/local `state_dir`. Remote VM hosts would reshape config/resolver and the Observe transport; explicitly out of scope for v1.
6. **V1 VM adapters use the legacy `Runtime.Deploy` path and do NOT implement `DesiredStateApplier`** (`internal/adapters/runtime/desired_state_capability.go:23`). Legacy drift compares desired artifact digest vs observed digest + health (`internal/service/registry.go:1555-1659`), which works unchanged with manifest-hash digests. Desired-state/`NormalizedObservation` support (container-shaped today, `runtime_desired_state.go:983-1008`) is an explicit follow-up.

## Approach

### 1. Runtime types and config

Add two runtime types to the closed set: `vm-firecracker` and `vm-qemu` (`domain.RuntimeType`, `models.go:110-118`; validator `validate.go:87-96`; compile-time coverage list `factory_test.go:410-420`). Two explicit types (rather than one `vm` + mechanism field) keeps the resolver/factory switch and per-environment `type:` config shape unchanged. Map them to loom's canonical mechanism names `vm/firecracker` and `vm/qemu-kvm` in docs/labels.

Extend the config surface (`internal/config/config.go:563-621`, resolver targets `resolver.go:167-200`, `RuntimeConfig` in `factory.go:13-26`) with a `vm` block: `state_dir`, `image_root`, `libvirt_uri` (qemu only), `vsock_guest_port`, resource defaults (vcpus, memory), network profile. Unknown-for-type fields are rejected, matching the explicit-failure convention from the desired-state plan.

### 2. Shared VM runtime core + two hypervisor drivers

New package `internal/adapters/runtime/vm`:

- `vm.Runtime` implements `Observer`, `Runtime`, `LifecycleRuntime` once, delegating to a small `Hypervisor` driver interface: `Create(spec) (InstanceID, error)`, `Start`, `Stop(graceful bool)`, `Destroy`, `State(name) (VMState, error)`, `List(prefix)`, `ConsoleLogPath`, `VsockDial(name, port)`.
- Instance naming: `bahia-<envID-short>-<serviceName>` (parallel to container naming), giving Observe/Undeploy a deterministic lookup key — same role as the libvirt domain-name scheme in cascadia-go (`platform_linux.go:257-270`).
- **libvirt driver**: persistent domains (`virsh define` + `start`, not the transient `create` used for one-shot jobs), qcow2 overlay per service instance, UEFI/NVRAM for Windows images, `virsh domstate`/`list` for State/List. Reuse the CGO-free virsh boundary pattern from cascadia-go's engine.
- **firecracker driver**: supervised long-lived VMM per service — a small per-instance supervisor (spawned `bahia-vm-shim` or in-process goroutine with pidfile + API socket under `state_dir/instances/<name>/`) so VMs survive Deploy-call completion and can be found again by Observe after bahia restarts. State = VMM process liveness + guest-agent ping.
- Per-service state dir with metadata.json (artifact ID, image release, spec hash) — this hash feeds `NormalizedState`/drift.

### 3. Artifact model (recommendation)

Keep bahia's artifact identity untouched (UUID → `ImageRepo`/`ImageDigest`), and make the digest point at a **VM image release manifest** rather than an OCI image:

- Extend cascadia-go's packaging manifest with a `format` field (`firecracker-rootfs` | `qcow2`) and qcow2/UEFI fields for QEMU/Windows; keep sha256 pinning + atomic `current` symlink semantics.
- `ImageRepo` = release channel path under the host's `image_root` (or fetchable URL later); `ImageDigest` = `sha256:<hex>` of the canonical `manifest.json` bytes. Deploy receives `repo@sha256:...` (built at `runtime_lifecycle.go:1261-1269`), splits on `@`, resolves `<image_root>/<repo>/current` (following the symlink once), recomputes the manifest hash, and fails explicitly on mismatch — mirroring cascadia-go's runtime verification (`platform_linux.go:452-522`). Tag-only artifacts (no digest) and malformed refs are rejected inside the vm core with explicit errors before any hypervisor call; registry-auth handling is bypassed for `vm-*` runtime types (filesystem repo paths never hit the OCI client).
- This keeps intent/state/drift machinery unchanged: observed "image digest" is the manifest hash of the running instance's release.

### 4. Observe + guest metrics

`Observe` composes, per instance:
1. Hypervisor state (`virsh domstate` / VMM process liveness) → running/stopped.
2. Guest-agent **ping** over vsock → healthy vs degraded.
3. Guest-agent **metrics** (CPU %, mem used/total, disk used/total, uptime) → `RuntimeObservation.Metadata`.

Agent expectation is declared per image: manifests carry `agent_protocol_version`; a manifest declaring `agent: none` (or a pre-v2 version) makes hypervisor-running sufficient for `healthy` — so Windows guests are not permanently degraded before fp-1mn lands. Images that declare a v2 agent require a successful ping for `healthy`.

Requires a cascadia-go protocol v2 (backward-compatible additions): `ping`/`pong` and `metrics_request`/`metrics_report` frames, multi-connection acceptance in the guest agent (today it accepts exactly one connection, `cmd/cascadia-guest-agent/agent.go:25-26`), and a `--service-mode` flag so the agent keeps serving after boot instead of exiting after one exec. Bahia imports `git.sharegap.net/cascadia/cascadia-go/worker/vmexec/protocol` (exported, non-internal). Windows guests need the agent cross-compiled (existing bead fp-1mn).

### 5. Logs

`StreamLogs` tails the hypervisor console log (firecracker log FIFO / libvirt serial console file) and emits `LogEntry` values (stream=stdout, parsed timestamps where present), with `Tail`/`Follow` semantics matching the Docker collector (`docker.go:439-514`). Guest-agent-based structured log streaming is explicitly out of scope for v1.

## Work Items

Ordered; repo in brackets. Beads: bahia items under `bahia-`, cascadia-go items under `fp-`.

1. **[cascadia-go]** Guest protocol v2: `ping`/`pong`, `metrics_request`/`metrics_report` frames; guest agent service mode (multi-connection loop, metrics collection from /proc; Windows counterpart stubbed behind build tag). Version-negotiated via existing `hello.ProtocolVersion`.
2. **[cascadia-go]** Packaging: add `format` field + qcow2 flavor to the image manifest and install/verify scripts; document `vm/qemu-kvm` image layout (UEFI/NVRAM template).
3. **[bahia]** Domain plumbing: `vm-firecracker`/`vm-qemu` runtime types, validator, config schema + resolver target fields, `RuntimeConfig` extension, factory cases (explicit failure if unconfigured). Includes ALL closed-set touchpoints: DB migration extending the `runtime_type` CHECK (`internal/db/migrations/000038_deployment_units.up.sql:18`), MCP enums (`internal/mcp/server.go:291,327`), and e2e TypeScript unions.
4. **[bahia]** `internal/adapters/runtime/vm` core: `Hypervisor` interface, instance naming, state-dir/metadata layout, artifact→release-dir resolution + manifest verification, `Deploy`/`Undeploy`/`Restart`/`Stop` orchestration, spec-hash drift input.
5. **[bahia]** libvirt driver: persistent domain XML templating (incl. UEFI/Windows), qcow2 overlay per instance, virsh boundary, domstate-based `State`, console log wiring.
6. **[bahia]** firecracker driver: long-lived supervised VMM, pidfile/API-socket instance registry, boot + vsock handshake reuse, reap/adopt orphan instances on startup.
7. **[bahia]** Observe + metrics: vsock ping/metrics client over protocol v2, RuntimeObservation mapping (health, digest = manifest hash, metadata metrics), wired into `RecordObservation` flow unchanged.
8. **[bahia]** StreamLogs console tail collector + SSE verification.
9. **[bahia]** Tests: fake `Hypervisor` unit tests for the core; per-driver fakes at the virsh/VMM process boundary (pattern from cascadia-go engines); factory/resolver/validation coverage; KVM-host integration tests gated like fp-1jh.
10. **[release]** cascadia-go release: tag protocol-v2/packaging changes, bump bahia `go.mod` (currently pinned `v1.0.1`, `go.mod:8`) and the literal pin in `.gitea/workflows/deploy-edge.yml:65`.
11. **[docs]** Operator docs: configuring a `vm-qemu` environment (incl. Windows image prep) and a `vm-firecracker` environment; artifact publishing flow.

Dependencies: 3 → 4 → 5 → 7 → 8; 6 (firecracker) follows 5 — libvirt proves the `Hypervisor` interface first and gets persistence free from `virsh define`, while the firecracker supervised-VMM path is the riskier build. 1+10 gate 7; **item 2 is the first cross-repo blocker** (it gates item 4's qcow2 artifact resolution), with item 1 needed only by item 7. Code for 1–2 lands in cascadia-go first, but bahia consumes it only after the item-10 tag/pin bump — the release step is on the critical path, not an afterthought.

## Open Questions

- Does deploy-time image *distribution* (pulling a release dir onto the target host) belong in v1, or are image releases pre-provisioned on hosts out-of-band (as fp-tpt assumes)? Plan assumes pre-provisioned; distribution becomes a follow-up.
- Should `DeployOptions.Ports`/networking map to anything for VMs in v1 (tap devices / port forwards), or is networking deferred per the isolation plan's "no implicit networking" stance? Plan assumes deferred — VM services are vsock+console only in v1.
- When VM adapters later adopt `DesiredStateApplier`, `NormalizedObservation` needs VM-shaped fields — out of scope here, flagged for the follow-up plan.

## References

- `loom-worker/docs/plans/vm-job-isolation-2026-08-01.md` — isolation levels, `vm/firecracker` canon
- `docs/plans/desired-state-runtime-architecture-2026-05-26.md` — additive capability + explicit-failure conventions
- cascadia-go `worker/vmexec/` (engines, protocol), `packaging/firecracker/` (image pipeline)
- Beads: fp-qkk, fp-0y6, fp-fw9, fp-1jh, fp-tpt, fp-1mn (cascadia-go VM engine lineage)
