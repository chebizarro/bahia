# CLI Reference

The `bahia` CLI provides command-line access to Bahia’s current HTTP-compatible read surfaces plus signer-first operator commands.

## Installation

```bash
# From source
go install github.com/openagentsinc/bahia/cmd/cli@latest

# Or build locally
cd bahia
make build
./bin/bahia --help
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `BAHIA_NOSTR_KEY_FILE` | File containing a local Nostr private key for signer-first operations | unset |
| `BAHIA_NOSTR_BUNKER_FILE` / `BAHIA_NOSTR_BUNKER_URI` | File containing, or direct value of, the NIP-46 bunker URI | unset |
| `BAHIA_NOSTR_BUNKER_RELAYS` | Comma-separated NIP-46 signer relay URLs | unset |
| `BAHIA_NOSTR_CLIENT_KEY_FILE` | Persistent NIP-46 client private-key file | unset |
| `BAHIA_NOSTR_CLIENT_PRIVATE_KEY` | Raw persistent NIP-46 client private key | unset |
| `BAHIA_NOSTR_NSEC` | Nostr private key in `nsec` form | unset |
| `BAHIA_NOSTR_PRIVATE_KEY` | Raw Nostr private key hex | unset |
| `BAHIA_NOSTR_RELAYS` | Comma-separated final relay URLs for signer-first operator transport | unset |
| `BAHIA_NOSTR_BOOTSTRAP_RELAYS` | Comma-separated bootstrap relay seeds used only when final relay sources are absent | unset |
| `BAHIA_NOSTR_SERVICE_PUBKEY` | Bahia service pubkey for signer-first routing and single-service discovery trust | unset |
| `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS` | Comma-separated trusted Bahia service pubkeys for bootstrap discovery | unset |
| `BAHIA_RESULT_TIMEOUT` | Maximum wait for a service/environment intent status (30315) | `30s` |

## Database migrations (operator command)

`bahia-migrate` is a separate, database-local command. It loads Bahia's protected
configuration (`--config`, default `config.yaml`) and never starts the server.
Use a valid deployment configuration and back up the database before rollback.

```bash
bahia-migrate --config /etc/bahia/config.yaml status
bahia-migrate --config /etc/bahia/config.yaml up
bahia-migrate --config /etc/bahia/config.yaml --confirm down
bahia-migrate --config /etc/bahia/config.yaml --confirm --to 000065_runtime_release_deployment_intents down
# With a valid config, make migrate runs up; select another action explicitly:
make migrate MIGRATE_CONFIG=/etc/bahia/config.yaml MIGRATE_ACTION=status
```

`status` only reads: it never creates `schema_migrations`, and prints each applied
**full filename stem** with `applied_at` plus every pending stem. It exits 0 when
nothing is pending, 2 when migrations are pending, and 1 on error. `up` applies
pending migrations under the same advisory lock used by server startup.

`down` requires `--confirm`. Without `--to`, it rolls back exactly the most
recently applied migration. `--to <stem>` keeps that applied migration and rolls
back newer applied migrations in reverse application order. By default the
command also requires each removed migration to be the highest applied filename
stem; `--force` overrides this ordering check, **not** SQL guards, confirmation,
or missing-script errors. Each down script and its version-row deletion commit
in one transaction. A missing `.down.sql` refuses the entire plan before any
rollback. A failed SQL guard leaves its version row intact. Do not substitute a
numeric prefix for a stem: seven historic prefixes have multiple migrations.

### Legacy Nostr event migration (`bahia-migrate nostr`)

`bahia-migrate nostr` converts legacy Bahia custom events recorded in
`nostr_events` (`internal/nostrmigration.LegacyKinds()`) into canonical events,
signs them with `nostr.private_key`, and publishes them. The daemon does not run
this on startup: re-signing and republishing old rows is not something a
restart should do (audit B-28).

```bash
# Report what would be migrated; signs and publishes nothing.
bahia-migrate --config /etc/bahia/config.yaml --dry-run nostr
# Migrate and publish to the sidecar plus nostr.relays (the default target).
bahia-migrate --config /etc/bahia/config.yaml nostr
# Publish elsewhere, and also read legacy events back from those relays.
bahia-migrate --config /etc/bahia/config.yaml --relays wss://relay.example --relay-backfill nostr
```

Run it once per deployment after upgrading from a release that still wrote
legacy kinds, with the Bahia database reachable. Then run it again only if a
dry run reports unmigrated records, for example after restoring an old
database backup. It is resumable (durable cursors in `nostr_events`) and
idempotent (a record whose canonical output tagged `migrated-from=<id>` exists
is skipped), so re-running is safe. `--relay-backfill` (default:
`nostr.legacy_relay_backfill`) also reads legacy kinds from the target relays;
the hardened sidecar refuses those reads, so point `--relays` at the legacy
relay when you need it. `--dry-run`, `--relays` and `--relay-backfill` are only
valid for `nostr`.

## Authentication

The CLI does not implement interactive `login` commands. The only built-in auth helper is:

```bash
bahia auth inspect
```

For local signing, use `--nostr-key-file`, `BAHIA_NOSTR_KEY_FILE`, `BAHIA_NOSTR_NSEC`, or `BAHIA_NOSTR_PRIVATE_KEY`.

For NIP-46 remote signing, use `--nostr-bunker-file` (or `BAHIA_NOSTR_BUNKER_FILE` / `BAHIA_NOSTR_BUNKER_URI`), at least one signer relay from the bunker URI, repeatable `--nostr-bunker-relay`, or `BAHIA_NOSTR_BUNKER_RELAYS`, and a persistent client key via `--nostr-client-key-file`, `BAHIA_NOSTR_CLIENT_KEY_FILE`, or `BAHIA_NOSTR_CLIENT_PRIVATE_KEY`. The CLI refuses simultaneous local-key and bunker configuration and does not generate a throwaway NIP-46 identity.

## Nostr-native transport

Service/environment create and update publish signed kind `30900` intents directly, subscribe for kind `30315` status, and read the resulting canonical `30900` state. Other signer-first CLI mutations still use ContextVM JSON-RPC over kind `25910`. Plain transport remains the default for those commands. Pass `--encrypted` to wrap the signed inner request in a NIP-59 kind `1059` gift wrap; encrypted mode requires `--service-pubkey` and works with either a local key or a NIP-46 signer that supports NIP-44. Reads consume canonical observable/state kinds (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`) and standard NIPs.

