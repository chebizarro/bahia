# CLI Reference

The `bahia` CLI reads canonical state from relays and publishes signed intents. It never talks to the daemon over HTTP.

## Installation

```bash
go install github.com/openagentsinc/bahia/cmd/cli@latest
# or
make build && ./bin/bahia --help
```

## Signer and relays

Every command needs a relay and, for writes and protected reads, a signer.

| Flag | Environment | Purpose |
|------|-------------|---------|
| `--nostr-key-file` | `BAHIA_NOSTR_KEY_FILE`, `BAHIA_NOSTR_NSEC`, `BAHIA_NOSTR_PRIVATE_KEY` | Local private key (`-` reads stdin) |
| `--nostr-bunker-file` | `BAHIA_NOSTR_BUNKER_FILE`, `BAHIA_NOSTR_BUNKER_URI` | NIP-46 bunker URI |
| `--nostr-bunker-relay` | `BAHIA_NOSTR_BUNKER_RELAYS` | Signer relay when not in the bunker URI (repeatable) |
| `--nostr-client-key-file` | `BAHIA_NOSTR_CLIENT_KEY_FILE`, `BAHIA_NOSTR_CLIENT_PRIVATE_KEY` | Persistent NIP-46 client key |
| `--relay` | `BAHIA_NOSTR_RELAYS` | Relay URL (repeatable; highest priority) |
| `--bootstrap-relay` | `BAHIA_NOSTR_BOOTSTRAP_RELAYS` | Bootstrap relay for discovery when no `--relay` is given |
| `--service-pubkey` | `BAHIA_NOSTR_SERVICE_PUBKEY` | Bahia service pubkey (status subscription, run-log fetch, single-service discovery trust) |
| `--trusted-service-pubkey` | `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS` | Trusted service pubkeys for bootstrap discovery (repeatable) |
| `--org` | `BAHIA_ORG_ID` | Organization UUID for organization-scoped intents |
| `--eose-timeout` | `BAHIA_EOSE_TIMEOUT` | Wait for relay EOSE on reads (default `5s`) |
| `--result-timeout` | `BAHIA_RESULT_TIMEOUT` | Wait for an intent status or ContextVM result (default `30s`) |
| `--result-retries` | — | `logs run` republish attempts after a timeout (default `2`) |
| `--encrypted` | — | Gift-wrap the `logs run` request (requires `--service-pubkey`) |
| `-o, --output` | — | `table` (default), `json`, `yaml` |

The CLI refuses a configuration that sets both a local key and a bunker, and never generates a throwaway NIP-46 identity. `bahia auth inspect` prints the configured signer's pubkey and npub.

Relay resolution is ordered: `--relay`, then `BAHIA_NOSTR_RELAYS`, then trusted bootstrap discovery, which needs at least one bootstrap relay and one trusted service pubkey (`--trusted-service-pubkey` or `--service-pubkey`).

## How reads work

Reads subscribe to the service's canonical `30900` records, keep a local event store under `$BAHIA_DATA_DIR/store/<service-pubkey>/` (or `$XDG_DATA_HOME/bahia/store/<service-pubkey>/`), and wait up to `--eose-timeout` for every relay to reach EOSE. When no relay reaches EOSE the command prints a stale-data warning on stderr and still exits 0 with the local result.

Confidential families (organizations, members, secrets metadata, notification channels) are OCK-encrypted on the relay; the CLI unwraps the key envelope with the signer. A non-member sees `not readable with this key` with exit code 0.

## How writes work

