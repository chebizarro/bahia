# Nostr Integration

Nostr is Bahia's control plane and source of durable product state. Clients subscribe to signed records, publish signed requests, wait for relay protocol outcomes, and follow domain status and canonical observables.

## Trust boundary

- The daemon signs canonical state, status, audit, discovery, and documentation with `nostr.private_key`.
- Fleet operators are the pubkeys configured in `nostr.authorized_pubkeys`.
- Organization membership and roles authorize tenant-scoped work.
- Confidential records use an organization or fleet content key (OCK).
- Every inbound event is checked for ID, Schnorr signature, author, kind, required tags, timestamp, and content shape before use.

## Current event model

| Role | Kind | Author | Addressing |
|---|---|---|---|
| Signed intent | `30900` with `t=bahia-intent` | operator | `d=<intent-id>`, `domain`, `intent_id`, optional `org` |
| Intent status | `30315` | service | requester `p`, intent correlation and bounded status |
| Canonical state | `30900` with `schema=bahia.cp-state.v1` | service | `t=<topic>` and stable `d` coordinate |
| Audit fact | `4903` | service | entity and correlation tags |
| Interactive request | `25910`, optionally wrapped by `1059` or `21059` | requester | registered ContextVM JSON-RPC method |
| Documentation | `30023` with `t=bahia-docs` | service | `d=<topic>` |
| Worker advertisement | `10100` | worker | worker pubkey |
| Discovery | `11316`–`11320`, `30002`, `10002` | service | endpoint and relay-set contracts |

Fleet integrations keep their protocol kinds, including Hive-CI workflow evidence and Soul Factory requests/results. Use the feature guide for each interop contract.

## Intents

A kind-`30900` intent is a parameterized replaceable request document:

```json
{
  "kind": 30900,
  "tags": [
    ["d", "0195f2c8-7c31-7c6d-8d18-29bb1d45ab4e"],
    ["t", "bahia-intent"],
    ["domain", "service"],
    ["op", "create"],
    ["intent_id", "0195f2c8-7c31-7c6d-8d18-29bb1d45ab4e"],
    ["org", "11111111-1111-1111-1111-111111111111"],
    ["p", "<service-pubkey>"]
  ],
  "content": "{\"name\":\"api\",\"artifact_repo\":\"ghcr.io/acme/api\"}"
}
```

The intent ID is the idempotency key. An exact retry replays the result; different content under the same ID is rejected. Updates include the canonical `updated_at` revision.

A relay `OK` proves that a relay accepted the event. The service's `30315` status reports admission, rejection, or conflict. Durable completion comes from canonical state, audit, and domain outcome records.

Virtualization `request` intents use `t=virtualization` and
`d=vm-operation:<operation_id>`. They receive a rejected status while the
canonical operation executor is unavailable; a relay `OK` does not start a VM.

Organization, secret, and notification intents are gift-wrapped so relays do not see confidential content.

## Canonical state

A canonical record is the newest valid event for one service-author and `d` coordinate. The `t` tag selects a topic such as `service-registry`, `environment-registry`, `deployment-intent`, `deployment-run`, `service-state`, `org-registry`, or a feature-specific family.

Readers must:

1. subscribe with the service author, kind, and indexed topic/coordinate tags;
2. process stored events through EOSE;
3. keep the subscription open for live events;
4. deduplicate by event ID;
5. choose the replaceable winner by Nostr ordering;
6. apply deletion/tombstone semantics;
7. reconnect and resubscribe with an overlap.

The web app and CLI keep local event stores and can render cached state before EOSE. They expose catch-up state so cached results are not mistaken for fresh results.

## Confidential state and OCK

Bahia uses the NIP-CAS-0011 content envelope for confidential canonical records. The event keeps public routing fields such as topic, coordinate, organization scope, and key version while encrypting the payload. Key envelopes wrap the current OCK to authorized members.

Organization key rotation excludes removed members. Refounding republishes current records under the current epoch. `strict_revocation` makes removal or downgrade rotate and refound before the membership change commits.

Fleet-scoped confidential records use the fleet OCK. Operator allowlists are addressed as `operators:<scope>`; the pubkeys exist only inside encrypted content.

## ContextVM

ContextVM JSON-RPC is used for registered interactive operations such as assistant turns, secret reveal, and run-log retrieval. Virtualization ContextVM methods are not registered while the feature is suspended. Use JSON-RPC `params` and the method's schema. A response is an acknowledgement or bounded result; canonical records remain durable truth.