For service/environment/deployment/runtime writes, the CLI enqueues the signed intent in its local outbox, subscribes before publishing, requires at least one relay OK, and waits up to `--result-timeout` (default `30s`, or `BAHIA_RESULT_TIMEOUT`) for status. Exit codes are 0 accepted, 1 rejected/conflict/superseded, 2 published without status, and 3 no relay accepted. Exit 2 prints `intent_id` and `event_id`; inspect the pending event with `bahia outbox list`. These writes do not use HTTP.

For remaining ContextVM commands, before publishing, the CLI waits for the reply subscription to reach EOSE on its established relays. Each publish attempt waits up to `--result-timeout` (default `30s`). On timeout it re-subscribes and republishes the same logical request up to `--result-retries` times (default `2`); the stable `d` tag lets Bahia replay its cached idempotent response. Bahia keeps completed idempotent responses in memory and in PostgreSQL for 24 hours, so duplicate requests replay the terminal response without re-running the handler.

### Troubleshooting: CLI times out but server logs handler completed

A handler-completed log means the mutation ran, not that the ephemeral response reached a subscribed relay. The CLI automatically re-subscribes and republishes the same logical request after each result timeout; Bahia answers the duplicate from its response cache. If all attempts fail, use the CLI error fields `method`, `request_event_id`, `d`, `configured_relays`, `subscribed_relays`, `failed_subscriptions`, `published_relays`, `attempts`, and `publish_results` to correlate the request with server logs and per-relay acceptance or subscription failures. Do not submit a new idempotency key until that evidence is checked.

Operator relay resolution is deterministic and ordered:

1. Explicit `--relay` values are final and highest priority.
2. `BAHIA_NOSTR_RELAYS` is next.
3. Trusted bootstrap discovery is used only when both final relay sources are absent.

Trusted bootstrap discovery requires at least one bootstrap relay (`--bootstrap-relay` or `BAHIA_NOSTR_BOOTSTRAP_RELAYS`) and at least one trusted service pubkey (`--trusted-service-pubkey`, `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS`, or `--service-pubkey` / `BAHIA_NOSTR_SERVICE_PUBKEY`).

## Registered command groups

The current top-level CLI command groups are:

