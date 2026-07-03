# Bahia Control Planes

Bahia's supported control-plane contract is now sidecar-first and Nostr-native. Agents implementing Nostr events should use `docs/nostr-event-implementation-guide.md` as the Bahia-specific authority for event-kind selection, event shapes, migration boundaries, and Cascadia fleet interoperability.

1. **Nostr relay sidecar** — primary async/realtime plane for browser state, ContextVM intent transport, agent progress, and read models.
2. **ContextVM / native MCP JSON-RPC** — canonical mutation method surface over Nostr kind `25910` and HTTP MCP at `/mcp` / `/api/v1/mcp`.
3. **REST API** — narrowed CRUD/query/log surface protected by direct NIP-98 when auth is enabled; Bearer credentials are not accepted.

Removed legacy surfaces:

- `GET /api/v1/events/stream` dashboard SSE stream
- `POST /api/v1/auth/nostr` NIP-98-to-JWT browser exchange
- `/api/v1/agent/*` custom MCP-inspired HTTP tools

ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) is the canonical client bootstrap. Production clients must not depend on legacy discovery kind `31974`; startup migration may still read legacy discovery artifacts only to produce canonical discovery and relay-set events.

---

## ContextVM and Native MCP Transport

> **Nostr kind**: `25910` ContextVM JSON-RPC messages, usually CEP-4/NIP-59 encrypted with `1059` or `21059`.  
> **HTTP MCP base paths**: `/mcp` and `/api/v1/mcp`

ContextVM clients use JSON-RPC 2.0 over Nostr for mutations; HTTP MCP clients use the same JSON-RPC method model over HTTP. Tool implementations are backed by `internal/mcp/server.go`; long-running tool results include Nostr correlation metadata (`request_event_id`, `method`, `service_id`, `route_id`, `release_id`, `environment_id`, `intent_id`, `run_id`, and canonical observable kinds) so agents can follow async truth on the relay. ContextVM discovery kinds `11316`-`11320` plus NIP-51 relay sets (`30002`) advertise bootstrap metadata for clients before subscribing.

Example:

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc":"2.0",
    "id":2,
    "method":"tools/call",
    "params": {
      "name": "bahia_deploy",
      "arguments": {
        "service_id": "...",
        "environment_id": "...",
        "artifact_id": "..."
      }
    }
  }'
