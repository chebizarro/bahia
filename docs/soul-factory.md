# Soul Factory

Soul Factory provisions and operates agent identities and runtimes from signed
Nostr requests. Bahia coordinates Signet custody, optional profile/community
setup, workspace and memory adapters, runtime control, service/deployment
records and the final soul read model.

## Event contract

| Kind | Role |
|---|---|
| `31950` | soul template |
| `31951` | authoritative soul read model |
| `31952` | editable signed soul draft |
| `5950` / `6950` / `7950` | provisioning request, progress and terminal result |
| `1950` | lifecycle action request |
| `30317` | runtime capability announcement |
| `38384` / `38386` | runtime control request and result |
| `30900` / `4903` | canonical provisioning, saga/ledger and audit projections |

The ContextVM methods `soul-factory/provision` and `soul-factory/action`
acknowledge dispatch. Completion is the correlated `7950`, final `31951` and
canonical projections; `EOSE`, a JSON-RPC response or a timeout is not
completion.

## CLI

```bash
bahia souls list --status active --limit 20
bahia souls get scout
bahia souls templates list --tier standard
bahia souls templates get research-agent

bahia souls provision scout --brief-file ./agent-brief.md --tier standard --follow
bahia souls provision scout --template 31950:<pubkey>:research-agent --follow
bahia souls suspend scout --reason 'Maintenance'
bahia souls resume scout
bahia souls redeploy scout
bahia souls revoke scout --reason 'Decommissioned'
bahia souls regenerate scout --brief-file ./new-brief.md
bahia souls await <request-event-id>
```

`--reply-timeout` defaults to `15m` and only bounds the caller's wait. It does
not change the operation's relay state.

The package-local tool set in `internal/soulfactory/mcp_server.go` is not part
of the standard Bahia external MCP registry. Web and CLI operations use the
signed Nostr contract above.

## Provisioning

The provisioner executes these stages, recording each in the saga:

1. resolve and verify the signed draft/template or generate from an inline
   brief;
2. provision a Signet-custodied identity and allowed kinds;
3. generate/store an avatar when configured;
4. publish the kind-`0` profile through Signet;
5. create the Qdrant collection when configured;
6. register and seed agent memory when configured;
7. initialize the workspace when configured;
8. register NIP-05, persist the soul, bind the selected runtime, create Bahia
   service/deployment records and publish `31951`.

A referenced `31952` draft and its `spec_hash` are authoritative; Bahia does
not regenerate that snapshot through the LLM.

Service-only adapter state is projected as fleet-OCK encrypted
`soul-factory-adapter-ledger` records. Saga progress is
`soul-factory-saga-run`. Runtime-control policy is a protected
`soul-factory-runtime-policy` record.

## Runtime selection and control

`soul_factory.agent_runtimes` is the administrative allowlist; unset defaults
to `openclaw`. `runtime_pubkeys` may pin each enabled runtime to exact signing
identities. Dispatch additionally requires a fresh, schema-compatible `30317`
from an allowed signer advertising the requested method. Capabilities older
than 10 minutes are ineligible.

The generic runtime contract is [SoulFactory runtime control](soulfactory-runtime-control.md).
The packaged OpenClaw path supports `soulfactory.provision`,
`soulfactory.update`, `soulfactory.persona.update` and `soulfactory.revoke`.

A `38384` request is successful only after a fully correlated `38386` signed
by the selected runtime. `soul_factory.runtime_result_timeout` defaults to
`5m`; expiry parks the operation as `awaiting_terminal` and publishes no
terminal `7950`. A valid result arriving later resumes the same continuation.
Actions and fleet reloads are serialized per soul and reuse their idempotency
keys after restart.

The reactor maintains per-relay cursors after EOSE and reconnects with a
10-minute overlap. Handlers deduplicate replayed events and terminal results.

## Community integrations

### NIP-29

Each `soul_factory.nip29_groups` entry names a relay and group ID. Provisioning
NIP-42-authenticates and publishes controller-signed kind-`9000` put-user
events. Every configured group must accept the event.

### Communikeys

Each entry names an exact `32222:<owner>:<community-id>` definition, delegated
`list_author`, section purposes and optional shard. Bahia verifies that the
definition references each computed kind-`30000` profile-list coordinate,
adds the agent `p` tag without dropping existing data, signs with the delegated
controller and requires relay `OK`. Bad definitions, list-author mismatches,
missing coordinates and rejected writes fail provisioning.

### Concord

A Concord entry supplies one CORD-05 invite source:

```yaml
concord_communities:
  - community_id: <64-hex>
    invite_bundle_sealed_file: /run/secrets/community.sealed
```

Exactly one of `invite_bundle_env`, absolute `invite_bundle_file` or absolute
`invite_bundle_sealed_file` is allowed. The sealed-file form is NIP-44 custody
under the Signet-held staff key and is the writable source for CORD-06
rotation. Bahia validates the self-certifying community, encrypts a direct
invite to the new agent, publishes to every configured community relay and to
the recipient's declared inbox relays, and records no bundle plaintext.

The implemented rotation permission is a validated-owner rekey of named
Private Channels. Non-owner rotations, Public-Channel-only rekeys and
Refounding are rejected before key generation, custody writes or publication.
A successful rekey advances the named channel epochs, writes verified sealed
custody, publishes CORD-06 rekey blobs and sends direct invites to the supplied
survivors. The receipt contains epochs, commitments and recipients, never root
or channel keys.

## Configuration

```yaml
soul_factory:
  enabled: true
  provisioning_state_dir: ./data/soulfactory/provisioning
  organization_id: <organization-uuid>
  agent_environment_id: <environment-uuid> # optional
  agent_runtimes: [openclaw]
  runtime_pubkeys:
    openclaw: [<runtime-pubkey>]
  relays: [wss://relay.example]
  additional_relays: []
  nip05_relays: []
  authorized_pubkeys: [<operator-pubkey>]
  soul_factory_pubkey: <controller-pubkey>
  signet_bunker_uri: bunker://<signet-pubkey>?relay=wss%3A%2F%2Frelay.example
  signet_client_secret_key: <secret>
  startup_timeout: 15s
  llm_base_url: https://llm.example
  llm_model: <model>
  llm_api_key: <secret>
  llm_timeout: 2m
  runtime_result_timeout: 5m
  reply_timeout: 15m
```

When enabled, validation requires at least one relay and authorized pubkey,
positive timeouts and a valid LLM origin/model/key. Outside `dev_mode`, a
Signet bunker URI is required. Workspace fields are optional but the Gitea
URL requires both secret references. Runtime pins may name only enabled
runtimes and must contain valid 64-hex pubkeys.

## Operations

- OpenClaw deploy: [sidecar runbook](soul-factory-sidecar-runbook.md)
- OpenClaw runtime: [sidecar reference](openclaw-soulfactory-sidecar.md) and
  [control wrapper](openclaw-soulfactory-control-wrapper.md)
- Provisioning recovery/retention: [operations runbook](runbooks/openclaw-provisioning-operations.md)
- Metiq enablement: [Metiq runbook](runbooks/metiq-runtime-enablement.md)

Troubleshooting starts with relay `OK`, operator authorization, a fresh trusted
`30317`, correlated `6950`/`7950` and `38384`/`38386`, then `/ready`. Signet
failure degrades readiness without taking down liveness; the reconnect loop
recovers without a daemon restart.
