# SoulFactory Nostr Agent Lifecycle — Plan

> **Status**: Ready for implementation planning
> **Date**: 2026-05-14
> **Scope**: Bahia SoulFactory, Bahia Souls UX, OpenClaw harness provisioning, and Swarmstr/Metiq Nostr-native harness lifecycle management

---

## Goal

Upgrade SoulFactory into a Nostr-native provisioning and lifecycle control plane for AI agent harnesses, starting with OpenClaw and Swarmstr/Metiq. Bahia should let operators create, customize, observe, and govern agents — identity, avatar/voice references, permissions, relay policy, runtime, repository, memory, deployment, and lifecycle — through signed Nostr events only, with no REST control APIs and no polling-based status semantics.

## Background

- Bahia already has a Souls web flow and Nostr event vocabulary for templates, agent souls, drafts, provisioning requests/status/results, and lifecycle actions: `bahia/web/src/lib/nostr/client.js:6-15`, `bahia/web/src/routes/souls/new/+page.svelte:119-203`, `bahia/web/src/lib/stores/souls.svelte.js:172-286`.
- SoulFactory's Go domain already models the core lifecycle: event kinds `31950/31951/31952/5950/6950/7950/1950`, `AgentSoul`, provisioning runs, eight provisioning steps, lifecycle states, actions, identity, permissions, avatar, Qdrant, workspace, and Bahia deployment fields: `bahia/internal/domain/soul.go:16-158`.
- The current backend reactor/provisioner works but is not yet robust enough for multi-runtime lifecycle management: it subscribes only with `Since: now`, filters only by kind, stops when subscriptions close, logs publish failures without requiring relay `OK`, duplicates lifecycle ownership, and can publish `31951` before Bahia service/deploy fields are final: `bahia/internal/soulfactory/reactor.go:128-158`, `bahia/internal/soulfactory/reactor.go:580-591`, `bahia/internal/soulfactory/provisioner_full.go:319-351`, `bahia/internal/soulfactory/lifecycle_handler.go:40-189`.
- Swarmstr/Metiq has the strongest Nostr-native runtime patterns in the workspace: task/control/result/lifecycle/capability/state kinds, signed control request/result flow, capability publication, validation, dedupe, reconnect, AUTH, and per-relay acceptance handling: `swarmstr/internal/nostr/events/kinds.go:6-48`, `swarmstr/internal/nostr/runtime/control_bus.go:134-767`, `swarmstr/internal/nostr/runtime/capability.go:202-253`.
- OpenClaw exposes the harness abstraction SoulFactory needs to provision: V1/V2 harness lifecycle, plugin registration, config-level identity, ACP runtime sessions, permission controls, and session-spawn surfaces: `openclaw/src/agents/harness/types.ts:1-85`, `openclaw/src/agents/harness/v2.ts:20-255`, `openclaw/src/config/types.agents.ts:15-146`, `openclaw/src/agents/tools/sessions-spawn-tool.ts:28-240`.
- Prior SoulFactory PSTF work already verified the first Nostr-native lifecycle slice, including signer-first `5950`, correlated `6950/7950`, signed `1950`, and no timeout/ticker terminal semantics: `bahia/pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/verification_report.md:1-62`.

## Approach

Keep Bahia's existing SoulFactory kinds as the user-facing contract and use Swarmstr-style runtime control kinds behind the scenes.

### Protocol shape

- Keep the existing SoulFactory UX/API kinds:
  - `31950` — Soul template
  - `31951` — Agent soul read model
  - `31952` — Soul draft / editable structured spec
  - `5950` — Provisioning request
  - `6950` — Status/progress for any correlated SoulFactory request
  - `7950` — Terminal result for any correlated SoulFactory request
  - `1950` — Soul lifecycle/customization action request
- Reuse `6950/7950` for lifecycle actions instead of adding new action result kinds. Distinguish provisioning vs lifecycle with tags such as `request-kind`, `action`, `soul`, and `agent-id`, all correlated by `#e` to the original request event.
- If existing callers already consume the implicit `KindSoulAction + 1` result, support it as a migration alias only; do not make it the new primary contract.
- Adopt Swarmstr/Metiq runtime kinds for backend-to-runtime control:
  - `30317` — Runtime capability announcement
  - `38384` — Runtime control request
  - `38386` — Runtime control result