```

### ContextVM mutation methods

Client mutation publication should use ContextVM JSON-RPC methods rather than Bahia legacy request kinds. Method names follow `<domain>/<operation>`:

| Domain | Methods |
|--------|---------|
| `service` | `deploy`, `rollback`, `scale`, `restart`, `stop`, `update`, `delete` |
| `worker` | `cordon`, `uncordon`, `drain`, `undrain`, `maintenance-enter`, `maintenance-exit`, `labels-update` |
| `package` | `publish`, `promote`, `yank`, `deprecate`, `drift-detect` |
| `dns` | `zone-create`, `zone-delete`, `record-set`, `policy-apply`, `drift-remediate` |
| `backup` | `run`, `restore`, `verify`, `retention-enforce`, `repository-probe` |
| `ml` | `model-import`, `recipe-run`, `inference-deploy`, `inference-rollback` |
| `security` | `scan`, `rescan`, `findings-list`, `schedules-list` |
| `sbom` | `generate`, `import` |

Example ContextVM request:

```json
{
  "jsonrpc": "2.0",
  "id": "worker-cordon-01",
  "method": "worker/cordon",
  "params": {
    "worker_pubkey": "<worker-pubkey>",
    "reason": "operator requested",
    "_meta": { "progressToken": "worker-cordon-01" }
  }
}
```

The production cutover is complete: CLI/web/client mutations use the ContextVM method surface, and legacy Bahia request-kind publication is not a production runtime path. Legacy kind constants and fixtures may remain only for startup migration, historical conversion, and tests that prove old events fail closed or migrate to canonical events.

---

## Command receipts and idempotency

Every long-running control-plane write surface returns a `CommandReceipt`-compatible object after the request event has been signed and at least one relay has accepted it. The canonical fields are:

- `request_event_id`: signed Nostr request event id.
- `request_kind`: legacy Nostr request kind when a migration fixture path is still used; ContextVM clients should instead expose `method` and ContextVM `request_event_id`.
- `status_kind` / `result_kind`: legacy progress and terminal event kinds when fixture paths are still used; canonical clients should follow `30900`, `4903`, `30315`, domain NIPs, and correlated ContextVM responses.
- `idempotency_key`: the Nostr `d` tag used to collapse retries of the same logical command.
- `status`: `submitted` when the request was accepted; `error` when only a partial publish succeeded.
- `published_relays`: number of relays that accepted the request.
- `timeout_seconds`: publish-and-wait compatibility timeout; default is 30 seconds unless configured by the caller.
- `error` / `retry_hint`: present for partial failure or relay-unreachable failures.

Publish-and-wait compatibility is not terminal truth. Clients must subscribe with scoped filters using `request_event_id`, `request_kind`, `status_kind`, `result_kind`, requester `p`, and relevant resource tags; EOSE only marks historical catch-up complete. Relay-unreachable failures return immediately with a retry hint because no relay accepted the command. Partial relay failures return a receipt with `status=error` and the accepted relay count so callers can avoid unsafe fallback once any relay has accepted the event.

Command publishers and reactors use idempotency keys as Nostr `d` tags. If a caller does not provide one, publishers generate one; CLI signer-first operator commands derive deterministic keys from the command kind, scoped tags, and payload. The reactor deduplicates both event ids and `(kind, pubkey, d-tag)` command replays before executing business logic; persistence exposes lookups by that coordinate so replay protection survives process restarts when the Nostr event audit repository is configured.

MCP runtime/deployment, LLM, AI/ML, DNS, adoption, and remediation tools return uniform receipt fields. REST long-running Nostr-backed AI/ML routes return `202 Accepted` with `CommandReceipt`; legacy REST registry routes that frontend clients still consume synchronously remain compatibility routes until their clients are migrated.

---

## Nostr Sidecar Topology

Browser and Bahia control-plane traffic should target the relay sidecar first.

- Browser discovery: ContextVM discovery announcements (`11316`-`11320`) plus NIP-51 relay sets (`30002`) advertise `nostr.browser_relays` / `nostr.sidecar_url`. Legacy `31974` discovery is migration inventory only.
- Bahia backend connection: `nostr.sidecar.backend_url` when set, otherwise `nostr.sidecar.public_url`
- Bahia-owned control-plane reactor/projector traffic uses only the sidecar backend URL in sidecar mode.
- Upstream relays: configure `nostr.relays` for public interop/audit traffic. If `nostr.sidecar.mirror_external=true`, Bahia treats the sidecar as the upstream mirror boundary and does not also connect directly to those URLs.
- Encrypted-request relay URLs and Loom relays remain explicitly configured for their own traffic and are not used for Bahia read-model publication.

This avoids duplicate event loops: Bahia publishes canonical observables (`30900` state, `4903` audit, `30315` status, `30316` assistant transcript ciphertext, ContextVM discovery `11316`-`11320`, relay sets `30002`, and app data `30078`) to the sidecar pool only, while optional upstream mirroring is isolated behind the sidecar boundary.

### Relay-purpose boundaries

Relay URLs are physical endpoints; Bahia relay purpose is policy. A deployment may intentionally reuse one relay URL for multiple purposes, but the documentation, config, and tests must preserve the semantic boundary:

| Purpose | Owner | Canonical mechanism | Boundary |
|---|---|---|---|
| Public browser bootstrap/read models | Bahia service | NIP-51 `30002`, `d=bahia-browser-v1` | Public browser bootstrap and read-model relay boundary. |
| ContextVM request/reply | Bahia service | NIP-51 `30002`, `d=bahia-contextvm-v1` | ContextVM mutation traffic; clients prefer it when present and may fall back to browser relays only with degraded metadata. |
| Service publish/backfill | Bahia service | NIP-51 `30002`, `d=bahia-service-v1`; advisory NIP-65 `10002` | Backend/service publication and historical backfill; not automatically exposed to browsers. |
| User/operator preferences | User/operator pubkey | NIP-65 `10002` | General author routing preferences, not Bahia service-strategy authorization. |
| Repository/ngit | Repository maintainer or SoulFactory | NIP-34 `30617` `relays` tags and `30618` state | Repository-specific relay hints, preferred before global Bahia relay policy for repository operations. |
| DM receive routing | Receiving identity | NIP-51 `10050` | Direct-message routing only for explicitly configured DM-enabled Bahia features and identities; not inferred from browser, ContextVM, or service relay sets. |
| FIPS public adverts | FIPS/Bahia operator | Existing FIPS overlay advert contract plus explicit bridge relay config | Public advert exposure; do not infer sensitive endpoint/control relay safety. |
| FIPS/Bahia endpoint/control | Bahia service/operator | ContextVM relay sets or explicit bridge relay config | Sensitive endpoint/control exposure; sharing with public relays is an explicit deployment decision. |
| Relay capability/liveness | Relay or trusted monitor | NIP-11; optional NIP-66 `10166`/`30166` | Advisory ranking/health metadata only; never a trust root. |
| Relay administration | Bahia relay owner/operator | Optional NIP-86 HTTP API with NIP-98 authorization | Relay-owner administration such as allow/ban/kind/metadata controls; not ContextVM app/control-plane mutation transport and not NIP-42 websocket AUTH. |

No new relay-routing kinds are allocated for these purposes. ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) remain canonical; legacy `31974` is historical/migration-only.

---

## Nostr Control Plane

Production runtime subscribes to ContextVM `25910` messages and canonical observable streams, then publishes canonical state, status, audit, discovery, and relay-set events. Legacy Bahia request/status/result/read-model kinds are not production reactor or subscriber inputs outside startup migration and historical conversion. REST is only a narrowed registry/query/compatibility surface.

| Series | Kinds | Production role |
|--------|-------|-----------------|
| ContextVM CRU | `25910` inside `1059`/`21059` where supported | Browser/CLI/agent JSON-RPC mutation methods and correlated responses |
| Canonical state | `30900`, `30078` | Control-plane state projections and NIP-78 app-specific data; SBOM references use `30078` with `schema=bahia.sbom.ref.v1` |
| Canonical audit/status | `4903`, `30315` | Immutable audit facts and NIP-38 operational statuses |
| Assistant transcript | `30316` | Assistant conversation/tool transcript entries encrypted as service-held symmetric-key AEAD envelopes with key-reference/rotation tags |
| ContextVM discovery | `11316`-`11320` | Server, tool, resource, prompt, and template announcements |
| Relay sets and curation sets | `30002`, `30004` | NIP-51 relay topology/bootstrap sets and complete SBOM availability lists |
| Deletions | `5` | NIP-09 delete events where relay-level deletion semantics apply |

Historical Bahia-specific request/status/result/read-model/encrypted ranges (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, `7980`) are migration inventory only. Production clients must not publish or subscribe to those numbers as live runtime contracts; they may appear in startup migration manifests, historical conversion tests, and fail-closed fixtures.

### Startup migration app

Bahia includes a startup migration app in `internal/nostrmigration` so deployed relays and local repositories can be converted to the canonical contract without keeping legacy kind support in core runtime code.

Runtime behavior:

1. Scan the local Nostr event repository for `LegacyKinds()`.
2. Optionally subscribe to configured relays for legacy kinds, bounded by migration backfill settings, and require `EOSE`.
3. Resolve each legacy event with the migration disposition manifest.
4. Skip if the target canonical kind already has an event tagged `migrated-from=<legacy_event_id>`.
5. Build a canonical event tagged with `migration=bahia-nostr-native-v1`, `legacy-kind`, `migrated-from`, `schema`, `domain`, and layer metadata.
6. Sign with the Bahia service private key.
7. Publish to relays and treat accepted `OK` or duplicate `OK` as success.
8. Persist the canonical event locally and emit the migration summary log.

The app is idempotent and can run on every startup. Non-dry-run migration requires a Nostr publisher and service private key. Relay backfill must reach `EOSE`; `CLOSED`, missing `EOSE`, signing failures, or zero accepted publishes are deployment failures to fix before relying on the canonical runtime. They are not a reason to reintroduce legacy reactor/subscriber paths.

### Canonical ContextVM mutation flow

Public, encrypted, DNS, and operator mutations follow the same ContextVM lifecycle invariants:

1. Build a JSON-RPC 2.0 request with a Bahia method such as `service/deploy`, `service/restart`, `worker/cordon`, `package/promote`, `dns/zone-create`, `backup/run`, or `ml/recipe-run`.
2. Publish the request as ContextVM kind `25910`, usually wrapped with CEP-4/NIP-59 random-key gift-wrap (`1059` or `21059`) when encrypted transport is available.
3. Require relay `OK` with `accepted=true` for the signed Nostr event. A JSON-RPC acknowledgment is only command receipt, not proof of long-running completion.
4. Subscribe with scoped filters for the correlated ContextVM response plus canonical observables: `30900` state, `4903` audit, `30315` status, `30316` assistant transcript ciphertext for assistant flows, relevant domain NIPs, NIP-09 `5` deletes where applicable, `30078` app data, `30004` curation sets, and discovery/relay updates (`11316`-`11320`, `30002`).
5. Treat EOSE as historical catch-up only; keep subscriptions open for realtime convergence. Deduplicate by event id and use replaceable semantics for `(kind, pubkey, d-tag)` state events.
6. Handle `CLOSED` and `AUTH` explicitly. Auth-related closures fail distinctly; non-auth closures fail only when all known result/observable relays close before a correlated terminal state/audit/status event.

SBOM methods (`sbom/generate` and `sbom/import`) are explicit asynchronous-ack methods. Their ContextVM JSON-RPC result is an acceptance coordinate, not a run result:

```json
{
  "accepted": true,
  "status": "accepted",
  "run_id": "<idempotencyKey>",
  "status_d_tag": "sbom:run:<sanitized-idempotencyKey>",
  "idempotencyKey": "<idempotencyKey>",
  "observable_kinds": [30315, 4903, 30078, 30004]
}
```

After receiving this acknowledgment, clients subscribe to `30315` with `#d=<status_d_tag>` for progress and to subject-scoped `30078` SBOM reference plus `30004` availability-list events for terminal truth. The service-side SBOM async runner handles orchestration off the request path and still requires relay `OK` acceptance for every published observable; `AUTH`, `CLOSED`, and rejected `OK` outcomes become failed status/error evidence rather than synchronous ContextVM completion payloads.

