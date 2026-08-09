# Soul Factory

Soul Factory is Bahia's Nostr-native agent provisioning and lifecycle subsystem. It turns signed soul drafts and requests into Signet-custodied identities, runtime bindings, Bahia service/deployment records, and durable relay-visible read models.

## Control planes and event kinds

| Kind | Role |
| --- | --- |
| `25910` | Canonical ContextVM request transport for `soul-factory/provision` and `soul-factory/action` |
| `31950` | Parameterized replaceable soul template |
| `31951` | Parameterized replaceable authoritative soul read model |
| `31952` | Parameterized replaceable editable soul draft |
| `5950` | SoulFactory provisioning interoperability request |
| `1950` | Soul lifecycle action request |
| `6950` | Correlated provisioning or lifecycle progress |
| `7950` | Correlated terminal provisioning or lifecycle result |
| `30317` | Runtime capability announcement |
| `38384` / `38386` | Runtime control request/result |
| `30900` / `4903` | Canonical provisioning state/audit projections for ContextVM-originated provisioning |

The canonical ContextVM methods are `soul-factory/provision` and `soul-factory/action`. The JSON-RPC response acknowledges dispatch only. Progress and terminal truth remain the correlated `6950`/`7950` events, final `31951`, and—when the request arrived through ContextVM—the `30900` state and `4903` audit projections.

Lifecycle results use `7950`. Kind `1951` is a migration-only legacy alias.

## CLI

The `bahia souls` command in `cmd/cli/soulfactory.go` uses relay-backed reads and signed Nostr requests rather than REST lifecycle endpoints.

```bash
bahia souls list --status active --limit 20
bahia souls get scout

bahia souls provision scout --brief "Research and synthesize findings" --tier standard --follow
bahia souls provision scout --brief-file ./agent-brief.md --follow
bahia souls provision scout --template 31950:<template-pubkey>:research-agent --follow

bahia souls suspend scout --reason "Maintenance"
bahia souls resume scout
bahia souls redeploy scout
bahia souls revoke scout --reason "No longer needed"
bahia souls regenerate scout --brief-file ./replacement-brief.md

bahia souls templates list --tier standard
bahia souls templates get research-agent
```

Global output flags such as `-o json` apply where supported by the root command. `revoke` prompts for the agent ID unless `--force` is supplied.

## Package-local MCP surface

`internal/soulfactory/mcp_server.go` defines:

- `soul_factory_list_souls`
- `soul_factory_get_soul`
- `soul_factory_list_templates`
- `soul_factory_provision`
- `soul_factory_action`
- `soul_factory_regenerate`
- `soul_factory_get_status`

This package-local server is tested, but it is not registered in the standard Bahia application's external MCP tool registry. Do not assume those names are remotely available without explicit integration wiring. The CLI and web UI use signed Nostr paths.

## Provisioning workflow

The full provisioner executes eight stages:

1. **Generate** — resolve a signed draft/template/inline brief; invoke the LLM only when no signed draft supplies the authoritative snapshot.
2. **Signet** — provision the agent identity and allowed kinds through Signet.
3. **Avatar** — generate/store an avatar when configured; otherwise record the stage as skipped.
4. **Profile** — publish the agent's kind-`0` profile using Signet-held signing.
5. **Qdrant** — create the vector collection when configured.
6. **Memory** — register and seed agent-memory when configured.
7. **Workspace** — initialize the workspace repository when configured.
8. **Deploy** — register NIP-05, store the soul snapshot, bind the runtime through `38384`/`38386`, create Bahia service/deployment records, and publish final `31951`.

A signed `31952` draft is authoritative: SoulFactory verifies its event reference and `spec_hash` and does not regenerate that approved snapshot through the LLM.

Runtime success is not inferred from timeout, EOSE, or relay closure. The terminal `38386` must be signed by the selected runtime and correlate to the `38384`, operator request, method, idempotency key, and spec hash.

## Recovery and reconciliation