### Trust and authorization

- Operators sign Bahia-facing `31952`, `5950`, and `1950` events.
- The SoulFactory service key signs runtime-facing `38384` events after validating the operator request.
- OpenClaw and Metiq runtimes trust only configured SoulFactory controller pubkeys, advertised via configuration and optionally discoverable from capability metadata.
- Runtime requests must include the original operator request ref (`e`), soul ref, controller pubkey, target runtime pubkey (`p`), idempotency key, and resolved spec hash.
- Runtime bridges must reject unsigned, self-authored, stale, duplicate, or unauthorized `38384` events before touching local agent/session state.

### Draft/read-model reconciliation

Make `31952` the canonical editable desired spec and `31951` the authoritative latest observed/read model.

- Every provisioning or update action captures an exact draft event id and spec hash at start.
- In-flight draft edits create a newer `31952`, but they do not mutate the active run; they require a later `1950 update` or new provisioning request.
- `31951` carries `draft`, `spec-hash`, runtime binding, capability ref, deploy status, and last terminal result ref.
- Only SoulFactory publishes `31951`; runtimes report state through `38386` and optional runtime lifecycle events.
- On replay or reconnect, SoulFactory checks for an existing terminal `7950` before re-executing external side effects.

### Initial structured spec

Keep the first schema narrow enough to ship a vertical slice, but reserve additive fields for the full agent studio.

Initial `31952` JSON should cover:

- identity: name, purpose, tier, optional NIP-05 target;
- runtime target: `openclaw` or `metiq`;
- permissions: allowed Nostr kinds, tool grants, approval policy;
- relay policy: read/write/control relays, with NIP-65 discovery where available;
- repository/workspace: source repo, branch, environment binding;
- avatar and voice references only: existing uploaded/generated asset refs, not full generation workflows yet.

Defer advanced avatar generation, voice-provider selection, memory tuning, and full UI customization until the protocol slice is proven.

### Backend reliability

Refactor SoulFactory relay handling before broadening capabilities:

- scoped subscriptions, not broad kind-only filters;
- EOSE-aware backfill followed by realtime handling;
- reconnect with exponential backoff and subscription reissue;
- NIP-42 AUTH handling;
- inbound event validation and dedupe by event id;
- idempotent request handling through terminal-result lookup;
- publish success only when at least one relay returns accepted `OK`.

## Work Items

1. **Lock the product contract with PSTF artifacts**
   - Add `/pstf/features/SOUL_FACTORY_RUNTIME_LIFECYCLE/feature_spec.json`, acceptance criteria, test matrix, defects placeholder, HITL decisions, and verification report scaffold.
   - Map every acceptance criterion to tests before implementation starts.
   - Preserve the existing no-polling/no-timeout terminal semantics from `SOUL_FACTORY_PROVISIONING_TRACKING`.

2. **Write the runtime control contract before bridge code**
   - Add `bahia/docs/soulfactory-runtime-control.md` defining `soulfactory.provision`, `soulfactory.update`, `soulfactory.suspend`, `soulfactory.resume`, `soulfactory.redeploy`, and `soulfactory.revoke`.
   - Specify request params, result payloads, error shape, idempotency key, required tags, trust model, and compatibility rules for OpenClaw TypeScript and Metiq Go implementations.
   - This document is the shared schema source for Work Items 8–9.

3. **Extend the SoulFactory domain contract**
   - Update `bahia/internal/domain/soul.go` with runtime kind aliases, structured draft/runtime/relay/permission specs, spec hash fields, migration handling for legacy lifecycle results, and `update` lifecycle action.
   - Keep all existing event kinds and legacy request fields valid.
   - Mirror new constants and parsers in `bahia/web/src/lib/nostr/client.js`.

4. **Add a resilient SoulFactory relay bus with an isolated validation gate**
   - Create `bahia/internal/soulfactory/relay_bus.go` for publish/query/subscribe transport.
   - Support scoped filters, EOSE backfill, realtime transition, reconnect/backoff, AUTH, CLOSED handling, OK enforcement, and dedupe.
   - Add focused relay-bus tests before wiring lifecycle/runtime work: OK false, all-relay reject, EOSE transition, CLOSED/auth-required, duplicate EVENT, reconnect/reissue.
   - Rewire `bahia/internal/soulfactory/reactor.go` to use the bus while preserving current provisioning behavior.

