# Dematon Basic Agent Kind — Plan

> **Status**: Ready for implementation planning
> **Date**: 2026-05-17
> **Scope**: Bahia SoulFactory, assistant/Coxswain identity attribution, service/harness agent consumption, Nostr/Blossom-backed minimal agent definition

---

## Goal

Add **Dematon** as Bahia's minimal base agent kind: the smallest Nostr-native definition required for a tool-using agent with private-key-backed identity, permissions, and discoverable metadata that any Bahia service or harness can consume.

Dematon should make Bahia's own operator assistant/Coxswain an instance of the same base agent model rather than a hardcoded special case, without replacing richer SoulFactory agent souls.

## Background

- SoulFactory already defines richer agent souls, drafts, provisioning requests/results, runtime capabilities, and runtime control events (`31950/31951/31952/5950/6950/7950/1950/30317/38384/38386`): `internal/domain/soul.go:16-32`.
- `AgentSoul` already carries Nostr identity, Signet bunker URI, Blossom refs, permissions, runtime, relay policy, workspace, and assets: `internal/domain/soul.go:260-343`.
- SoulFactory's wire authority is `internal/soulfactory/event_codec.go`; it centralizes tag constants and builds/parses `31951`, `31952`, `5950`, `6950`, `7950`, and `38384`: `internal/soulfactory/event_codec.go:17-58`, `internal/soulfactory/event_codec.go:758-931`.
- The full provisioner already resolves drafts/templates, provisions Signet identity, optionally publishes Blossom/profile/memory/workspace/Bahia integrations, and emits final `31951`: `internal/soulfactory/provisioner_full.go:82-260`, `internal/soulfactory/provisioner_full.go:380-385`.
- The operator assistant currently uses a hardcoded `bahia-operator-assistant` id and service-key fallback attribution in assistant orchestration and MCP async tools: `internal/soulfactory/operator_assistant_bootstrap.go:10-55`, `internal/mcp/agent_async_tools.go:11-31`, `internal/app/app.go:1005-1037`.
- UI chat publishes assistant prompt/approval events (`38420/38421`), while the backend validates, plans, executes allowlisted `bahia_assistant_*` tools, and observes correlated terminal result events: `internal/domain/assistant.go:12-18`, `internal/service/assistant_orchestrator.go:136-438`.
- Signet is the intended private-key custody boundary for agent identities: Bahia stores pubkey/npub/bunker URI while Signet holds private keys: `internal/adapters/signet/client.go:1-4`, `internal/adapters/signet/client.go:43-64`, `internal/adapters/signet/client.go:151-225`.
- Prior lifecycle work verified the richer SoulFactory path and should be reused rather than forked: `pstf/features/SOUL_FACTORY_RUNTIME_LIFECYCLE/verification_report.md:24-32`, `docs/soulfactory-runtime-control.md:7-35`.

## Approach

Introduce Dematon as an additive **parameterized replaceable event** rather than a constrained view of `31951` or `31952`.

Recommended contract:

- `kind:31953`
- `schema: dematon/v1`
- `d=<agent_id>` as the stable agent coordinate
- `p=<agent_pubkey>,...,agent`, `npub`, and `bunker` for Signet-backed identity discovery
- `name`, `status`, and at least one permission surface (`allowed-kind` or `tool`)
- explicit `approval-policy`; v1 valid values should be limited to `human_approval` and `disabled`
- optional `purpose`, `nip05`, relay policy tags, runtime binding tags, asset refs, metadata refs, and `soul` source reference

Keep content JSON typed and tag it narrowly. Services/harnesses should be able to consume one `31953` document without reconstructing the full rich `31951` soul, while still validating the event signature, hash, timestamp, schema, required tags, permissions, and **acceptable author**. For v1, acceptable authors should be the configured SoulFactory/controller pubkey set; an authorless relay query is allowed only for bootstrap discovery and must reject events whose author is not subsequently recognized as trusted.

Dematon should be projected from rich SoulFactory souls, published alongside `31951`, and exposed through read-only client/MCP discovery APIs. It should not change runtime control kinds (`30317/38384/38386`), provisioning request/result kinds (`5950/6950/7950`), or assistant prompt/approval kinds (`38420/38421/38422/38423`).

## Work Items