Do not model ordinary state reads as ContextVM calls. Subscribe to canonical state or use the store-backed CLI/MCP read.

## Relays and discovery

The service publishes NIP-51 kind-`30002` relay sets named `bahia-browser-v1`, `bahia-contextvm-v1`, and `bahia-service-v1`, plus an advisory NIP-65 kind-`10002` list. Clients start from configured bootstrap relays and trusted service pubkeys, then follow the signed relay sets.

The web container receives its runtime seed through `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS`. The CLI uses `--bootstrap-relay` and `--trusted-service-pubkey` or explicit `--relay` and `--service-pubkey`.

### Relay sidecar

The sidecar stores and serves the fleet's events and enforces write policy. `nostr.sidecar.read_auth_mode` defaults to `enforce`; unset or unknown values also resolve to `enforce`. Protected REQ and COUNT filters require NIP-42 AUTH by an admitted fleet operator, organization member, or configured allowed reader. `warn` logs the decision without denying; `off` disables this read gate.

Clients handle `AUTH`, `OK`, `EOSE`, and `CLOSED`. A connection alone is not evidence that a filter caught up or a publish succeeded.

## Publish outbox

The daemon stores publish work until the configured relay quorum accepts it. Exhausted entries move to failed state and mark their canonical coordinate undelivered. Readiness reports this as `canonical_delivery` with warning details.

Every outbound relay frame — publications inline and redelivered, NIP-42 AUTH frames, NIP-77 reconcile uploads, NIP-46 signer requests, from the daemon and standalone agents alike — crosses one process-wide admission controller before relay I/O: per-lane and aggregate event budgets, a per-relay wire budget, duplicate suppression from a bounded receipt cache, a shared circuit breaker that any `rate-limited:` feedback opens, and a file-backed emergency kill switch. Admission refusals are back-pressure: the outbox entry stays pending and is retried; they never count against the attempt budget and never abandon an entry. Configuration lives under `nostr.outbound` (`BAHIA_NOSTR_OUTBOUND_*`); the `nostr_outbound_admission` health check reports the gate status with content-free counters. See the [outbound admission runbook](../runbooks/nostr-outbound-admission.md).

Inspect and retry through:

```bash
bahia outbox --daemon counts
bahia outbox --daemon list --state failed
bahia outbox retry <event-id>
```

MCP provides `bahia_outbox_status` and `bahia_outbox_retry`. Retry requeues work; it does not claim delivery. A quorum-accepted publish clears the undelivered marker.

## HTTP surface

The daemon mounts HTTP only where HTTP semantics are part of the current product:

| Route | Purpose |
|---|---|
| `GET /health`, `GET /ready` | Liveness and readiness |
| `GET /metrics` | Metrics when configured |
| `POST /mcp` | Authenticated platform-admin MCP |
| `/v2` | OCI registry when configured |
| `GET /api/v1/deployments/runs/{id}/logs` | Stored run logs |
| `GET /api/v1/services/{id}/environments/{envId}/logs` | Live log stream |
| `GET /api/v1/deployments/runs/{id}/cost`, `/payments/history` | Canonical payment reads |
| `GET /api/v1/config-fabric/drift` | Administrative drift read |
| `GET /api/v1/blossom/blob/{hash}` | Content-addressed blob proxy |
| `/api/v1/virtualization-hosts`, `/vm-images`, `/persistent-vms`, `/execution-planes`, `/vm-checkpoints`, `/vm-exports`, `/vm-operations` | Authorized virtualization reads |
| `POST /api/v1/artifacts/{id}/sbom` | Authenticated SBOM import |
| `POST`/`DELETE .../managed-instances/.../maintenance` | Maintenance override |
| `POST /api/v1/soulfactory/legacy-reconciliation/{preview,apply}` | Dry-run-first agent reconciliation |

Routes whose backing repository is unavailable return `503`. Payment reads and relay-backed product state do not require PostgreSQL. PostgreSQL is an optional derived index.

## Documentation topics

`internal/docs` publishes every `docs/user-guide/**/*.md` file as kind `30023`. A path such as `features/services.md` becomes topic `features-services` and page `/docs/features-services`. Relative Markdown links resolve only to other files inside the user-guide tree.

## Related

- [Core Concepts](core-concepts.md)
- [CLI Reference](cli-reference.md)
- [MCP Tools](mcp-tools.md)
- [Settings](features/settings.md)