5. **Centralize event parsing and building**
   - Create `bahia/internal/soulfactory/event_codec.go` for provisioning requests/status/results, action requests/status/results, drafts, soul read models, and runtime-control envelopes.
   - Remove duplicated parsing/building from `reactor.go`, `lifecycle_handler.go`, and `provisioner_full.go`.
   - Add tests for legacy and new event shapes.

6. **Unify lifecycle ownership**
   - Make `bahia/internal/soulfactory/lifecycle_handler.go` the only orchestrator for `1950`.
   - Split execution behind `ProvisioningEngine` and `LifecycleEngine`; `FullProvisioner` can implement both, but request parsing/publication should not live in the engine.
   - Publish action progress/results through `6950/7950` with lifecycle tags, and support legacy result aliases only as needed for existing callers.

7. **Ship runtime capability publishers first**
   - Add an OpenClaw Nostr bridge that can publish `30317` capability announcements and validate addressed requests, even before it executes full provisioning.
   - Add a focused Metiq bridge, likely `swarmstr/internal/nostr/runtime/soulfactory_bridge.go`, using the existing `ControlRPCBus` and capability publisher.
   - Keep Bahia runtime adapters disabled until both runtimes can advertise capability and controller trust correctly.

8. **Introduce Bahia runtime adapters**
   - Add `bahia/internal/soulfactory/runtime_adapter.go` plus OpenClaw and Metiq implementations.
   - Discover runtime capabilities from `30317` and select relays from explicit draft policy, capability hints, and NIP-65 relay lists.
   - Send signed `38384` requests as the SoulFactory service key and require correlated `38386` results.

9. **Make provisioning draft-backed and runtime-aware**
   - Resolve `5950` as template defaults + exact `31952` draft event + inline legacy overrides.
   - Keep the eight visible provisioning steps, but expand step 8 (`deploy`) to include runtime binding, Bahia registration/deployment intent creation, final `31951`, and terminal `7950`.
   - Publish final `31951` only after immediately-known runtime/Bahia fields are set.
   - Clarify the no-REST boundary: SoulFactory must not expose or depend on REST control APIs; in-process Bahia registry/deployment calls may remain if they are not the external agent control plane. If any required deploy path is REST-only, replace it with or defer to an event-native Bahia control-plane path.

10. **Implement OpenClaw provisioning/control execution**
    - In OpenClaw, add controller allowlist config and optional `AgentConfig.soulFactory` metadata in `openclaw/src/config/types.agents.ts` for managed agents, soul refs, owner pubkey, control relays, capability refs, and runtime bindings.
    - Dispatch the `soulfactory.*` methods from the bridge into existing config/session APIs.
    - Extract reusable session-spawn orchestration from `openclaw/src/agents/tools/sessions-spawn-tool.ts` rather than duplicating runtime/session logic.
    - Leave `openclaw/src/agents/harness/types.ts` and `openclaw/src/agents/harness/v2.ts` unchanged unless implementation proves the bridge cannot pass required metadata through existing contracts.

11. **Implement Metiq provisioning/control execution**
    - Register `soulfactory.*` handlers on the existing Swarmstr/Metiq control bus.
    - Reuse existing `30317` capability publication and `38384/38386` control flow; do not add duplicate event kinds.
    - Optionally emit `30316` lifecycle events for Metiq observability, while keeping Bahia-facing completion on `7950` and `31951`.

12. **Upgrade Bahia Souls UX as a vertical slice, not a full studio yet**
    - Extend `bahia/web/src/lib/stores/souls.svelte.js` to dedupe replaceable souls/templates/drafts/capabilities, track lifecycle runs through `6950/7950`, publish drafts, and publish structured `update` actions.
    - Convert `bahia/web/src/routes/souls/new/+page.svelte` into a draft-backed runtime-aware wizard for identity, runtime, permissions, relays, repository, and optional avatar/voice refs.
    - Update list/detail/edit routes to surface runtime capability, deploy state, and explicit lifecycle progress/results.
    - Gate runtime choices on discovered `30317` capabilities so the UI cannot provision unsupported targets.