- `auth`
- `services`
- `environments`
- `state`
- `builds`
- `artifacts`
- `dns`
- `deployments`
- `adopt`
- `workers`
- `logs`
- `policies`
- `config`
- `secrets`
- `orgs`
- `notifications`
- `package`
- `souls`

Bahia does **not** currently register top-level `llm` or `payments` CLI commands.

## Commands

### Services

`services list` and `services get` read canonical service events from the configured relays by default. Pass `--service-pubkey` (or set `BAHIA_NOSTR_SERVICE_PUBKEY`) and configure a relay with `--relay` or `BAHIA_NOSTR_RELAYS`. Reads reuse a local cursor under `$BAHIA_DATA_DIR/store/<service-pubkey>/` or `$XDG_DATA_HOME/bahia/store/<service-pubkey>/`. The legacy REST read path is no longer mounted.

```bash
# List services
bahia services list
bahia services list -o json

# Get service by ID
bahia services get svc-123
bahia services get svc-123 -o yaml

# Publish a signed service intent (organization UUID required)
bahia services create --org "$ORG_UUID" \
  --name "payment-api" \
  --artifact-repo "ghcr.io/company/payment-api"
bahia services update --service <service-uuid> --name "payment-api-v2"

# Direct runtime lifecycle actions (UUIDs and organization required)
# The same commands are also available under `services actions`.
bahia services deploy --org "$ORG_UUID" --service "$SERVICE_UUID" --environment "$ENV_UUID" --artifact "$ARTIFACT_UUID"
bahia services restart --org "$ORG_UUID" --service "$SERVICE_UUID" --environment "$ENV_UUID"
bahia services stop --org "$ORG_UUID" --service "$SERVICE_UUID" --environment "$ENV_UUID"
```

### Environments

`environments list` and `environments get` read canonical environment events from relays by default; `get` includes the deployment-unit read model. The legacy REST read path is no longer mounted. Environment create/update publish signed `30900` intents and wait for `30315` status. Updates read the current canonical `30900` record, merge flags into the complete desired state, and include its `updated_at` revision. Deployment-unit helpers use the same canonical read and intent write path; there is no automatic HTTP fallback.

```bash
# Read environments (detail includes deployment_units)
bahia environments list
bahia environments get <environment-id>

# Create or update an environment from a complete unit-set file
bahia environments create --org "$ORG_UUID" --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json

# List explicit units or the marked implicit default
bahia environments units list <environment-id>

# Create or update one unit using a JSON specification
bahia environments units create <environment-id> --file unit.json --default-unit-key max
bahia environments units update <environment-id> max --file unit.json --default-unit-key max
```

Omitting `--units-file` leaves the unit set unchanged on update. Supplying a file replaces the complete explicit set; use a JSON `[]` to return to the implicit default. Complete-set updates carry the environment's `updated_at` revision. On conflict, the CLI reports the stale revision; re-read and retry deliberately rather than silently rebasing a complete-set mutation. `--default-unit-key` on unit create/update changes targeting in the same transaction; use it when the first explicit unit has a non-`default` key. Unit JSON follows `schemas/deployment_unit.json`.

### Builds

The `builds` group provides the signer-first request → follow → register-artifact path without SQL or ad hoc image injection. The service must have a repository coordinate, matching artifact repository, and an opaque repository-credential secret owned by that service. The server-side fleet mirror initiator must be enabled with `hiveci.initiator.enabled`; otherwise `builds request` returns `Gitea mirror and HiveCI build initiation are not configured`.

```bash
# Queue the exact Astillero commit through the governed HiveCI path.
bahia builds request \
  --service <service-uuid> \
  --git-ref b13b14fba6e54f008bfa1ba26d716c2ef05c206e \
  --credential-ref <repository-credential-secret-uuid> \
  --artifact-repo <registered-service-artifact-repo> \
  --idempotency-key build:astillero:b13b14f \
  --result-timeout 120s

# Follow durable build lineage. Production transitions are queued directly to
# succeeded or failed; Bahia does not currently project an intermediate running state.
bahia builds list --service <service-uuid>
bahia builds get --build <build-uuid>
bahia artifacts list --service <service-uuid>
bahia artifacts get --artifact <artifact-uuid>

# After the build succeeds, register only its verified HiveCI artifact result.
# Use the succeeded build ID shown by builds list; if CI correlation created a
# separate terminal row, it can differ from the queued ID returned by request.
bahia builds register-result --build <succeeded-build-uuid>

# Use the returned artifact ID in the normal reviewed deployment flow.
bahia deployments preview \
  --service <service-uuid> \
  --environment <environment-uuid> \
  --artifact <artifact-uuid>
bahia deployments deploy \
  --service <service-uuid> \
  --environment <environment-uuid> \
  --artifact <artifact-uuid> \
  --expected-desired-state-hash <reviewed-hash>
```