1. **Lock the protocol contract and PSTF feature.**
   - Add `docs/dematon-agent-kind.md` defining `31953`, `dematon/v1`, required tags/content, trusted author policy, projection rules from `31951`, assistant bootstrap usage, and service/harness read expectations.
   - Add `pstf/features/DEMATON_BASIC_AGENT_KIND/` with the repo-required PSTF files. Keep early artifacts concise; complete `test_matrix.json` and `verification_report.md` as tests land.
   - Track implementation slices in Beads; this plan is tracked by `bahia-w157`.

2. **Add the Dematon domain and codec.**
   - Add `KindDematonAgent = 31953` near existing SoulFactory kind constants in `internal/domain/soul.go`.
   - Add `internal/domain/dematon.go` with `DematonSchemaV1` and a `DematonAgent` model reusing `SoulPermissionSpec`, `SoulRelayPolicySpec`, `SoulRuntimeSpec`, `SoulAssetRefs`, and `ToolGrant`.
   - Extend `internal/soulfactory/event_codec.go` with `BuildDematonEvent` and `ParseDematonEvent`, reusing existing tag constants where possible and adding only missing tags such as `metadata-ref`.
   - Tests: codec round-trip, required-field validation, schema rejection, optional Blossom/runtime/relay refs, and missing-permission rejection.

3. **Add reactor publish/query support.**
   - Add `PublishDematon(ctx, agent)` and `GetDematon(ctx, agentID)` to `internal/soulfactory/reactor.go`.
   - Query by `kind:31953` and `#d=<agentID>`; author-filter by configured trusted SoulFactory/controller pubkeys when available.
   - If bootstrap must query without an author filter, treat that as discovery only: parse and then reject events not signed by a configured or derived trusted controller pubkey.
   - Split the existing `GetSoul(...)` empty-author behavior into its own regression-tested fix because it changes `31951` lookup semantics for existing callers.
   - Tests: publish OK handling, latest-wins replaceable semantics within the trusted author set, untrusted author rejection, invalid event rejection, CLOSED/error handling, and `GetSoul(...)` empty-author regression coverage.

4. **Project Dematon from SoulFactory identity state.**
   - Add `internal/soulfactory/dematon.go` with `DematonFromSoul(...)`, `DematonBootstrapSpec`, and `EnsureDematonAgent(...)`.
   - Projection rules: copy identity fields, prefer structured `PermissionSpec`, carry relay/runtime/assets when present, set `SourceSoulRef` to the `31951` coordinate/event reference, and leave Blossom `MetadataRef` empty unless a stable ref already exists.
   - `EnsureDematonAgent(...)` should resolve existing trusted Dematon first, re-project from a newer trusted `31951` when the source soul is fresher, backfill from existing `31951` second, and provision a Signet identity only as the last model-backed path.
   - Define fallback advancement precisely: parse/validation/authorship failures are hard failures, absence advances to the next tier, and relay/CLOSED/AUTH failures surface explicitly rather than silently provisioning duplicates.

5. **Confirm Signet identity lookup/idempotency before Coxswain cutover.**
   - Verify whether `provision_agent` is idempotent by `agent_id` in `internal/adapters/signet/client.go`'s real backend path.
   - If it is not idempotent, add or track a Signet lookup-by-agent-id path before any startup flow can auto-provision the operator assistant.
   - Tests: duplicate bootstrap cannot create a second private-key identity for the same Dematon agent id.

6. **Publish Dematon with SoulFactory provisioning and lifecycle.**
   - In `internal/soulfactory/provisioner_full.go`, publish Dematon after final identity/runtime fields are known and before terminal `7950` success.
   - In `internal/soulfactory/lifecycle_handler.go`, replace single-model republish paths with a helper that republishes Dematon then `31951` for status/permission/ref-changing actions.
   - Treat Dematon publish failure as part of the same lifecycle transaction boundary: no terminal success until the identity projection and rich read model are both published or explicitly replayed.
   - Do not include high-frequency deploy status in Dematon v1; keep `internal/soulfactory/status_sync.go` focused on rich soul/deploy state.
   - Tests: provisioning emits Dematon and `31951` before success; suspend/resume/revoke/update republish Dematon; partial publish failure prevents terminal success and remains replay-safe.