13. **Verify before expanding customization knobs**
    - First slice: one OpenClaw target and one Metiq target can publish capabilities, be selected in Bahia, receive provisioning via `5950 → 38384`, publish `38386`, produce final `31951/7950`, and handle one lifecycle `1950 → 7950` action.
    - Extend Bahia Go tests around reactor, relay bus, codec, lifecycle handler, provisioner ordering, and runtime adapters.
    - Extend Bahia web unit/e2e tests around draft save, capability discovery, provisioning progress, lifecycle tracking, and no timeout/relay-close terminal failure.
    - Add OpenClaw bridge tests and Swarmstr bridge tests for capability publication, authorization, request validation, idempotency, and result correlation.

## Acceptance Criteria

- Bahia provisioning and lifecycle control use signed Nostr events only; no REST control path is introduced for OpenClaw or Metiq management.
- SoulFactory handles relay `EVENT`, `EOSE`, `OK`, `CLOSED`, and `AUTH` explicitly and never treats timeout or relay closure as terminal success/failure.
- The relay bus has isolated tests before runtime/lifecycle work depends on it.
- `31952` drafts can express the initial runtime, identity, permissions, relay, repository, and avatar/voice reference fields.
- `31951` remains a backward-compatible authoritative read model and is published only after immediately-known runtime/Bahia fields are present.
- Runtime-facing `38384` events are signed by trusted SoulFactory controller keys, authorized by the runtime, idempotent, and correlated to `38386` results.
- OpenClaw and Metiq advertise capabilities via `30317` and accept the same documented `soulfactory.*` method contract.
- Bahia web can discover runtime targets, save drafts, provision agents, track `6950/7950`, issue lifecycle/customization actions, and avoid inferred timeout/relay-close terminal states.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Runtime bridges drift on payload shape. | Write `docs/soulfactory-runtime-control.md` before bridge implementation and test the same fixtures in Go and TypeScript. |
| Cross-repo rollout skew exposes unavailable runtime targets. | Capability-gate UI choices and backend adapter selection on live `30317` announcements; keep static runtime allowlists until both bridges are deployed. |
| Event-schema expansion breaks existing clients. | Make all tags/content additive, keep legacy `{ brief }`, keep `31951` content as `SoulMD`, and preserve current provisioning kinds. |
| Draft edits race active runs. | Capture draft event id + spec hash at run start; newer drafts require explicit update/reprovision actions. |
| Lifecycle behavior diverges between handlers. | Make `lifecycle_handler.go` the only `1950` state machine; engines perform side effects only. |
| Runtime or Bahia side effects succeed but terminal Nostr publish fails. | Require relay `OK`, make execution idempotent, and on replay republish authoritative state/results instead of repeating external side effects. |
| OpenClaw bridge duplicates existing spawn/session logic. | Extract shared helpers from `sessions-spawn-tool.ts`; do not fork ACP/subagent orchestration. |
| Bahia deploy integration is REST-only under the hood. | Treat in-process Bahia calls as allowed internal implementation only; if a required path is external REST, replace or defer it behind event-native Bahia control-plane events. |

## Open Questions

- OpenClaw bridge placement should be validated during implementation: this plan assumes an embedded bridge is preferable to a sidecar because OpenClaw already owns config/session state.
- Voice provider schema, generated-avatar UX, and memory tuning should be handled as follow-up customization packs unless product input makes them mandatory for the first vertical slice.

## References

- `bahia/docs/soul-factory.md`
- `bahia/docs/control-planes.md`
- `bahia/docs/investigations/swarmstr-tool-provisioning-2026-05-02.md`
- `bahia/pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/verification_report.md`
- `bahia/docs/reviews/soulfactory-lifecycle-plan-critique-2026-05-14.md`
- `openclaw/src/agents/harness/types.ts`
- `openclaw/src/agents/harness/v2.ts`
- `openclaw/src/config/types.agents.ts`
- `openclaw/src/agents/tools/sessions-spawn-tool.ts`
- `swarmstr/internal/nostr/runtime/control_bus.go`
- `swarmstr/internal/nostr/runtime/capability.go`
- NIP-01: https://github.com/nostr-protocol/nips/blob/master/01.md
- NIP-42: https://github.com/nostr-protocol/nips/blob/master/42.md
- NIPs index: https://github.com/nostr-protocol/nips