First-time mirror creation and ref resolution can exceed the default 30-second per-attempt result timeout, so `--result-timeout 120s` is recommended for the first request. Reusing the same `--idempotency-key` replays the first completed ContextVM result from Bahia's durable response store instead of starting another CI run or registering another build. An idempotency key identifies one logical request: do not reuse it with different request fields.

`--build-arg KEY=VALUE` is repeatable and values may contain `=`, but the fleet-local tag-only kind-5401 dispatch contract has no build-argument field. The private-mirror Hive-CI initiator therefore rejects non-empty build arguments before any secret resolution, mirror operation, event publication, or queued-build registration. Omit `--build-arg` for this workflow.

`builds get/list` and `artifacts get/list` read signed `30900` build-registry and artifact-registry records from relays by default, using the same local cursor and stale-EOSE warning policy as service reads. The legacy REST read endpoints are no longer mounted. Build and artifact mutations remain signer-first.

If the queued ID returned by `builds request` remains `queued` while `builds list --service` shows a newer `succeeded` row, use that succeeded row's ID with `register-result`; this is the recovery path when CI result correlation lands on a separate build row.

`builds register-result` accepts only a successful build. It resolves the immutable artifact from accepted HiveCI evidence and the configured registry; it does not permit an operator-supplied image override. Requesting or registering a build does not deploy it.

### Deployments

```bash
# Preview a managed desired state and review its authoritative hash.
# --compact returns only the hash plus a structural summary for cases where
# the full preview is too large to deliver over relays. The hash is identical
# and authoritative in both modes. Environment variable values are never
# included in the summary (only sorted key names). Compact mode prints a
# human-readable evidence summary in table format without requiring -o json.
bahia deployments preview --service svc-123 --environment env-456 --artifact art-789 \
  --managed-runtime-config-file runtime.json
bahia deployments preview --service svc-123 --environment env-456 --artifact art-789 \
  --managed-runtime-config-file runtime.json --compact

# Submit signer-first deployment intent
bahia deploy --org "$ORG_UUID" --service "$SERVICE_UUID" --environment "$ENV_UUID" --artifact "$ARTIFACT_UUID"

# Attach managed HTTPS/DNS routing to the current deployed artifact without redeploying it
bahia deployments route-attach --service svc-123 --environment env-456 \
  --deployment-unit unit-789 --hostname api.example.com \
  --upstream-port 8080 --health-path /healthz --internal
# Internal HTTPS is automatic when configured and zone-allowed; opt out explicitly:
bahia deployments route-attach --service svc-123 --environment env-456 \
  --deployment-unit unit-789 --hostname public-only.example.com \
  --upstream-port 8080 --health-path /healthz --internal=false

# Submit signer-first rollback intent
bahia rollback --org "$ORG_UUID" --service "$SERVICE_UUID" --environment "$ENV_UUID" --deployment-unit "$UNIT_UUID" --target-artifact "$PRIOR_ARTIFACT_UUID" --supersedes-intent "$CURRENT_INTENT_UUID"

# Approve or reject a pending deployment using its canonical revision
bahia deployments approve --org "$ORG_UUID" --intent "$INTENT_UUID" --expected-updated-at "$UPDATED_AT"
bahia deployments reject --org "$ORG_UUID" --intent "$INTENT_UUID" --expected-updated-at "$UPDATED_AT"
```

Deployment creation, rollback, approval/rejection, and `services actions deploy/restart/stop` publish signed `30900` intents, not ContextVM requests. They require `--org` and UUID entity IDs; `--idempotency-key` accepts a UUIDv7 for retrying one logical intent. The CLI persists the signed event in its outbox before relay publication, then waits for `30315` status. Exit codes are 0 accepted, 1 rejected/conflict, 2 published without status (inspect `bahia outbox list`), and 3 no relay accepted. These writes do not use HTTP. Configure the daemon's `deployment` and `runtime` intent domains before using them. Deployment preview and route-attach remain ContextVM calls and retain their retry keys.

