# Bahia HTTP Reference

The daemon listens on `server.host:server.port` (default `127.0.0.1:8080`).
HTTP is one narrow surface of Bahia: health and readiness probes, Prometheus
metrics, the OCI distribution API, MCP over HTTP, and a small set of
`/api/v1` routes for things that are HTTP-native (streamed logs, blob
downloads and an SBOM upload), operator maintenance, payment reads and
virtualization projections. Control-plane entity mutations and the primary
state-read path use Nostr ([control planes](control-planes.md),
[event specification](event-spec.md)).

The routing table is `internal/api/router/router.go`; any route not listed
here, including every other method on a listed path, returns `404`.

## Authentication and limits

- With `auth.enabled: true` (default `false`), `/metrics`, `/mcp` and every
  `/api/v1` route require `Authorization: Nostr <base64 NIP-98 event>`.
  `Bearer` tokens are rejected with `401`. Every `/api/v1` caller must be a
  bootstrap owner (`auth.bootstrap_owner_pubkeys`) or a member of at least
  one organization. Virtualization handlers always require an authenticated
  principal and tenant authorization, even when global HTTP auth is disabled. Routes marked *admin* additionally require the `admin`
  platform role in an organization; routes marked *org* require membership
  in the resource's organization (and the listed permission).
- Per-IP rate limits: 100 requests/minute on read routes, 30/minute on write
  routes.
- CORS origins come from `cors.allowed_origins` (default none).
- Routes marked *db* answer `503` when PostgreSQL is not available.
- Every `/api/v1` response is JSON; successful bodies are `{"data": …}`,
  errors `{"error": "…"}`.

## Probes and metrics

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/health` | none | Liveness. Always `200` with `{"status","version","ready","checks","runners"}` |
| GET | `/ready` | none | Readiness. `200` when every required check passes, `503` otherwise, same body |
| GET | `/metrics` | NIP-98 when auth is enabled | Prometheus exposition (`telemetry.enabled`) |

Readiness checks reported in `checks[]` (by `name`): `intent_readiness`,
`relay_quorum`, `bootstrap_ready`, `background_runners`, `ock_rotation`,
`canonical_delivery`, `adoption`, `backup_scheduler`, `edge_routing`,
`hiveci`, `intent_authors_sync`, `internal_routing`, `payments`,
`relay_policy_projection`, `security_scanner`, `supervision_apply_lock`.
Each has `status` (`pass`, `warn`, `fail`), `message` and optional
`details`. `runners[]` lists background runners with `running`. Container
health should probe `/health`; traffic gates should probe `/ready`.

## MCP

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/mcp` | NIP-98 when enabled, *admin*, *db* | MCP JSON-RPC (`tools/list`, `tools/call`, `resources/*`, `prompts/*`). Intent tools sign a `30900` intent with the caller's identity and return `{"status","intent_id","event_id","status_kind","status_coordinate",…}`; store-read tools answer from the local event store |

Tool names and arguments: [MCP tools reference](user-guide/mcp-tools.md).

## OCI distribution (`oci.enabled`)

`/v2/*` is mounted when the registry is enabled (*db*). It implements the
distribution API for `/v2/`, `/v2/<repo>/manifests/<ref>`,
`/v2/<repo>/blobs/<digest>`, `/v2/<repo>/blobs/uploads/[<id>]`,
`/v2/<repo>/tags/list` and `/v2/<repo>/referrers/<digest>`. Manifests and
tags are indexed in PostgreSQL; blobs are stored in Blossom. Principals are
resolved from NIP-98 (`Authorization: Nostr …`), HTTP Basic for
`oci.service_accounts`, or anonymous pull from `oci.allow_anonymous_pull_cidrs`.
`oci.public_host` is the hostname advertised in discovery.

## `/api/v1` routes

### Reads

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/virtualization-hosts`, `/vm-images`, `/persistent-vms`, `/execution-planes`, `/vm-checkpoints`, `/vm-exports` | NIP-98, *db* | List a virtualization resource kind. Query `org_id` (required), `limit` (default 50), `offset`. Returns the public DTOs only |
| GET | `/api/v1/<kind>/{id}` for the kinds above and `/api/v1/vm-operations/{id}` | NIP-98, *db* | Get one resource (`org_id` required) |
| GET | `/api/v1/deployments/runs/{id}/logs` | NIP-98, *org*, *db* | Stored stdout/stderr of a deployment run from Blossom. Query `tail`, `stream=stdout|stderr|merged`. Mounted when `blossom.enabled` |
| GET | `/api/v1/services/{id}/environments/{envId}/logs` | NIP-98, *org*, *db* | Live container logs as `text/event-stream`. Query `tail` (default 100), `follow=true`. Mounted when a runtime resolver is configured |
| GET | `/api/v1/deployments/runs/{id}/cost` | NIP-98 | Cost of a run from the daemon's canonical payment records (no PostgreSQL needed) |
| GET | `/api/v1/payments/history` | NIP-98 | Payment records. Query `worker` (required), `limit` (default 50, max 250) |
| GET | `/api/v1/config-fabric/drift` | NIP-98, *admin*, *db* | Desired versus applied Config Fabric status per target |
| GET | `/api/v1/blossom/blob/{hash}` | NIP-98 when enabled | Proxy download of a content-addressed Blossom blob (lets an HTTPS page fetch from an HTTP Blossom server). Mounted when `blossom.enabled` |

### Writes

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance` | NIP-98, *org*, *db* | Open a maintenance window on a supervised instance. Body `{"reason": "…", "expires_at": "<RFC 3339>"}`; `reason` is required. `503` when supervision is off |
| DELETE | same path | NIP-98, *org*, *db* | Close the maintenance window |
| POST | `/api/v1/artifacts/{id}/sbom` | NIP-98, *org* (`services:write`), *db* | Ingest an SBOM document for an artifact (raw body, at most 10 MiB). Bahia stores the payload, publishes the `30078` reference and `30004` availability list |
| POST | `/api/v1/soulfactory/legacy-reconciliation/preview` | NIP-98, *admin*, *db* | Dry-run classification of pre-existing SoulFactory agents against souls on the relay |
| POST | `/api/v1/soulfactory/legacy-reconciliation/apply` | NIP-98, *admin*, *db* | Apply one approved link. Body is the preview's classification plus `approval_ref` (required); `409` on a conflicting state |

Maintenance windows are also available through the web Fleet Health page
and the managed-instance records (`runtime-instance-health`); the SBOM
route exists for CI uploads that already have the document in hand — the
same result is reachable with the `sbom/import` intent and a Blossom
`location`.