The reactor keeps a live multi-relay subscription with reconnect/backoff behavior and uses EOSE only to mark completion of the stored-event phase.

At startup it requests the newest `5950`, newest `1950`, and up to 100 stored `38386` events. Existing terminal-result checks prevent repeated side effects. This backfill is intentionally bounded to the newest request and newest action globally; it is not a full historical queue scan.

If a runtime operation first produces a deploy-stage timeout/error but a valid success `38386` arrives later, the reactor validates the full correlation chain, restores the public-safe soul checkpoint embedded in `38384`, republishes active `31951`, and replaces the terminal provisioning result. It does not repeat Signet, avatar, memory, workspace, or runtime side effects.

## NIP-29 group assignment and NIP-42

Configure `soul_factory.nip29_groups`:

```yaml
soul_factory:
  nip29_groups:
    - relay: wss://groups.example.com
      id: operators
```

During the Signet stage, SoulFactory authenticates to each group relay with NIP-42 and publishes controller-signed kind-`9000` put-user events with `h=<group-id>` and `p=<agent-pubkey>`. Every configured group must accept the write; assignment fails closed otherwise. The relay adapter waits briefly for a late challenge, and group assignment retries a write once if it races the AUTH acknowledgement.

## Communikeys community write access

Configure `soul_factory.communikeys_communities` with the controller-owned community pubkey and the names of existing kind-`30000` section profile lists:

```yaml
soul_factory:
  communikeys_communities:
    - pubkey: <64-char-controller-community-pubkey>
      sections:
        - General
        - Apps
        - Chat
```

The community pubkey must be controlled by the configured Signet signer, and each section list must already be available through `soul_factory.relays` or `additional_relays`. After Signet provisions an agent identity, SoulFactory queries the exact admin-authored `(kind=30000, pubkey, d=section)` coordinate through EOSE, adds the agent `p` tag without dropping existing tags or content, signs the replacement with the controller, and requires a relay `OK`. Existing membership is idempotent. Missing or invalid admin lists, signer ownership mismatches, AUTH failures, and rejected writes fail provisioning closed. The assigned `30000:<community-pubkey>:<section>` coordinates are recorded in the Signet step as `communikeys_communities`.

Kind-`30000` `p` tags are the write ACL. Communikeys badges are engagement metadata only and are never used to grant publishing permission.

## Concord encrypted community onboarding

Concord is an optional encrypted backup plane. Configure one `soul_factory.concord_communities` entry per existing community, with its 64-character `community_id` and exactly one secret source for the CORD-05 `CommunityInvite` JSON:

```yaml
soul_factory:
  concord_communities:
    - community_id: <64-char-community-id>
      invite_bundle_env: FLEET_CONCORD_INVITE
    - community_id: <64-char-other-community-id>
      invite_bundle_file: /run/secrets/concord-other.json
```