### State

```bash
# List desired/observed state
bahia state list
bahia state list --output json

# Show drifted services
bahia state drifted
```

These reads use the Bahia service's signed `30900` service-state records by default. Set `--service-pubkey` and `--relay` (or their environment equivalents). `drifted` selects records whose `drift_status` is exactly `drifted`. A missing EOSE prints a stale-data warning to stderr and still exits 0 with the local-store result. The legacy REST read endpoint is no longer mounted.

### DNS

DNS mutations publish signed ContextVM kind `25910` requests and await their correlated acknowledgments. Configure an operator signer and relay using the global Nostr options described above.

```bash
# Create and reconcile a managed zone
bahia dns zone-create \
  --name prod.example \
  --visibility external \
  --backend-ref powerdns-prod \
  --ttl 300

# Apply a nested DNS policy document
bahia dns policy-apply --file dns-policy.json

# Pin a record, optionally until an RFC3339 timestamp
bahia dns record-set \
  --zone prod.example \
  --name api \
  --type A \
  --value 192.0.2.10 \
  --ttl 60 \
  --reason "incident pin" \
  --expires-at 2026-09-04T12:00:00Z

# Retire an existing pin once the projected record is authoritative.
# Retirement expires the override rather than deleting it, so the row remains
# as an audit record. It is idempotent: retrying reports the override as
# already inactive and does not move the recorded retirement time.
bahia dns override-retire \
  --override-id 1273e277-dfa7-4459-a452-89598eeca4a2 \
  --reason "Bahia now projects the zone authoritatively"

# Reconcile one zone or all configured zones
bahia dns drift-remediate --zone prod.example
bahia dns drift-remediate
```

`dns-policy.json` uses the DNS policy schema, including nested `match` and `action` objects. Unknown fields and invalid policies are rejected before publication:

```json
{
  "name": "edge-routing",
  "rules": [
    {
      "match": {"environment": "prod"},
      "action": {"visibility": "edge", "ttl_override": 60}
    }
  ],
  "enabled": true
}
```

### Workers

```bash
# List workers
bahia workers list

# Show worker detail
bahia workers show <64-character-worker-hex-pubkey>
```

### Logs

Completed run logs are fetched through the signed, keyed ContextVM request path; the HTTP-only live SSE CLI command has been removed.

```bash
# Fetch run logs
bahia logs run <run-uuid> --tail 100
```

### Policies

```bash
# Read policies
bahia policies list
bahia policies get <policy-uuid>

# Create a signer-first policy
bahia policies create \
  --name require-sbom \
  --rules '[{"type":"require_sbom"}]' \
  --enforcement block \
  --idempotency-key policy-create-require-sbom
```

Policy reads use signed `30900` policy-registry records by default. The legacy REST endpoint is no longer mounted. `get` requires a policy UUID and returns an error when that UUID is absent. The same stale-data warning and successful exit behavior applies when no relay reaches EOSE.

### Config fabric

```bash
# Sign a validated NIP-51/NIP-78 desired-state request with an operator nsec
# or NIP-46 bunker and publish directly to relays (per-relay OKs in CLI outbox).
bahia --relay wss://relay.example --service-pubkey <daemon-pubkey> \
  config publish --file config-request.json

# Compare desired events with applied/rejected/withdrawn status
# (WITHDRAWN: the desired event was deleted or expired; the last applied
# config stays live until a newer version is published)
bahia --relay wss://relay.example --service-pubkey <daemon-pubkey> config drift

# Republish a prior desired event at the next version
bahia --relay wss://relay.example --service-pubkey <daemon-pubkey> \
  config rollback <desired-event-id>
```

`publish` and `rollback` require `--nostr-key-file`/`BAHIA_NOSTR_NSEC` or a
NIP-46 bunker signer. The signer must be a configured fleet operator or a
trusted config author on the relay sidecar. Rollback requires a desired event
by the same operator retained in the local store or CLI outbox; it copies the
old policy/list payload and assigns the next version for that operator and
`(service, policy, scope)` coordinate. Drift reads local desired events and
the daemon's stable `config-status:<service>:<policy>:<scope>` v3 records;
`GET /config-fabric/drift` remains available for compatibility. Publish and
rollback require EOSE from every configured relay before choosing a version;
drift can still show stale local data with a warning when relays are unavailable.
The latest v3 status carries the last effective event even while a newer
desired version is merely accepted, so local drift retains the applied version.