### DNS/FIPS operator UX

Human browser operators use the DNS dashboard as a Nostr-native console:

- DNS and FIPS mesh state are read from canonical state/app-data observables (`30900` and `30078`) with semantic `domain`, `schema`, `d`, and resource tags such as `dns-endpoint`, `dns-zone`, `dns-policy`, `worker`, and `fips-mesh`. The dashboard bootstraps historical state through EOSE-aware queries and keeps subscriptions open for realtime EVENT updates. REST read catalogs are not the dashboard substrate.
- DNS writes are ContextVM methods (`dns/zone-create`, `dns/policy-apply`, `dns/record-set`, `dns/drift-remediate`) rather than Bahia-specific request kinds. The browser records the ContextVM event id, relay OK accepted/rejected outcomes, and canonical observable updates.
- No REST DNS write endpoints are part of this UX. REST remains a compatibility/query surface for areas that have not moved to Nostr-native flows.

Agent operators use MCP for synchronous discovery and action entry points while following Nostr truth for async state:

- MCP exposes DNS/FIPS discovery via resources/tools backed by DNS/FIPS projection data, including FIPS mesh node/status resources from mesh DNS projection records.
- Long-running MCP actions must return ContextVM/Nostr correlation metadata so agents can subscribe to canonical observables instead of polling REST.

