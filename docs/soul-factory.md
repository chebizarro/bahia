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
    - community_id: <64-char-third-community-id>
      invite_bundle_sealed_file: /var/lib/bahia/concord-third.sealed
```

`invite_bundle_env` names an environment variable; `invite_bundle_file` and `invite_bundle_sealed_file` must be absolute paths. Raw `community_root` and channel keys are not accepted inline in Bahia configuration. The referenced JSON follows [CORD-05](https://github.com/concord-protocol/concord/blob/main/05.md) and contains `community_id`, `owner`, `owner_salt`, `community_root`, `root_epoch`, `control_pk`, up to 256 `channels` (`id`, `key`, `epoch`, `name`), one to five `relays`, `name`, and optional `expires_at`, `creator_npub`, and `label` fields. Bahia bounds the NIP-44 plaintext at 65,535 bytes, verifies the self-certifying community ID, validates key/channel/relay fields, and refuses expired bundles.

After Signet mints the agent identity, Bahia builds a kind-`3313` rumor with empty tags and the bundle JSON as content, encrypts it to the new agent through the Signet-held staff key, signs the kind-`13` seal through Signet, and creates the kind-`1059` outer giftwrap with a single-use ephemeral key. The outer tags are exactly `p=<agent-pubkey>` and `k=3313`. Bahia requires every relay declared in the bundle to be present in `soul_factory.relays` or `additional_relays`, authenticates only that declared set, requires an accepted relay `OK` from every declared relay, fails the Signet provisioning step closed on any configuration, encryption, signing, AUTH, or publish error, and records successful community IDs as `concord_communities` without recording bundle contents.

### Delivery targets

CORD-05 §6 delivers a Direct Invite to the recipient's giftwrap inbox: the relays in their kind-`10050` DM relay list (NIP-17) when one exists, their NIP-65 read relays otherwise. Bahia resolves that inbox on the SoulFactory relays for every recipient and publishes the wrap there **in addition to** the community relays declared in the bundle.

The two targets have different failure rules, because they have different owners. The community relays are operator-controlled and must all return an accepted `OK`, unchanged. An inbox list is recipient-controlled and may name a dead relay, so one acceptance is enough — but zero is a delivery failure, since publishing only where the recipient does not read is a silent drop. Only the recipient's own signed lists are honored, the list is capped at five `ws`/`wss` URLs with no embedded credentials, and a relay Bahia already holds is reused rather than re-dialed.

A freshly provisioned fleet agent has published neither list at provisioning time and is configured to read the fleet relays, so its invite rides the community relays alone. The inbox path matters for the recipients that are not that case: an npub the fleet did not just provision, an agent that later moved to a different inbox relay set, and every survivor re-invited by a CORD-06 rotation.

Configure every relay declared in each bundle in `soul_factory.relays` or `additional_relays`, and configure the newly provisioned agent's Concord client to watch at least one of them. Direct Invites cannot be revoked after delivery; accidental disclosure requires the CORD-06 rekey/refounding process below before the old holder loses access.

### Signet-backed custody

`invite_bundle_sealed_file` is the custody form. The file holds only a NIP-44 payload sealed to the Signet-held staff key, so a leaked backup, container image, or config dump yields ciphertext instead of a community's access keys. Bahia opens it lazily through the bunker for each provisioning or rotation and keeps no plaintext copy, so material rotated by another process is picked up without a restart. The sealed plaintext is a custody document:

```json
{ "version": 1, "invite_bundle": { "community_id": "…" }, "control_root": "<64-char-hex>" }
```

`control_root` is the CORD-02 §2 staff write secret. It is minted by a Refounding, is never placed in an invite, and never leaves custody. A file sealed with a bare `CommunityInvite` is also accepted, and the first Refounding upgrades it to the document form. Writes are verified before they land: Bahia re-opens the fresh payload through Signet and requires it to match before atomically replacing the file at mode `0600`, so custody is never overwritten with material the bunker cannot reopen. `invite_bundle_env` and `invite_bundle_file` remain supported but are read-only, and rotation refuses to run against them.

### CORD-06 rekeys and refoundings

`FullProvisioner.RotateConcordCommunity` performs an explicit [CORD-06](https://github.com/concord-protocol/concord/blob/main/06.md) rotation after a membership change or an accidental disclosure. A rotation names its scope and its **surviving** members; the caller supplies that membership, and anyone omitted is severed from the rotated scope.

- A **Rekey** (`ChannelIDs`) mints a fresh key for each named Private Channel and bumps only that channel's epoch. A Public Channel derives from the `community_root` (CORD-03), so requesting one alone is refused.
- A **Refounding** (`Refound: true`) rolls the `community_root`, bumps `root_epoch`, mints a fresh `control_root` beside it, and republishes `control_pk` as `group_key("concord/control-signer", control_root, community_id, new_epoch).pk` — the CORD-06 §3 split upgrade. Public Channels follow the base for free; Private Channels rotate only when named.

The derivations are the frozen CORD-02 Appendix A shapes (HKDF-SHA256 with `info = utf8(label) || 0x00 || id[32] || epoch_be[8]`, `scalar_normalize`, and the A.5 epoch-key commitment). Bahia builds the rotated bundle in full before writing anything, round-trips unknown bundle and channel fields verbatim, revalidates its own minted bundle against the relay bus, seals it into custody, and only then redistributes it to the survivors as CORD-05 §6 Direct Invites. A rotation is resumable rather than atomic: if redistribution fails partway, custody already holds the new material and the receipt is returned alongside the error so delivery can be re-run idempotently.

The returned `ConcordRotationReceipt` is safe to log or persist. It records community, epochs, per-channel `prevcommit` continuity commitments, recipients, and the rekey addresses and chunk counts published — never a `community_root`, `control_root`, or channel key. A rotation reaches only the supplied `Recipients`: anyone omitted is severed from the rotated scope, which is the point, but a member Bahia does not know about is severed by accident.

#### Rekey Blobs (kind 3303)

Every rotation also publishes CORD-06 §1 Rekey Blobs, the lane that lets a member outside the fleet-provisioned set converge on the new epoch without being individually re-invited. `Recipients` receive a blob; `DirectInvites` narrows the CORD-05 §6 handoff to the survivors Bahia can giftwrap individually, and defaults to all of them. `Staff` names the survivors holding a Control-writing permission (CORD-04 §3).

Blobs ride the rekey address derived from the **prior** `community_root` — `concord/base-rekey-pseudonym` over the `community_id` for a Refounding, `concord/rekey-pseudonym` over the `channel_id` for a Channel — at the new epoch, which is exactly the address a holder of the prior key precomputes and subscribes to. Each event is a CORD-01 reversed Stream wrap: a kind-`1059` authored by the rekey address with an ephemeral `p` tag, around an encrypted kind-`20013` seal signed by the Signet-held Rotator, around the kind-`3303` rumor. Every chunk carries `scope`, `newepoch`, `prevepoch`, `prevcommit`, and `chunk i n`.

A blob's plaintext is fixed-width and the width declares the form: 72 bytes for a Channel (`scope_id ‖ epoch_be ‖ new_key`), 104 for a member's base rotation (`… ‖ new_control_pk`), 136 for a staff recipient (`… ‖ new_control_root`). The 72-byte *base* form is the legacy pre-split shape and is never minted. Its recipient finds it by the `concord/recipient-pseudonym` locator over `rotator_xonly || recipient_xonly`, both public, so a bunker-held account locates its blob without touching a secret.

Chunking binds on two limits. CORD-06 caps an event at 120 blobs; the CORD-01 double wrap re-encrypts and base64s the payload twice, so 120 of the widest form would grow the wrap plaintext past NIP-44's 65535-byte ceiling. An event carries *up to* 120 blobs, so Bahia chunks on whichever limit binds first and a rotation to many staff simply spans more events.

Blob plaintexts are binary and NIP-46 carries params as JSON strings, so the pairwise encryption uses Signet's binary-safe `nip44_encrypt_b64` (base64 in, NIP-44 payload out). A bunker without that method fails the rotation rather than encrypting U+FFFD-substituted material, so an out-of-date Signet is a loud error rather than blobs no client can use.

## Signet transport and secret handling

The controller identity is obtained from the configured Signet bunker and authorizes management requests.

Signet management JSON-RPC is carried privately as a NIP-17 kind-`14` rumor, placed in a seal signed by the Signet-controlled service identity, and NIP-59 gift-wrapped as kind `1059`. The response subscription is established before publish. Responses require decryption, valid seal verification by the bunker identity, and JSON-RPC request-ID correlation.

A returned agent `bunker_uri` may contain a one-time connection secret. It is retained only for private runtime handoff and removed from public `31951` tags, public `7950` content, and the relay-visible `38384` soul checkpoint. Public artifacts expose agent pubkey/npub and runtime references, never agent private keys or bunker secrets.

## Runtime integration

Runtime choices are capability-gated by compatible `30317` announcements intersected with the administratively enabled runtime set (`soul_factory.agent_runtimes`). Bahia instantiates the generic runtime-control adapter for every enabled target; runtime targets are extensible protocol identifiers, so registering an additional conforming runtime is configuration, not new code. The packaged OpenClaw wrapper supports:

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
  agent_runtimes:
    - openclaw
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

### Agent runtime enablement

`agent_runtimes` is the administrative enablement list for SoulFactory agent runtimes.

- Unset: defaults to `[openclaw]`, preserving prior single-runtime behavior.
- Entries are normalized to lowercase; empty, malformed (`^[a-z0-9][a-z0-9._-]{0,63}$`), and duplicate targets fail config validation. Startup fails fast on any invalid entry — enabled targets are never silently omitted.
- Bahia builds one generic runtime-control adapter per enabled target and installs the same registry in the provisioning engine and the lifecycle/customization handler, so dispatch cannot diverge between paths.
- Enablement is necessary but not sufficient: dispatch additionally requires a live, trusted, schema-compatible `30317` capability advertising the requested method. Capability advertisement is not authorization — runtime bridges independently validate the controller pubkey, and Bahia fails closed before side effects when no compatible capability exists.
- The web UI intersects live compatible capabilities with this list (via `GET /api/v1/soulfactory/runtimes`) and gates lifecycle controls by advertised methods. When the policy endpoint is unreachable the UI fails closed and exposes no targets.

Rollout of an additional runtime (for example `metiq`): deploy the runtime bridge, publish its `30317`, add the target to `agent_runtimes`, restart Bahia. Rollback is removing the target and restarting; the absent-entry default keeps `openclaw` available at all times.

### Missing or stale capability diagnostics

- Target enabled but not selectable in UI: no compatible `30317` seen, or it was filtered by server policy. Inspect `30317` events tagged with the runtime for schema, `control-schema`, methods, and `controller` tags.
- Capability freshness: dispatch requires a `30317` no older than 10 minutes (`DefaultMaxCapabilityAge`). A runtime that stops republishing its capability becomes ineligible; Bahia rejects dispatch with a stale-capability error instead of publishing `38384`. Runtimes must republish their replaceable capability well inside that window.
- `no compatible <target> runtime capability found for method <m>`: the live capability does not advertise `m`, advertises a different controller set, or is stale/incompatible.
- `no runtime adapter configured for <target>`: the draft/request targets a runtime absent from `agent_runtimes`; enable it or correct the draft.
- Exact replay returns the cached `38386`; reused keys with conflicting input return `duplicate_conflict` without side effects.

## Troubleshooting

### Signet unavailable at startup

Bahia starts its HTTP listener and non-signing relay consumers without waiting for the Soul Factory bunker. Inspect `GET /ready` for the `signet-soulfactory` check and its `state`, `last_error`, `last_attempt`, and `last_success` details. Disconnection degrades readiness rather than liveness. Fix the bunker or NIP-46 relay and allow the built-in reconnect loop to recover; no Bahia restart is required. The reactor begins or restarts its historical subscription only after signing is available, then replays the queued request backlog while terminal-result deduplication prevents repeated provisioning.

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
