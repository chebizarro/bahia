# Troubleshooting

Symptoms, likely causes, and the checks that resolve them.

## Daemon

### `/health` does not answer

```bash
docker compose ps
docker compose logs bahia
curl http://localhost:8080/health
```

The daemon listens on `server.host:server.port` (default `127.0.0.1:8080`). Inside Compose the host is `0.0.0.0`; outside it, set `BAHIA_SERVER_HOST`.

### `/ready` returns 503

`/ready` lists every readiness check with its status:

- `relay_quorum` — fewer healthy relays than `nostr.relay_quorum.*_min_healthy` for the current mode. Check relay URLs, TLS, and NIP-42 credentials.
- `bootstrap_ready` / `intent_readiness` — the daemon is still replaying its relay filters after start; wait for `all filters synced`.
- `background_runners` — a required runner stopped; the message names it, the log has the cause.
- `canonical_delivery` (`warn`) — the publish outbox abandoned records after exhausting retries. The message names the coordinates; run `bahia outbox --daemon list --state failed` and `bahia outbox retry --all`, or call the `bahia_outbox_retry` MCP tool. Fix the relay before retrying or the entries fail again.
- `intent_authors_sync` — the daemon could not push its trust set to the relay sidecar over NIP-86; operators and members will be refused as writers until it succeeds.
- Feature checks (`adoption`, `hiveci`, `edge_routing`, `internal_routing`, `security_scanner`, `backup_scheduler`, `payments`, `ock_rotation`, `relay_policy_projection`, `supervision_apply_lock`) report the state of their subsystem and name the configuration they need.

PostgreSQL is not a readiness gate: the daemon keeps serving relay-backed reads without it, and the HTTP routes that need the index answer `503`.

### Log level

```yaml
log:
  level: debug      # BAHIA_LOG_LEVEL=debug
  format: json
```

## Relays

### Clients cannot connect or receive nothing