### Canonical observable contracts

| Kind | Contract | Filtering guidance |
|------|----------|--------------------|
| `30900` | Canonical control-plane state projection | Scope by service author, `#d`, `#domain`, `#schema`, resource tags (`#service`, `#environment`, `#artifact`, `#dns`, `#worker`, etc.) |
| `4903` | Canonical audit fact | Scope by service author, requester `#p`, resource tags, and correlation `#e` where present |
| `30315` | NIP-38 operational status | Scope by service author, `#d`, `#domain`, `#status`, resource tags, and correlation `#e`; continuity heartbeat observations use `#domain=continuity`, `schema=bahia.status.continuity-heartbeat.v1`, and heartbeat `d`/`worker` tags rather than a separate `30350` kind |
| `30316` | Assistant transcript | Scope by service author, `#schema=bahia.assistant-transcript.v1`, `#session`, `#turn`, `#role`, and sequence/key tags; content is service-held symmetric-key AEAD ciphertext |
| `11316`-`11320` | ContextVM server/tool/resource/prompt/template discovery | Scope by Bahia service pubkey; use for bootstrap before mutation or state subscriptions |
| `30002` | NIP-51 relay set | Scope by Bahia service pubkey and relay-set `#d` tags |
| `30004` | NIP-51 Curation Set | Scope by Bahia service pubkey, `#d`, `#domain=sbom`, `#schema=bahia.sbom.available-list.v1`, and subject tags |
| `30078` | NIP-78 app data | Scope by Bahia service pubkey, `#d`, `#domain`, and `#schema`; SBOM references use `#domain=sbom` and `#schema=bahia.sbom.ref.v1`; Security findings use `#domain=security` and `#schema=bahia.security.findings.v1` |
| `5` | NIP-09 delete | Scope by service author and `#e`/`#a` references |

DNS state uses the same canonical observable stream rather than a custom Bahia read-model range. Deletions may be NIP-09 kind `5` events or canonical tombstone replacements with `deleted=true` when domain state requires durable tombstones.

### AI/ML and Backup canonical observability

AI/ML and backup operators use ContextVM mutation methods (`ml/model-import`, `ml/recipe-run`, `ml/inference-deploy`, `backup/run`, `backup/restore`, `backup/verify`, and related methods) over kind `25910`, wrapped with `1059`/`21059` when encrypted transport is available. Long-running operations publish canonical observables instead of Bahia-specific command/result/read-model ranges:

- `30900` for desired/observed state projections.
- `4903` for immutable audit facts and provenance breadcrumbs.
- `30315` for operational status and progress; continuity heartbeat observations are NIP-38 statuses with `#domain=continuity` and heartbeat schema/d/worker tags, not a dedicated `30350` kind.
- `30078` for app-specific registries, SBOM references, and operator-visible projection details.
- `30004` for NIP-51 SBOM availability lists when a subject has one or more SBOM references.
- `5` for NIP-09 deletion where relay-level deletion semantics apply.