7. **Migrate Coxswain/operator assistant identity resolution.**
   - Rework `internal/soulfactory/operator_assistant_bootstrap.go` so `bahia-operator-assistant` is only a bootstrap seed, not the downstream attribution source.
   - Update `internal/app/app.go` so startup resolves Dematon, backfills from `31951`, provisions Signet + Dematon if needed, and only then falls back to service-key identity if the model-backed path fails.
   - Update `internal/service/assistant_orchestrator.go` so production attribution comes from injected `AssistantIdentity`; add `agent-pubkey` tags when available while preserving existing assistant kinds.
   - Update `internal/mcp/agent_async_tools.go` and the MCP server constructor/config so assistant-safe command attribution uses injected assistant identity and fails closed when absent.
   - Tests: existing-Dematon path, `31951` backfill, Signet-provision-and-publish path, publish failure, injected agent id on async commands, missing identity failure.

8. **Expose Dematon to services and harnesses.**
   - Extend `internal/soulfactory/nostr_client.go` with `ListDematons` and `GetDematon`, using codec parsing and trusted-author replaceable-event dedupe.
   - Defer MCP discovery tools unless a concrete harness needs MCP rather than Nostr client access in the implementation slice; if needed, add read-only `soul_factory_list_dematon_agents` and `soul_factory_get_dematon_agent` in `internal/soulfactory/mcp_server.go`.
   - Keep provisioning/lifecycle APIs unchanged; Dematon v1 is a discovery and identity contract, not a new provisioning request path.

9. **Enforce approval policy at command execution boundaries.**
   - Treat `approval-policy` as live policy, not metadata: assistant orchestration and assistant-safe MCP command execution must reject tool execution when Dematon policy is `disabled`, and require the existing `38421` approval flow when policy is `human_approval`.
   - Keep valid v1 values intentionally small until a product decision expands policy semantics.
   - Tests: disabled policy blocks execution; human approval continues to require approval before async tool invocation.

10. **Verify through PSTF and regression gates.**
   - Acceptance criteria should cover signed `31953` persistence, Signet-backed identity metadata, Dematon publication with provisioning/lifecycle, independent service/harness queries, Coxswain model-driven identity, and bounded startup fallback.
   - Map every criterion to deterministic tests that inject events/OK/CLOSED/AUTH rather than sleeping for relay delivery.
   - Re-run SoulFactory lifecycle regressions to prove existing `31951/6950/7950/38384/38386` behavior remains intact.

## Risks and Decisions

- **New kind vs existing kind:** choose new `31953` because Dematon must be consumable without rich SoulFactory lifecycle semantics. `31951` remains the authoritative rich read model.
- **Private-key custody:** do not store private keys in Bahia or Blossom. Store public identity and bunker URI in Dematon; Signet remains custody/signing boundary.
- **Blossom scope:** Dematon may reference Blossom metadata/assets, but v1 should not require blobs. Requiring Blossom would make the minimum agent kind depend on optional asset storage.
- **Coxswain rollout:** use a staged startup fallback so existing deployments keep working, but remove hardcoded identity from downstream orchestration and MCP command attribution.
- **Runtime control:** do not alter `30317/38384/38386`; Dematon is identity/discovery, not a new runtime-control protocol.
- **Signet idempotency is a predecessor:** Coxswain cutover must not auto-provision until `provision_agent` idempotency or lookup-by-agent-id is verified. If unsupported, create a Beads issue for the Signet lookup/migration path and block the cutover on it.
- **Offline startup:** v1 assumes relay connectivity for model-backed startup. If Coxswain must start offline, add a local cache design before implementation; otherwise service-key fallback remains an explicit degraded-start path only.

## Open Questions

- Confirm the project accepts `31953` as Bahia's local Dematon kind allocation before implementation. If a shared NIP or cross-project registry later claims this range differently, the contract doc should record migration expectations.
- Confirm whether Coxswain must operate with no relay connectivity at startup. If yes, add a local trusted cache slice before migrating startup identity resolution.

## References

- `docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md`
- `docs/plans/llm-enabled-ux-foundation-2026-05-16.md`
- `docs/soulfactory-runtime-control.md`
- `docs/openclaw-soulfactory-sidecar.md`
- `pstf/features/SOUL_FACTORY_RUNTIME_LIFECYCLE/verification_report.md`