Mutations are signed kind `30900` intents (see [Nostr Integration](nostr-integration.md#intents)). The CLI:

1. builds and signs the intent with a UUIDv7 `intent_id` (`--idempotency-key` to supply your own),
2. gift-wraps it when the domain is `org`, `secret`, or `notification` (a NIP-44-capable signer is required),
3. stores the signed event in its local outbox (`$XDG_DATA_HOME/bahia/outbox.bolt` or `~/.local/share/bahia/outbox.bolt`),
4. subscribes for the status, publishes, and requires at least one relay `OK`,
5. waits up to `--result-timeout` for the daemon's `30315` intent status.

Exit codes: `0` accepted, `1` rejected or conflict, `2` published but no status received (the command prints `intent_id` and `event_id`; inspect with `bahia outbox list`), `3` no relay accepted the event. Retry a timed-out intent with the same `--idempotency-key` rather than minting a new one.

Updates carry the record's canonical `updated_at` as `expected_updated_at`; on a stale revision the daemon answers `conflict`, so re-read and retry deliberately.

Two operations are confidential request/response calls instead of intents: `logs run` (`deployments/run-logs-get`) and secret reveal (`services/secrets-reveal`, used by the web app). `logs run` waits for its reply subscription to reach EOSE before publishing, then republishes the same keyed request up to `--result-retries` times after each timeout so the daemon can replay its cached response.

## Commands

### auth

```bash
bahia auth inspect
```

### services

```bash
bahia services list
bahia services get <service-id>
bahia services create --org "$ORG" --name payment-api --artifact-repo ghcr.io/acme/payment-api \
  --repo-source gitea --repo-coordinate acme/payment-api --clone-url https://git.example/acme/payment-api.git \
  --ci-provider hiveci --ci-workflow .hive/ci.yaml --runtime-type compose
bahia services update --org "$ORG" --service <service-id> --name payment-api-v2

# Direct runtime actions (also `bahia services actions deploy|restart|stop`)
bahia services deploy  --org "$ORG" --service <service-id> --environment <env-id> [--artifact <artifact-id>]
bahia services restart --org "$ORG" --service <service-id> --environment <env-id>
bahia services stop    --org "$ORG" --service <service-id> --environment <env-id>
```

`create` accepts `--id` to pin a client-minted UUID so a retried create is idempotent. Direct actions are `runtime` intents authorized by the `deployments:write` role in the organization; the daemon must have `direct_runtime_actions.enabled`.

### app

```bash
bahia app onboard --org "$ORG" --name payment-api --artifact-repo ghcr.io/acme/payment-api \
  --repo-coordinate acme/payment-api --clone-url https://git.example/acme/payment-api.git \
  --environment production [--policy require-sbom] [--strategy replace] [--runtime-type compose]
```

Creates the service, its environment, and an optional pipeline policy binding in one workflow; pass `--idempotency-key` to retry after inspecting partial results.

### environments

```bash
bahia environments list
bahia environments get <environment-id>            # includes deployment units
bahia environments create --org "$ORG" --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json --expected-updated-at <rfc3339>
bahia environments units list   <environment-id>
bahia environments units create <environment-id> --file unit.json [--default-unit-key max]
bahia environments units update <environment-id> <key> --file unit.json
```

`--units-file` replaces the complete explicit unit set (`[]` returns to the implicit default); omitting it on update leaves units unchanged. Unit flags (`--unit-key`, `--unit-runtime-type`, `--unit-endpoint-ref`, `--unit-compose-dir`, `--unit-reconcile-mode`, …) describe a single unit inline; unit JSON follows `schemas/deployment_unit.json`. Environment-level flags cover `--strategy` (`replace`, `blue_green`, `canary`), `--reconcile-mode` and `--default-reconcile-mode` (`observe_only`, `auto_apply`, `approval_required`, `disabled`), `--protected`, `--secret-scope-mode` (`service`, `environment`, `unit`), `--failure-domain-label`, `--runtime-config-file`, and `--loom-worker-selector-file`.

### builds and artifacts

```bash
bahia builds request --org "$ORG" --service <service-id> --git-ref <branch|tag|sha> \
  --credential-ref <repository-credential-secret-id> --artifact-repo <registered-repo> \
  [--idempotency-key <uuidv7>] --result-timeout 120s
bahia builds list --service <service-id> [--limit 20 --offset 0]
bahia builds get --build <build-id>

bahia artifacts list --service <service-id> [--limit 50 --offset 0]
bahia artifacts get --artifact <artifact-id>
bahia artifacts register --service <service-id> --build <build-id> --image-repo <repo> --image-tag <tag> --image-digest sha256:… [--id <uuid>] [--sbom-url …] [--signature-ref …] [--scan-status …] [--metadata-file …]
bahia artifacts import-observed --service <service-id> --environment <env-id> [--deployment-unit <unit-id>] \
  --image-repo <repo> --image-tag <tag> --image-digest sha256:<observed-digest>
```

`builds request` queues a Hive-CI run through the daemon's mirror initiator (`hiveci.initiator.enabled`); the accepted status `data` carries the build ID, and the daemon registers the resulting artifact from signed Hive-CI evidence. The organization is derived from the service; a supplied `--org` must match. First-time mirroring can exceed 30 seconds, so raise `--result-timeout`. `--build-arg` is accepted by the parser but the fleet-local dispatch contract has no build-argument field, so a request with build arguments is rejected before any side effect.

`import-observed` registers an image that is already running and that the daemon itself observes (digest and `bahia.*` container labels must match) as governed lineage. It is gated by `hiveci.allow_live_artifact_import` (default `false`) and never changes desired state; follow it with a reviewed deployment.

### deployments

```bash
bahia deployments preview --service <service-id> --environment <env-id> --artifact <artifact-id> \
  [--managed-runtime-config-file runtime.json] [--compact]
bahia deploy   --org "$ORG" --service <service-id> --environment <env-id> --artifact <artifact-id> \
  [--deployment-unit <unit-id>] [--expected-desired-state-hash <hash-from-preview>]
bahia rollback --org "$ORG" --service <service-id> --environment <env-id> --deployment-unit <unit-id> \
  --target-artifact <prior-artifact-id> --supersedes-intent <current-intent-id>
bahia deployments approve --org "$ORG" --intent <intent-id> --expected-updated-at <rfc3339>
bahia deployments reject  --org "$ORG" --intent <intent-id> --expected-updated-at <rfc3339>
bahia deployments route-attach --service <service-id> --environment <env-id> --deployment-unit <unit-id> \
  --hostname api.example.com --upstream-port 8080 --health-path /healthz [--internal=false]
```

`bahia deploy` and `bahia rollback` are shorthand for `bahia deployments deploy|rollback`. `preview` returns the authoritative desired-state hash and a bounded plan in the accepted status; `--compact` returns only the hash and a structural summary (environment variable *names* only). `route-attach` plans and applies managed HTTPS routing for the current artifact without redeploying it; internal HTTPS is attached automatically when configured unless `--internal=false`.

### state

```bash
bahia state list
bahia state drifted        # records whose drift_status is exactly "drifted"
```

### adopt

```bash
bahia adopt scan   --target prod=prod-docker [--environment prod=production] [--offset 0 --limit 20]
bahia adopt import --org "$ORG" --target prod=prod-docker --all
bahia adopt import --org "$ORG" --target prod=prod-docker --select prod/<container-id>=payment-api
```

Targets are server-managed endpoint references (`alias=endpointRef`); `--raw-target alias=dockerHost` is accepted only when the daemon sets `adoption.allow_raw_docker_hosts`. `scan` returns a redacted findings page (`total_findings`, `next_offset`, `truncated`) in the accepted status; `import` returns an intent ID and candidate count and the imported services appear as canonical records. Both require the signer in `adoption.allowed_pubkeys`.

### dns

```bash
bahia dns zone-create --name prod.example --visibility external --backend-ref powerdns-prod --ttl 300 [--authoritative]
bahia dns zone-update   --file zone.json
bahia dns zone-delete   --name prod.example --expected-updated-at <rfc3339>
bahia dns endpoint-create|endpoint-update --file endpoint.json
bahia dns endpoint-delete --coordinate <id> --expected-updated-at <rfc3339>
bahia dns backend-create|backend-update --file backend.json
bahia dns backend-delete --ref <id> --expected-updated-at <rfc3339>
bahia dns policy-apply  --file dns-policy.json
bahia dns policy-update --file dns-policy.json
bahia dns policy-delete --id <id> --expected-updated-at <rfc3339>
bahia dns record-set --zone prod.example --name api --type A --value 192.0.2.10 --ttl 60 \
  --reason "incident pin" [--expires-at 2026-09-04T12:00:00Z]
bahia dns override-retire --override-id <uuid> --reason "projection is authoritative"
bahia dns drift-remediate [--zone prod.example]
```

DNS is fleet-scoped: no `--org` is needed, and the signer must be a fleet operator. `record-set` pins a record as an override; `override-retire` expires it (the row remains as audit and retrying is idempotent). Zone visibility is `internal`, `external`, `edge`, or `mesh`; record types are `A`, `AAAA`, `CNAME`, `SRV`. See [DNS](features/dns.md).

### workers

```bash
bahia workers list
bahia workers show <worker-pubkey>
bahia workers cordon|uncordon|drain|undrain|maintenance-enter|maintenance-exit <worker-pubkey> [--reason …]
bahia workers labels-update <worker-pubkey> --labels '{"gpu":"a100"}'
bahia workers cleanup <worker-pubkey> [--mode reclaimable_only|aggressive] [--reason …]
bahia workers cleanup-orphans [--apply]      # dry-run by default
```

### logs

```bash
bahia logs run <run-id> [--tail 100] [--stream stdout|stderr|merged] [--encrypted]
```

### policies

```bash
bahia policies list
bahia policies get <policy-id>
bahia policies create --name require-sbom --rules '[{"type":"require_sbom"}]' --enforcement block [--environment <env-id>]
```

Policy mutations are fleet-scoped; `--enforcement` is `warn` or `block`.

### config

```bash
bahia --relay wss://relay.example --service-pubkey <daemon-pubkey> config publish --file config-request.json
bahia --relay wss://relay.example --service-pubkey <daemon-pubkey> config drift
bahia --relay wss://relay.example --service-pubkey <daemon-pubkey> config rollback <desired-event-id>
```

Config fabric desired state is published directly to relays as NIP-51/NIP-78 events signed by the operator; the daemon answers with `config-status:<service>:<policy>:<scope>` records. `publish` and `rollback` wait for EOSE from every relay before choosing the next version; `drift` compares desired versions with applied, rejected, and withdrawn status and can report stale local data with a warning. `--outbox-path` overrides the CLI outbox used for per-relay OK tracking.

### secrets

```bash
bahia secrets list <service-id>                                     # metadata only
bahia secrets set  <service-id> DATABASE_URL postgres://…  [--environment <env-id>]
bahia secrets set  <service-id> DATABASE_URL --value-file /run/secrets/db   # owner-only absolute path
bahia secrets delete <service-id> <secret-id>
```

Secret values travel only inside gift-wrapped intents and are never printed by the CLI; reveal is available in the web app.

### orgs

```bash
bahia orgs list
bahia orgs get <id-or-name>
bahia orgs create <name> [--display-name "ACME Corporation"]
bahia orgs delete <org-id>
bahia orgs members list <org-id>
bahia orgs members add    <org-id> <pubkey> --role viewer|deployer|admin|owner
bahia orgs members remove <org-id> <pubkey>
bahia orgs invites create <org-id> <pubkey> [--role viewer] [--expires-in 72]
bahia orgs invites delete <org-id> <invite-id>
```

Creating an organization requires a fleet operator or the organization's configured bootstrap owner.

### notifications

```bash
bahia notifications channels list
bahia notifications channels get <channel-id>
bahia notifications channels create --file channel.json     # full document, including confidential config
bahia notifications channels update --file channel.json
bahia notifications channels delete <channel-id>
```

Reads omit webhook URLs and credentials.

### package

```bash
bahia package repo apply --name libs --format npm --backend-type nexus --backend-ref nexus-main [--policy '{…}'] [--config '{…}']
bahia package repo delete --name libs [--force] [--reason …]
bahia package upload  --repository libs --package widgets --version 1.0.0 --file ./dist/widgets-1.0.0.tgz
bahia package promote --source-repository libs --target-repository production --package widgets --version 1.0.0 --filename widgets-1.0.0.tgz
bahia package yank    --repository production --package widgets --version 1.0.0 --filename widgets-1.0.0.tgz --reason "security issue" [--deprecated]
bahia package drift   --repository production [--include-artifacts]
```

Formats: `npm`, `pypi`, `conan`, `deb`, `rpm`, `pub`, `go_modules`, `gradle`. Backends: `nexus`, `pulp`, `filesystem_mock`. Repositories may be addressed by `--repository-id` instead of name.

### souls

```bash
bahia souls list [--status active|suspended|revoked] [--limit 50]
bahia souls get <agent-id>
bahia souls provision <agent-id> --template "31950:<pubkey>:<identifier>" [--tier lightweight|standard|heavy] [--brief …|--brief-file …] [--follow]
bahia souls await <request-id>
bahia souls suspend|resume|redeploy <agent-id>
bahia souls revoke <agent-id> --reason "…" [--force]
bahia souls regenerate <agent-id> --brief "…"
bahia souls templates list [--tier …]
bahia souls templates get <identifier>
```

`souls` speaks the SoulFactory event contract directly (`5950` requests, `6950`/`7950` progress and results, `1950` actions, `31950`/`31951` templates and Souls) and needs `soul_factory.enabled` on the daemon. `--reply-timeout` (`BAHIA_SOUL_FACTORY_REPLY_TIMEOUT`, default 15m) bounds the wait for a terminal result.

### outbox

```bash
bahia outbox counts
bahia outbox list [--state pending|failed|published|all] [--limit 50]
bahia outbox retry <event-id> | --all
bahia outbox prune [--max-age 168h] [--confirm]     # dry run without --confirm
bahia outbox --daemon counts                          # daemon outbox, read-only
```

`--daemon` reads the daemon's outbox at `$BAHIA_DATA_DIR/nostr-cache/outbox.bolt`; `--outbox-path` overrides either location. Use `retry` on entries the daemon has abandoned (the `/health` check `canonical_delivery` lists them).

## Operator tools outside the CLI

- `bahia-migrate` (`--config config.yaml`) manages the optional PostgreSQL index: `status` (exit 2 when migrations are pending), `up`, and `down --confirm [--to <stem>] [--force]`. `bahia-migrate nostr [--dry-run] [--relays …] [--relay-backfill]` converts event records stored under non-canonical event kinds in `nostr_events` into canonical events and publishes them; it is resumable and idempotent. `make migrate MIGRATE_CONFIG=… MIGRATE_ACTION=status` wraps the same binary.
- `bahia-migrate f74a-census --config <path> [--cutoff <RFC3339>] [--f74a-timeout 30m]` reads a repeatable-read PostgreSQL snapshot and reports F74a observation, package, and publication counts without changing data or the mounted configuration. `bahia-migrate f74a-compact --config <path> --cutoff <past-RFC3339> [--f74a-timeout 30m]` is also **read-only**. Its `hot_suppressible_observations_before_cutoff` is the exact count of old, unlinked, no-op observations still physically hot in that snapshot; `suppressible_observations_before_cutoff` also includes already archived history. The default deadline is 30 minutes (`--f74a-timeout` accepts 1 second through 24 hours); timeout cancels the snapshot. Counts can change after the snapshot. `f74a-compact --confirm` is explicitly disabled: no trusted backup and isolated-restore receipt proves coverage of this same Bahia PostgreSQL database. See the [F74a compaction runbook](../runbooks/f74a-backfill-and-compaction.md).
- `bahia-policy-census --config <path> --relays <url,...> [--max-rows 1000]` audits legacy SQL deployment-policy coordinates against an operator-supplied relay set using complete per-relay EOSE. It reports relay-present conflicts without signing or publishing; coordinates with no valid relay event fail closed rather than receiving an absence verdict. The supplied set must match the signed canonical relay policy after sidecar precedence and be confirmed by EOSE on both bootstrap and effective relays; this read-only binding is **not** import authorization or a writer fence. Any unavailable, refusing, or truncated relay aborts with no partial report. See the [policy census runbook](../runbooks/legacy-policy-census.md).
- `bahia-migrate legacy-cutover --config <path>` emits one read-only JSON census of the legacy SQL families and pending/failed signed SQL outbox. Its `blockers` list identifies every nonempty source and exact row count; it exits nonzero on any blocker or incomplete census. `--confirm-quiesced --outbox-path <absolute-daemon-outbox>` can seal only a fully empty census in the stopped daemon's existing local outbox. Neither mode imports SQL rows or proves relay delivery. See the [offline cutover procedure](../runbooks/legacy-sql-cutover.md).
- `bahia-migrate outbox-transfer --target default|control-plane --config <path> [--after <token>] [--max-rows 1000]` is a **read-only inventory** of pending legacy PostgreSQL signed-outbox rows. It validates each event ID and signature and reports rows with recorded relay attempts as conflicts. Each page reads at most 10,000 rows in bounded SQL batches and emits `next_after` for the following page; the token carries the cumulative conflict count so a later clean page cannot hide an earlier conflict. Save and reuse the token verbatim across restarts, but treat it as a moving keyset position, not a snapshot: concurrent inserts or updates behind it can be missed. Quiesce SQL writers for an exhaustive census; otherwise repeat a full census from the beginning and reconcile changes. `next_after` empty only ends that pass. Every intact zero-recorded-attempt row reports `prior_relay_acceptance=unknown`; a relay may already have accepted it. The command never enqueues, re-targets, signs, or publishes rows. `--apply` explicitly fails before database access: no enforceable old-publisher fence, cross-store activation, or effective durable relay-policy proof is available. See the [transfer safety boundary](../runbooks/nostr-event-store-lifecycle.md#transfer-activation-safety-boundary).
- `soulfactory-legacy-adoption-report -input <snapshot.json>` writes a deterministic report from a sanitized snapshot of running agent containers; it contacts nothing and exits 3 when an agent has conflicting Soul identity evidence.

## Related

- [Getting Started](getting-started.md)
- [MCP Tools](mcp-tools.md)
- [Nostr Integration](nostr-integration.md)