Historical AI/ML and backup custom ranges (`38390`-`38431`, `31980`-`31999`, `6981`-`6984`) are migration inventory only. They may appear in startup migration manifests, historical conversion reports, and fail-closed fixtures, but production docs must not instruct clients to publish or subscribe to them as live runtime contracts. Artifact signature attestation kind `31200` remains a historical artifact-signature reference and is not a replacement for canonical Bahia audit/status observability.

REST and MCP may initiate compatible AI/ML or backup tooling flows, but they must return ContextVM/Nostr correlation metadata instead of claiming completion for long-running work. A successful synchronous response includes the ContextVM request event id, method, relevant canonical observable kinds, requester pubkey, and scoped tags such as `endpoint`, `environment`, `model_version`, `recipe`, `run`, `backup_repository`, or `restore_id`. Clients subscribe with those tags, wait for EOSE for historical catch-up, process realtime observable events, and never poll REST/MCP for completion.

### Security OSV/SBOM canonical observability

Security OSV/SBOM scanning uses ContextVM mutation/read methods and canonical observables rather than request/status/result kind triplets. The initial Security method catalog is:

| Method | Purpose | Notes |
|--------|---------|-------|
| `security/scan` | Explicit scan request for SBOM reference, package coordinate, PURL, or Git commit target. | Response acknowledges intent only. |
| `security/rescan` | Request a new scan run for a known target or latest SBOM target. | Response acknowledges intent only. |
| `security/findings-list` | Read persisted findings by target, run, policy scope, severity, or OSV id. | Read response does not imply in-flight scan completion. |
| `security/schedules-list` | Read policy-derived scan schedules and freshness state. | Due execution is observable through scan events. |

Durable Security truth is published by the Bahia service key as:

- `30315` with `domain=security`, `schema=bahia.status.security-scan.v1`, `d=security:scan:<run_id>`, `run`, `target_type`, `target_key_hash`, `status`, optional `step`, and `e`/`p` correlation tags.
- `30900` with `schema=bahia.security.scan-summary.v1` and `d=security:scan-summary:<run_id>` for per-run summaries, or `schema=bahia.security.target-summary.v1` and `d=security:target-summary:<target_key_hash>` for latest target state.
- `30078` with `domain=security`, `schema=bahia.security.findings.v1`, and `d=security:findings:<run_id>:<chunk_or_finding_hash>` for normalized public-safe finding details.
- `4903` with `domain=security`, `schema=bahia.audit.security.v1`, and `type=security-scan`, `security-policy-breach`, or `security-publication` for lifecycle, failure, policy-breach, and publication-retry facts.

SBOM-triggered scanning observes existing SBOM `30078` references and `30004` availability lists with `#domain=sbom` and exact schema/subject filters. Security does not mutate canonical SBOM events after scanning; compatibility aggregate updates are tracked as projection work under Beads. Epic 3 does not add REST compatibility endpoints for Security scans or reads; callers use encrypted ContextVM methods and canonical observables.

Clients following a Security request subscribe to `30315`, `30900`, `30078`, and `4903` with `#domain=security`, trusted service author, `#e=<contextvm-request-event-id>` when correlated, and `#target_key_hash` or `#run` when known. They process historical events until `EOSE`, keep subscriptions open for realtime convergence, handle `CLOSED` and `AUTH` explicitly, deduplicate by event id, and apply replaceable semantics for `30900`/`30078`. Every Security publish requires relay `OK accepted=true`; partial or rejected publishes must remain visible as failed or retryable publication state.

The notification bridge uses the internal event type `security.policy_breached` for new or materially changed breach fingerprints. The Nostr evidence for that notification is the corresponding `4903` Security audit fact; `security.policy_breached` is not a new Nostr kind.

### ContextVM Operator Actions

Operator workflows are ContextVM JSON-RPC requests carried as kind `25910`, usually inside CEP-4/NIP-59 gift-wrap (`1059` or `21059`). They are not REST RPC and must be followed as event streams: publish the ContextVM request, subscribe for the correlated ContextVM response and canonical observables, process `30315` statuses and `4903` audit facts as progress/evidence, and treat canonical state convergence (`30900`/domain NIPs) plus explicit JSON-RPC errors as the durable truth. Clients should not poll or use timeout-based completion; use EOSE for historical catch-up and keep subscriptions open for realtime replies.

CLI behavior:

- `bahia adopt scan|import` and `bahia services actions deploy|restart|stop` use ContextVM methods such as `adoption/scan`, `adoption/import`, `service/deploy`, `service/restart`, and `service/stop`.
- Relay resolution is deterministic: repeatable `--relay` flags, then comma-separated `BAHIA_NOSTR_RELAYS`, then ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`).
- Live status chatter is written to stderr only in table mode; JSON/YAML stdout remains reserved for the final ContextVM acknowledgment or canonical result projection selected by the command.
- `--http-fallback` (or `BAHIA_OPERATOR_HTTP_FALLBACK=true`) is explicit compatibility mode and is only safe before any relay accepts a signed ContextVM request, such as signer/relay discovery failure or publish with zero accepted relays.
- `--raw-target` is compatibility-only. ContextVM adoption paths use server-managed endpoint refs; raw Docker transport material is not published as public relay content.

Authorization uses the verified inner ContextVM event pubkey after unwrap:

- `nostr.authorized_pubkeys` is the global fallback for public operator request authorization.
- `adoption.allowed_pubkeys` additionally authorizes adoption ContextVM methods.
- `direct_runtime_actions.allowed_pubkeys` additionally authorizes direct-runtime ContextVM service action methods.
- Subject/email operator allowlists remain HTTP/NIP-98 compatibility settings and are ignored by ContextVM event authorization.

#### Adoption scan/import

Adoption requests use ContextVM methods `adoption/scan` and `adoption/import`. Targets must reference server-managed runtime endpoints; raw Docker transport material is forbidden. Historical `5978`/`5979` events may be consumed only by startup migration/fixtures.

#### Direct-runtime actions

Direct-runtime deploy/restart/stop use ContextVM methods `service/deploy`, `service/restart`, and `service/stop`. Historical `5963` service-action events and `6963`/`7962` status/result events are migration inventory only and are not production runtime subscriptions.

Deploy actions that reach the desired-state runtime path expose additive metadata through the existing ContextVM response and canonical observables. Status progression uses the existing `30315` status channel with `step` values `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`; rollback may add rollback-specific pre-apply steps before the shared desired-state sequence. Terminal ContextVM responses and service/deployment state projections may include `desired_hash`, `renderer`, `target`, runtime target/apply summaries, environment or unit revision metadata, and `observation_id` when available. These fields are optional and backward-compatible; old clients can ignore them, and clients must not require legacy `7961`/`7962` or `31961`/`31967`/`31968` live subscriptions.

Desired-state metadata is sanitized. Public relay content may include hashes, renderer names, stable target keys, IDs, and redacted secret refs, but must not include resolved secret values, generated Compose env-file contents, raw Docker host URLs supplied by callers, Docker TLS material, bearer credentials, or NIP-98 credentials. Runtime endpoint details remain server-managed aliases such as `endpoint_ref`.

### Legacy Encrypted Request/Result Events (5980/7980)

Legacy encrypted request/result events (`5980`/`7980`) are migration artifacts only. Production sensitive mutations use ContextVM JSON-RPC kind `25910`, usually wrapped with CEP-4/NIP-59 random-key gift-wrap (`1059` or `21059`), and correlate responses with `e=<request_event_id>` / `p=<requester_pubkey>` tags on the inner ContextVM response. Legacy events may be read by startup migration or retained in fixtures, but production browser, CLI, MCP, reactor, and subscriber paths must not use them as the live runtime contract.

Discovery/config contract:

- Backend service publish/backfill relay URLs are configured as `nostr.service_relays` with `nostr.relays` retained only as a compatibility service alias.
- Browser-safe relay URLs are configured as `nostr.browser_relays` and exposed through `d=bahia-browser-v1`.
- ContextVM request/reply relay URLs are configured as `nostr.contextvm_relays`, exposed through `d=bahia-contextvm-v1`, and fall back to browser relays only with degraded metadata when the ContextVM set is absent.
- ContextVM discovery `features.encrypted_nostr_requests=true` means the backend has a service key, at least one backend service subscription target, and at least one browser-discoverable relay URL advertised for the operation.
- ContextVM discovery advertises early liveness support with `control_plane.capabilities=[..., "encrypted_controlplane.progress_ack"]` and `control_plane.wire_version="contextvm-jsonrpc-v2"`; clients that see both fields apply a short ack timeout before the longer work timeout.
- Browser clients must keep public `nostr.browser_relays` / `nostr.sidecar_url` separate from `nostr.contextvm_relays`; sensitive payloads must never be inferred safe for public browser relays without encrypted ContextVM capability metadata.

Event contract:

- Production request kind: inner ContextVM `25910`, optionally wrapped as `1059`/`21059`.
- Production clients subscribe for correlated ContextVM responses and canonical observable kinds before publishing when the request event id is known.
- For routed and authorized encrypted ContextVM requests, the backend first returns a JSON-RPC notification with `method="notifications/progress"`, no `id`, and `params.status="processing"`; routing mismatches stay silent and unauthorized requests return the existing terminal error without an ack.
- Request routing tags include `p=<service_pubkey>` and ContextVM method/correlation tags; sensitive payloads stay inside the encrypted wrapper.
- Completion for long-running work is not the JSON-RPC acknowledgment; clients follow canonical observables (`30900`, `4903`, `30315`, domain NIPs) and NIP-09 delete events where applicable.
- Backend handlers validate the inner event signature/sender after unwrap, reject unauthorized requesters, publish JSON-RPC errors for decrypt/validation failures, and deduplicate by event id plus `_meta.progressToken` where supplied.

Browser signer support:

- NIP-07 is supported only when `window.nostr.nip44.encrypt/decrypt` are available.
- NIP-46 can participate only if the provider explicitly exposes `provider.nip44.encrypt/decrypt`; NIP-46's internal encrypted RPC channel does not by itself give the web app NIP-44 conversation-key operations. If absent, encrypted request/result route migration is blocked for that signer mode and the UI/tests should surface that exact blocker.

Encrypted operation catalog:

The following legacy operation names are retained only to document startup migration inputs and historical fixtures. New encrypted browser-facing operations must use ContextVM method names instead of extending `5980`/`7980`.

Notification encrypted operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `notifications.channels.list` | `{}` | `{channels}` | Channel configs are encrypted in transit; webhook `config.secret` is omitted from results. |
| `notifications.channels.get` | `{id}` | `{channel}` | Returns one sanitized channel or an encrypted terminal error. |
| `notifications.channels.create` | channel fields | `{channel}` | Webhook secrets are accepted only as encrypted write payloads. |
| `notifications.channels.update` | `{id, ...fields}` | `{channel}` | Omitted webhook secrets preserve the stored secret; returned channel is sanitized. |
| `notifications.channels.delete` | `{id}` | `{status,id}` | Deletes the channel over encrypted request/result events. |
| `notifications.channels.test` | `{id}` | `{status,id}` | Dispatches directly to the selected channel and returns terminal success/error. |
| `notifications.logs.list` | `{limit?,channel_id?}` | `{logs}` | Delivery logs and payloads are returned only in encrypted result content. |

Encrypted domain operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `payments.history` | `{worker,limit?}` | `PaymentRecord[]` | `worker` is required; `limit` defaults to 50 and is capped at 250. |
| `orgs.list` | `{}` | `({org fields..., role})[]` | Returns orgs visible to the requester pubkey with the caller's role attached to each row. |
| `orgs.detail` | `{id}` | `{org,members,invites,my_role}` | `id` may be an org UUID or org name; `invites` is populated only when the requester has admin access. |
| `orgs.create` | `{name,display_name?}` | `Organization` | Creates an organization for an authorized requester and returns the created org object directly. |
| `orgs.delete` | `{id}` | `{message}` | Deletes the organization when the requester is authorized. |
| `orgs.my_invites` | `{}` | `InviteWithOrg[]` | Returns invites for the requester pubkey enriched with org name/display name. |
| `orgs.accept_invite` | `{invite_id}` | `OrgMember` | Accepts an org invite for the requester pubkey and returns the created membership directly. |
| `orgs.create_invite` | `{org_id,pubkey,role?,expires_in?}` | `OrgInvite` | `role` defaults to `viewer`; `expires_in` is in hours and defaults to 72. |
| `orgs.revoke_invite` | `{org_id,invite_id}` | `{message}` | Revokes an existing invite. |
| `orgs.update_member_role` | `{org_id,pubkey,role}` | `{message}` | Updates member role state through encrypted transport. |
| `orgs.remove_member` | `{org_id,pubkey}` | `{message}` | Removes a member from the org. |

Encrypted route operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `services.secrets.list` | `{service_id}` | `{secrets,total}` | Returns secret refs only; plaintext/encrypted values are omitted. |
| `services.secrets.create` | `{service_id,name,value,environment_id?,encryption_method?}` | `{secret,status}` | Secret value is encrypted in the request and at rest; result contains metadata only. |
| `services.secrets.update` | `{service_id,secret_id,value,encryption_method?}` | `{secret,status}` | Re-encrypts the new value; result contains metadata only. |
| `services.secrets.delete` | `{service_id,secret_id}` | `{status,secret_id}` | Validates the secret belongs to the service before deletion. |
| `services.secrets.reveal` | `{service_id,secret_id}` | `{secret,value}` | Plaintext is returned only in the encrypted result for explicit reveal actions. |
| `deployments.run_logs.get` | `{run_id,tail?,stream?}` | `{logs,stream}` | Stored stdout/stderr snapshots are encrypted result content; public run projections carry metadata only. |
| `artifacts.signatures.verify` | `{artifact_id}` | `{found,stored,verified,discovered,rejected,errors,signatures}` | Verification is triggered by encrypted signed requests and stores discovered signature records. |

### Correlation Tags

Use tags for relay-side filtering and MCP follow-up subscriptions. Service flows use `service`, `environment`, `artifact`, `intent`, and `run`. LLM flows use `route`, `release`, `environment`, `intent`, and `run`. Status/result replies also include `e` with marker `reply`, `p` for the requester pubkey, plus `status` and `step` where applicable. Encrypted result replies use the same `e`/`p` pattern but keep payloads encrypted. MCP async tools return the ContextVM request event id, method, correlation tags, and relevant canonical observable kinds so clients can subscribe directly rather than polling.

Clients should wait for EOSE on bootstrap queries, then keep subscriptions open for live updates. Deduplicate by event id; for replaceable events, latest `created_at` wins for `(kind, pubkey, d-tag)`. Use NIP-09 kind `5` deletions where relay-level Nostr deletion semantics apply; domain projections that require durable tombstone state may also publish canonical tombstone replacements with `deleted=true`.

---

## REST API: Read-Only Surface

The REST API (`/api/v1/*`) is strictly a **read-only** query and compatibility surface. It serves GET endpoints for registry listing, service/environment/deployment read models, health checks, and log retrieval. It does **not** expose mutation endpoints.

### Prohibition

REST write endpoints (`POST`, `PUT`, `DELETE` for creating, updating, or deleting services, policies, deployments, LLM routes, or any domain entity) are architecturally prohibited. This prohibition exists because:

1. **Nostr is the source of truth.** Every mutation must be a signed Nostr event published to relays, giving relay-side indexing, signature verification, replay protection, and audit lineage. REST writes bypass all of this.
2. **Command receipts are relay-acknowledged.** The `CommandReceipt` contract requires a signed event ID and relay `OK` acceptance. REST-originated writes cannot produce authentic receipts because no Nostr event was published.
3. **ContextVM is the canonical mutation transport.** AGENTS.md mandates that all mutations flow through ContextVM kind `25910` JSON-RPC intents. Wrapping Nostr publish calls behind REST handlers creates "fake request/response wrappers over relays" — an explicitly prohibited pattern.

### What to use instead

| Surface | Mutation entry point | Implementation |
|---------|---------------------|----------------|
| **MCP tools** | `POST /mcp` with `tools/call` JSON-RPC | `internal/mcp/server.go` → controlplane publishers |
| **CLI** | `bahia services actions deploy\|restart\|stop`, `bahia adopt scan\|import` | `cmd/cli/operator_nostr.go` → ContextVM `25910` |
| **Browser** | ContextVM `25910` via NIP-07/NIP-46 signer | Direct Nostr event publication to relay |
| **REST** | Read-only `GET` endpoints only | `internal/api/handlers/*.go` (Get/List methods) |

### Correct mutation flow

```
┌──────────────┐    ┌──────────────────┐    ┌─────────────┐    ┌────────────┐
│ MCP / CLI /  │───▸│ Controlplane     │───▸│ Nostr Relay │───▸│ Reactor /  │
│ Browser      │    │ CommandPublisher  │    │ (relay OK)  │    │ Projector  │
└──────────────┘    └──────────────────┘    └─────────────┘    └────────────┘
                           │                                          │
                    Signs event with              Subscribes to canonical
                    service/operator key          observables (30900, 4903,
                    as kind 25910                 30315) for durable truth
                           │                                          │
                    Returns CommandReceipt          Updates read models
                    (event_id, relay count,         served by REST GET
                     idempotency key)               endpoints
```

REST GET endpoints serve the **read models** that reactors and projectors maintain after processing Nostr events. The mutation path never touches REST.

---

## Authorization

- **Signer-first browser identity**: web sessions are signer-first (NIP-07 or NIP-46), with signer pubkey as the primary user identity.
- **Nostr requests**: event signatures are verified and request kinds require authorized operator pubkeys.
- **Control-plane operator allowlist**: `nostr.authorized_pubkeys` is for control-plane/operator request authorization only.
- **Tenant bootstrap owner allowlist**: `auth.bootstrap_owner_pubkeys` governs who may create organizations over REST compatibility endpoints when configured.
- **REST and MCP HTTP**: use direct NIP-98 (`Authorization: Nostr <base64event>`) when auth is enabled. `Authorization: Bearer ...` is rejected with `401` rather than treated as a fallback.
- **REST role in architecture**: REST is a compatibility transport for narrowed CRUD/query surfaces that have not yet moved to Nostr-native flows.

---

## Deprecated / Quarantined Event Kinds

The legacy 311xx command bridge and Bahia-specific request/status/result/read-model ranges are deprecated and may appear only as migration inventory or fail-closed fixtures. New integrations must use ContextVM kind `25910` methods and canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, and NIP-09 `5`).

| Deprecated historical input | Production replacement |
|-----------------------------|------------------------|
| 31102 intent create | ContextVM `service/deploy` or `service/update` method |
| 31103/31104 approval/rejection | ContextVM approval method for the relevant domain |
| 31105 rollback | ContextVM `service/rollback` method |