### Secrets

`secrets list` reads OCK-encrypted `30900` secret references from relays by default. It returns metadata only; secret values are never included and remain available only through the authorized ContextVM reveal flow. A NIP-44-capable signer (`--nostr-key-file`/`BAHIA_NOSTR_NSEC`, or a NIP-46 bunker) and the Bahia service pubkey are required. The legacy REST read is no longer mounted.

```bash
# List secrets for a service
bahia secrets list svc-123

# Set a secret
bahia secrets set svc-123 DATABASE_URL postgres://example

# Delete a secret
bahia secrets delete svc-123 secret-456
```

### Organizations

`orgs list`, `orgs get`, and `orgs members list` read the service's signed `30900` records and unwrap the matching `32010` OCK envelope through the CLI signer. A non-member receives `not readable with this key` with exit code 0, not decrypted org data. The local event-store cursor is reused across reads; missing relay EOSE prints a stale warning while returning cached state. The legacy REST reads are no longer mounted.

```bash
# List organizations
bahia orgs list

# Get organization by ID or name
bahia orgs get acme-corp

# Create an organization
bahia orgs create acme-corp --display-name "ACME Corporation"

# List members
bahia orgs members list org-123

# Add a member
bahia orgs members add org-123 npub1member... --role deployer

# Remove a member
bahia orgs members remove org-123 npub1member...
```

### Notification channels

`notifications channels list` and `notifications channels get <channel-uuid>` read OCK-encrypted channel metadata from relays. Their output omits service-only webhook URLs and credentials, including for fleet-scoped channels. The signer, relay, service pubkey, stale-cache rules are the same as for organization reads.

```bash
bahia notifications channels list -o json
bahia notifications channels get <channel-uuid>
```

### Encrypted operator requests with a remote signer

`--encrypted` wraps operator requests in NIP-59 and requires the signer to
perform NIP-44 only. A Signet/NIP-46 bunker signer therefore works without any
local key material:

```bash
bahia --nostr-bunker-file /etc/bahia/signer-bunker-url \
  --service-pubkey <bahia-service-pubkey> \
  --encrypted \
  artifacts import-observed --service ... --environment ... \
  --image-repo ... --image-tag ... --image-digest sha256:...
```

Bahia's ContextVM transport never uses NIP-04, so a signer that implements only
the modern cipher is fully supported. Private key material is never required,
printed, or passed in argv: the bunker URI is read from a file.

### Importing an already-running image

Bahia governs images that CI attested. When an image is already running but has
no Bahia artifact — for example a locally built image deployed before its
release workflow existed — an authorized operator can import it as governed
lineage. **Never edit the database to bridge missing artifact or build state.**

```bash
# Import the image Bahia currently observes running for a service
bahia artifacts import-observed \
  --service <service-id> \
  --environment <environment-id> \
  --deployment-unit <deployment-unit-id> \
  --image-repo astillero \
  --image-tag 2729a7c \
  --image-digest sha256:<observed-manifest-digest>
```

The digest must match what Bahia itself observes running: the control plane, not
the operator, is the authority for what exists. The command also verifies the
observed container's `bahia.service_id`, `bahia.environment_id`, and
`bahia.deployment_unit_id` labels when present, and refuses if a registry that
knows the repository reports a different digest.

Importing provenance never deploys or promotes it. Desired state is unchanged;
align it afterwards with a reviewed deployment:

```bash
bahia deployments preview --service <service-id> --environment <environment-id> --artifact <artifact-id>
bahia deployments deploy  --service <service-id> --environment <environment-id> --artifact <artifact-id> \
  --expected-desired-state-hash <hash-from-preview>
```

The path is governed by `hiveci.allow_live_artifact_import` (default `false`).
When it is disabled the command fails before writing anything and names both the
config key and the Hive CI alternative. Prefer the Hive CI path whenever the
image came from CI: a signed kind 5402 carrying `BAHIA_ARTIFACT` registers a
digest-pinned artifact automatically.

### Package repositories and artifacts

