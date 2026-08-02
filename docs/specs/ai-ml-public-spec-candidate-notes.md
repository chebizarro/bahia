# AI/ML Public Spec Candidate Notes

> **Status annotation (2026-08-01): Superseded production transport candidate.**
> Bahia retains these numeric kinds as legacy compatibility inventory, but the
> production AI/ML mutation path now uses ContextVM kind `25910` and canonical
> status/control-state/audit observables (`30315`, `30900`, `4903`). Do not use
> the `38390-38399` or `31980-31989` ranges as current discovery guidance.

> Status: Draft implementation guidance
> Date: 2026-05-16
> Related tasks: `bahia-66ax.5.3`, `bahia-66ax.5.3.1`
> Scope: Candidate public event contract notes for Bahia AI/ML kinds `38390-38399` and `31980-31989`.

Bahia's generic AI/ML event family is a public-spec candidate track, not a blocking standardization dependency. Implementation slices may proceed while these notes capture which fields should be treated as stable, which fields remain implementation-proving details, and how replay, idempotency, and compatibility should work.

## Candidate namespace

- `38390-38399`: phase-1 addressable AI/ML command and terminal result events.
- `31980-31989`: phase-1 replaceable AI/ML registry, state, provenance, and capability read models.

These ranges intentionally avoid NIP-90's `5000-7000` Data Vending Machine request/result/feedback ranges. They are suitable as a Bahia public-spec candidate because they isolate generic AI/ML semantics from the stable LLM compatibility namespace and give implementations room to prove field names before any later NIP/BUD proposal.

Standardization is non-blocking: the Hugging Face -> vLLM and recipe-coordinator slices should implement against this documented contract, collect compatibility evidence, and only then prepare a public proposal.

## Stable fields and tags

Treat these as stable unless a later migration note explicitly supersedes them.

### Common Nostr envelope behavior

- Events are signed Nostr events and must pass normal event-id, signature, pubkey, and timestamp validation.
- Command/result events use addressable coordinates with a required `d` tag.
- Read models use latest-valid replaceable/addressable semantics for `(kind, pubkey, d-tag)`.
- Resource tags are used for relay-side filtering; consumers should not subscribe broadly and filter locally.

### Command events (`38390-38394`)

Stable command fields/tags:

- `kind`: one of `38390` recipe run, `38391` deploy, `38392` approval/rejection, `38393` rollback, `38394` model/model-version import.
- `d`: idempotency key or request id.
- Scoped tags as applicable: `model`, `model_version`, `recipe`, `run`, `endpoint`, `environment`, `deployment`, `artifact`, `worker`, `runtime`, `task`, `accelerator`.
- JSON `content` carries command intent and inputs; HTTP/MCP acknowledgements are not completion truth.

### Result events (`38395-38399`)

Stable result fields/tags:

- `kind`: terminal counterpart for the corresponding command family.
- `d`: result coordinate, preferably `result:<request_event_id>` unless a stricter family-specific coordinate is documented.
- `e=<request_event_id>` with reply semantics.
- `p=<requester_pubkey>`.
- `status=<queued|running|succeeded|failed|rejected>`.
- Same relevant scoped resource tags as the request.
- JSON `content` carries terminal payload or structured error details.

Consumers must check both the `status` tag and any content error fields.

### Read-model events (`31980-31989`)

Stable read-model fields/tags:

- `kind`: one of the documented AI/ML read-model kinds.
- `d`: stable coordinate, for example `model:<slug>`, `model-version:<model-slug>:<version>`, `recipe:<name>:<version>`, `recipe-run:<run-id>`, `endpoint:<name>:<environment>`, `endpoint-state:<name>:<environment>`, `artifact:<sha256>`, or `worker:<pubkey>:ai-capability`.
- Scoped tags for filtering and compatibility views, including `model`, `version`, `format`, `runtime`, `sha256`, `task`, `accelerator`, and `status` where applicable.
- JSON `content` is the projected read model for browser, REST, and MCP compatibility surfaces.

## Unstable fields

These details are expected to evolve during implementation slices and should not be treated as permanent public contract without additional evidence:

- Exact JSON content schema for recipe inputs, parameters, step checkpoints, and result payloads.
- Full artifact reference shape across Hugging Face, Blossom, OCI, SeaweedFS/S3, GitHub, local mirrors, and future stores.
- Runtime-specific deployment configuration fields for vLLM, llama-server, Ollama, ONNX Runtime, RKNN, Triton, TensorRT-LLM, and custom containers.
- Worker capability granularity beyond stable tags such as `runtime`, `artifact_format`, `task`, `accelerator`, `toolchain`, `ram_gb`, `vram_gb`, and `status`.
- Evaluation, benchmark, fine-tune, dataset import, and experiment command kinds, which are intentionally deferred.
- Whether future public specs split command status events from terminal result events. Phase 1 should use read models plus terminal results for progress/completion truth.

## Replay and idempotency behavior

Bahia processors and clients must be replay-safe and event-driven.

- Deduplicate every inbound event by event id.
- For addressable command replays, collapse by `(kind, pubkey, d-tag)` so client retries and relay replay do not start duplicate work.
- A repeated command with the same `d` coordinate is the same logical request unless implementation-specific validation rejects it as conflicting.
- Use the request event id for result correlation through `e` reply tags and result content where useful.
- Bootstrap clients with scoped `REQ` filters, wait for `EOSE` for historical catch-up, then keep subscriptions open for realtime updates.
- Do not use REST/MCP polling, arbitrary sleeps, or timeout-based completion to decide that a workflow has finished.
- Recipe steps should be idempotent by `(recipe_run_id, step_index, input_digest_set)`.
- Deployment work should serialize by `(endpoint_id, environment_id)` while allowing different endpoints/environments to proceed concurrently.
- Failed provenance, digest, runtime, toolchain, or hardware checks fail closed and publish terminal failure state instead of retrying blindly.

## Compatibility notes

- Existing LLM event kinds `5971-5975`, `6973`, `7971-7973`, and read models `31964/31965` remain stable compatibility surfaces.
- Generic AI/ML events must not mutate the LLM compatibility namespace into generic semantics.
- REST and MCP may initiate tooling-compatible flows, but successful synchronous responses must return Nostr correlation metadata: request event id, request kind, expected result kind, read-model kind(s), requester pubkey, and scoped tags.
- Browser, REST, and MCP views should derive long-running workflow truth from Nostr result/read-model events.
- Bahia may ingest Loom `10100` and Swarmstr `30317` capability advertisements, but should project normalized Bahia capability read models as `31989`.
- Tombstones for read models should use documented deleted markers such as `deleted=true`; do not rely on Nostr delete events for product state.

## Migration risks

- Public field churn: early clients may depend on content fields before vertical slices prove them. Mitigation: keep stable tags conservative and document content-schema changes.
- Namespace collision: future public NIP/BUD work could choose different ranges. Mitigation: keep adapter boundaries explicit and record migration mapping if the candidate changes.
- LLM compatibility regression: accidentally routing generic AI/ML behavior through existing LLM kinds could break `/llm` users. Mitigation: keep LLM kinds stable and implement ML-backed compatibility as a façade only when planned.
- Replay duplication: processors that treat retries as new work can double-run imports, recipes, or deployments. Mitigation: enforce `d`-tag idempotency and per-target serialization.
- Capability drift: workers may advertise incomplete runtime or hardware data. Mitigation: fail closed unless required runtime, format, hardware, toolchain, and digest facts are present.
- Rollback after ML-table cutover: after the planned canonical ML registry cutover, rollback requires DB snapshot restore. Mitigation: perform additive backfill and parity checks before cutover.

## Non-blocking public-spec candidate rationale

`38390-38399` and `31980-31989` are public-spec candidates because they describe relay-visible, interoperable AI/ML workflow semantics: model registry, recipe execution, inference deployment, artifact provenance, runtime capability, and terminal results. They should be documented carefully enough for sibling implementations and future reviewers to interoperate.

They are non-blocking because Bahia still needs implementation evidence before freezing a public proposal. The immediate requirement is a consistent internal/public candidate contract that enables phase-1 and phase-2 slices without waiting on external standardization.