`invite_bundle_env` names an environment variable; `invite_bundle_file` must be an absolute path to a mounted secret. Raw `community_root` and channel keys are not accepted inline in Bahia configuration. The referenced JSON follows [CORD-05](https://github.com/concord-protocol/concord/blob/main/05.md) and contains `community_id`, `owner`, `owner_salt`, `community_root`, `root_epoch`, `control_pk`, up to 256 `channels` (`id`, `key`, `epoch`, `name`), one to five `relays`, `name`, and optional `expires_at`, `creator_npub`, and `label` fields. Bahia bounds the NIP-44 plaintext at 65,535 bytes, verifies the self-certifying community ID, validates key/channel/relay fields, and refuses expired bundles.

After Signet mints the agent identity, Bahia builds a kind-`3313` rumor with empty tags and the bundle JSON as content, encrypts it to the new agent through the Signet-held staff key, signs the kind-`13` seal through Signet, and creates the kind-`1059` outer giftwrap with a single-use ephemeral key. The outer tags are exactly `p=<agent-pubkey>` and `k=3313`. Bahia requires every relay declared in the bundle to be present in `soul_factory.relays` or `additional_relays`, authenticates only that declared set, requires an accepted relay `OK` from every declared relay, fails the Signet provisioning step closed on any configuration, encryption, signing, AUTH, or publish error, and records successful community IDs as `concord_communities` without recording bundle contents.

Configure every relay declared in each bundle in `soul_factory.relays` or `additional_relays`, and configure the newly provisioned agent's Concord client to watch at least one of them. Direct Invites cannot be revoked after delivery; accidental disclosure requires the CORD-06 rekey/refounding process before the old holder loses access.

## Signet transport and secret handling

The controller identity is obtained from the configured Signet bunker and authorizes management requests.

Signet management JSON-RPC is carried privately as a NIP-17 kind-`14` rumor, placed in a seal signed by the Signet-controlled service identity, and NIP-59 gift-wrapped as kind `1059`. The response subscription is established before publish. Responses require decryption, valid seal verification by the bunker identity, and JSON-RPC request-ID correlation.

A returned agent `bunker_uri` may contain a one-time connection secret. It is retained only for private runtime handoff and removed from public `31951` tags, public `7950` content, and the relay-visible `38384` soul checkpoint. Public artifacts expose agent pubkey/npub and runtime references, never agent private keys or bunker secrets.

## Runtime integration

Runtime choices are capability-gated by compatible `30317` announcements. The packaged OpenClaw wrapper supports:

- `soulfactory.provision`
- `soulfactory.update`
- `soulfactory.persona.update`
- `soulfactory.revoke`

See [runtime control](soulfactory-runtime-control.md), [OpenClaw sidecar](openclaw-soulfactory-sidecar.md), [control wrapper](openclaw-soulfactory-control-wrapper.md), and [deployment runbook](soul-factory-sidecar-runbook.md).

## Web UI

- `/souls` loads `31951` souls and filters/searches the gallery.
- `/souls/new` saves signed `31952`, selects compatible `30317`, publishes `5950`, and tracks `6950`/`7950`.
- Drafts whose `agent_id` already has a provisioned `31951` are removed from the unresolved-drafts view.
- Capability choices must be compatible and advertise the requested method.
- Soul updates use signed `1950` actions with previous/new spec hashes and merge/replace parameters.

## Configuration

The current `soul_factory` block is defined in `internal/config/config.go`:

```yaml
soul_factory:
  enabled: true
  relays:
    - wss://relay.example.com
  additional_relays: []
  nip05_relays:
    - wss://relay.example.com
  nip29_groups: []
  communikeys_communities: []
  concord_communities: []
  authorized_pubkeys:
    - <64-char-operator-pubkey>
  soul_factory_pubkey: <64-char-controller-pubkey>
  signet_bunker_uri: bunker://<signet-pubkey>?relay=wss%3A%2F%2Fsignet-relay.example
  signet_client_secret_key: <controller-client-secret>
  startup_timeout: 30s
  llm_base_url: https://llm.example.com
  llm_model: <model-name>
  llm_api_key: <secret>
  llm_timeout: 2m
```

When enabled outside development mode, validation requires at least one relay, a Signet bunker URI, at least one authorized pubkey, positive timeouts, and a valid LLM origin/model/key. Workspace fields are optional but jointly constrained when `workspace_gitea_url` is set.

## Troubleshooting

### No provisioning result

- Confirm a relay accepted the signed request.
- Check the operator is in `authorized_pubkeys`.
- Inspect correlated `6950`, `7950`, `38384`, and `38386`; EOSE alone is not completion.
- Confirm selected `30317` advertises `soulfactory.provision`.

### Late runtime success

A later valid success `38386` can reconcile a deploy-stage runtime timeout. The original `5950`, `38384`, and `38386` must remain queryable and signatures must match.

### NIP-29 failure

Verify group relay/ID, controller authorization, NIP-42 support, and relay `OK`. Assignment fails closed.

### Soul absent from UI

Verify the factory-authored final `31951` exists on a browser-visible read-model relay. A draft or runtime result alone is not the final soul read model.