```bash
# Create or update a package repository
bahia package repo apply \
  --name libs \
  --format npm \
  --backend-ref nexus-main \
  --backend-type nexus

# Delete a package repository
bahia package repo delete --name libs

# Upload an artifact
bahia package upload \
  --repository libs \
  --package widgets \
  --version 1.0.0 \
  --file ./dist/widgets-1.0.0.tgz

# Promote an artifact
bahia package promote \
  --source-repository libs \
  --target-repository production \
  --package widgets \
  --version 1.0.0 \
  --filename widgets-1.0.0.tgz

# Yank an artifact
bahia package yank \
  --repository production \
  --package widgets \
  --version 1.0.0 \
  --filename widgets-1.0.0.tgz \
  --reason "security issue"

# Trigger drift detection
bahia package drift --repository production
```

### Souls

Soul Factory is feature-gated and disabled by default unless Bahia is configured with `BAHIA_SOUL_FACTORY_ENABLED=true`.

```bash
# List souls
bahia souls list
bahia souls list --status active

# Get soul details
bahia souls get scout

# Provision
bahia souls provision scout \
  --template "31950:pubkey:research-agent" \
  --tier standard \
  --follow

# Lifecycle
bahia souls suspend scout --reason "Maintenance"
bahia souls resume scout
bahia souls revoke scout --reason "No longer needed"
bahia souls redeploy scout
bahia souls regenerate scout --brief "New purpose..."

# Templates
bahia souls templates list
bahia souls templates get research-agent
```

### Adoption

```bash
# Scan for containers (target syntax is alias=endpointRef)
bahia adopt scan --target prod=prod-docker

# Import discovered containers and bind the signed request to an organization
bahia adopt import --target prod=prod-docker --all --org 11111111-1111-1111-1111-111111111111
```

`--org` is part of the signed import request. Use the destination organization UUID; it is not client-only display metadata.

### Legacy agent Soul adoption report

`soulfactory-legacy-adoption-report` is a separate, read-only planning binary. It reads a sanitized JSON snapshot and writes a deterministic JSON report; it does not contact Docker, Bahia, Signet, or a relay.

```bash
go run ./cmd/soulfactory-legacy-adoption-report \
  -input internal/soulfactory/testdata/legacy_adoption_input.json
```

The command exits `3` if any running agent has multiple authoritative matches or conflicting trusted Soul identity evidence. Container and display names are report context only and never matching evidence. See [Legacy agent Soul adoption plan](../soulfactory-legacy-agent-adoption.md) for the input contract and operator review boundary.

## Output formats

```bash
# Table (default)
bahia services list

# JSON
bahia services list -o json

# YAML
bahia services get svc-123 -o yaml
```

## Global flags

| Flag | Description |
|------|-------------|
| `--nostr-key-file` | Read a local Nostr private key from a file (use `-` for stdin) |
| `--nostr-bunker-file` | Read a NIP-46 bunker URI from a file |
| `--nostr-bunker-relay` | Add a NIP-46 signer relay (repeatable) |
| `--nostr-client-key-file` | Read the persistent NIP-46 client key from a file |
| `--relay` | Specify final operator relay (repeatable; highest priority) |
| `--bootstrap-relay` | Specify bootstrap relay seed for trusted operator discovery (repeatable) |
| `--service-pubkey` | Specify Bahia service pubkey for routing and single-service discovery trust |
| `--trusted-service-pubkey` | Specify trusted Bahia service pubkey for bootstrap discovery (repeatable) |
| `--eose-timeout` | Maximum wait for relay EOSE on Nostr reads (default `5s`; env `BAHIA_EOSE_TIMEOUT`). If no relay reaches EOSE, cached data is printed with a stale warning on stderr and the read exits 0 |
| `--encrypted` | Use NIP-59/NIP-44 encrypted operator requests and replies; requires `--service-pubkey` |
| `--result-timeout` | Maximum wait for a 30315 status on service/environment intents, or a ContextVM result for remaining commands (default `30s`; intents also support `BAHIA_RESULT_TIMEOUT`) |
| `--result-retries` | Idempotent re-publishes after a result timeout (default `2`) |
| `-o, --output` | Output format (`table`, `json`, `yaml`) |
| `--help` | Show help |

## Related

- [Getting Started](getting-started.md) — Setup guide
- [MCP Tools](mcp-tools.md) — Programmatic access
- [Nostr Integration](nostr-integration.md) — Event model