1. Confirm the relay accepts WebSocket connections: `websocat wss://relay.example.com`.
2. Confirm the client trusts the right service pubkey: canonical records are filtered by `authors`, so a wrong pubkey yields an empty store, not an error. The web app's seed is `PUBLIC_BAHIA_SERVICE_PUBKEYS`; the CLI's is `--service-pubkey`.
3. Confirm you are subscribing by `#t` topic (`service-state`, `deployment-intent`, …); `domain` and `schema` are not relay-indexed.
4. If the subscription is CLOSED with `auth-required:`, the topic is protected: authenticate with a pubkey the sidecar admits (fleet operator, organization member, or `read_auth_allowed_pubkeys`). See [relay sidecar](nostr-integration.md#relay-sidecar).
5. If EOSE never arrives on one relay, the CLI prints a stale-data warning and serves its local store; the web app shows the relay as catching up or degraded. Either is a relay problem, not a data problem.

### Publish is refused

The relay's `OK` message carries the reason:

- `blocked: pubkey is not admitted by the persisted relay policy` — the signer is not a fleet operator, organization member, or relay administrator. Membership propagates to the sidecar through the daemon's intent-author sync; a member added seconds ago may need the next sync.
- `invalid: created_at too far in the future` / `… in the past` — fix the client clock.
- `invalid: event has expired` — an `expiration` tag in the past.
- `auth-required:` — the relay wants NIP-42 and the signer cannot authenticate.

### Intent accepted by the relay but nothing happens

Relay `OK` is delivery, not processing. Wait for the `30315` status addressed to your pubkey (`bahia …` exits `2` and prints the intent and event IDs when it times out). If no status ever arrives:

- the domain is listed in `nostr.intent_domains_disabled`;
- the daemon is not subscribed to the relay you published to (`nostr.relays` / `contextvm_relays`);
- the intent is missing a required tag (`d`, `domain`, `intent_id`, `org` for organization-scoped domains) and was dropped at parse time — check the daemon log.

A `rejected` or `conflict` status carries a `reason`; a `conflict` on an update means your `expected_updated_at` is stale — re-read and resubmit.

## Signers

### NIP-07 extension not detected

The web app needs `window.nostr` with `signEvent`, `getPublicKey`, and `nip44` for confidential intents. Install or unlock the extension and reload; some extensions require the site to be allowed first.

### NIP-46 bunker will not connect

- The bunker URI must include at least one relay, or pass it with `--nostr-bunker-relay` (CLI) / the relay field (web).
- The bunker must approve the client key. The CLI uses a persistent client key from `--nostr-client-key-file`; it never generates a throwaway identity.
- Confidential domains (`org`, `secret`, `notification`, `relay`) require a NIP-44-capable signer; the control stays disabled and names the missing capability otherwise.

### `access denied` from MCP or `not readable with this key`

MCP over HTTP requires the caller's pubkey in `nostr.authorized_pubkeys` and the platform admin role. A confidential read answers `not readable with this key` when the signer is not a member of the organization (or, for fleet-scope records, not a fleet operator), so no key envelope was wrapped to it.

## Deployments

### Intent stays `pending`

The environment is `protected`, its reconcile mode is `approval_required`, or a `block` policy failed. Approve or reject it under **Deployments → Pending Approvals** or with `bahia deployments approve --intent <id> --expected-updated-at <rfc3339>` (role `deployer` or above).

### Run `failed` or `timeout`

Read the run logs (**Deployments → run**, `bahia logs run <run-id>`, or `bahia_get_run_logs`). Then check that the artifact digest is pullable from the runtime endpoint, that the deployment unit's `endpoint_ref` resolves, and — for worker-executed runs — that an eligible worker is online (`bahia workers list`).

### `drifted`

`bahia state drifted` lists affected services. The observed artifact differs from the desired one: a manual restart with another image, an out-of-band deployment, or a crash loop. Redeploy the desired artifact, or set the environment's reconcile mode to `auto_apply` to let the daemon converge.

### Managed route unhealthy while the service is healthy

Route canaries report `service_healthy_route_broken=true` when the container is fine but the public route fails. Check DNS projection, TLS, and the proxy; see [Route Canaries](features/route-canaries.md).

## Builds and artifacts

- `Gitea mirror and HiveCI build initiation are not configured` — set `hiveci.initiator.enabled` and its mirror settings.
- `builds request` times out on first use — mirroring a repository can take longer than 30 s; retry with the same `--idempotency-key` and `--result-timeout 120s`.
- A build succeeded but no artifact appears — the daemon registers artifacts only from signed `5402` results whose publisher is in `hiveci.trusted_loom_worker_pubkeys` / `hiveci.trusted_ci_pubkeys`.
- `artifacts import-observed` refuses — `hiveci.allow_live_artifact_import` is `false`, or the digest and `bahia.*` labels do not match what the daemon observes.

## Security scans

- A scan did not start after an SBOM import — the scanner watches the SBOM reference and availability events; confirm both were accepted by the relay, then trigger one explicitly from **Security** (a `security` `scan-run` intent).
- `security.policy_breached` notifications are sent only when a breach fingerprint is new or changed, through a channel subscribed to that event type.
- A policy blocks for a missing scan — run the scan, wait for the `security-finding` records, then re-evaluate.

## Web app

- **Blank page** — the container did not receive `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` / `PUBLIC_BAHIA_SERVICE_PUBKEYS`; check the browser console for `Documentation publisher not configured` or a bootstrap error.
- **Stale data** — the page renders the local event store first; the relay indicator shows **Syncing with relays…** until every relay reaches EOSE. Use **Settings → Relays** to reconnect the browser session or add a local override.
- **Pending badge never clears** — see *Intent accepted by the relay but nothing happens* above.

## Collecting diagnostics

```bash
docker compose logs bahia > bahia.log
curl -s http://localhost:8080/health
curl -s http://localhost:8080/ready
bahia outbox --daemon counts
```

Configuration printed in logs or exported as JSON/YAML is masked (keys, credentials, bunker URIs) and safe to share.

## Related

- [Getting Started](getting-started.md)
- [Nostr Integration](nostr-integration.md)
- [CLI Reference](cli-reference.md)
