# Bahia User-Guide Documentation Drift Review

**Date:** 2026-07-17
**Scope:** `docs/user-guide/**/*` and root `README.md`, checked against the current codebase (`web/src/routes/**`, `cmd/cli/**`, `internal/mcp/**`, `internal/config/**`, `internal/adapters/nostr/projector.go`, `internal/kinds/**`).
**Method:** Every documented route, CLI command, MCP tool, discovery kind, and feature-availability claim was traced to an authoritative implementation surface (route file, Cobra registration, MCP tool registration, config default, or capability gate). Findings below cite that evidence. Only repository evidence is used; where the code contradicts the docs, the code is treated as authoritative.

**Availability vocabulary used below:** *available* · *requires authentication* · *requires capability X* · *feature-gated* · *disabled by default*.

---

## Executive summary

The user guide has drifted substantially from the implementation in three high-impact areas:

1. **CLI reference is largely fabricated.** `docs/user-guide/cli-reference.md` (and CLI blocks in several feature pages) document entire top-level commands — `bahia llm`, `bahia payments`, `bahia artifacts`, `bahia builds`, `bahia notifications` — that are **not registered** in the CLI. Many documented subcommands and flags on real commands also do not exist. The authoritative command tree is registered at `cmd/cli/main.go:66-80`.
2. **MCP tool names are wrong throughout.** `docs/user-guide/mcp-tools.md` lists many tool names that do not match the registry, and documents two whole categories — **Soul Factory tools (`soul_factory_*`)** and **Organization tools (`bahia_org_*`)** — that **do not exist** as MCP tools at all. Verified: `grep -r 'soul_factory\|bahia_org_' internal/mcp` returns zero matches.
3. **Feature-availability is overstated.** LLM routes and Soul Factory are **disabled by default**; Cashu payments are **disabled by default and actively rejected** by config validation. The guide presents these as generally available.

Plus several stale constants and internal inconsistencies (README's discovery `kind 31974`; worker capability kind `31989` vs `10100`).

Severity legend: **P0** user will hit a hard failure following the docs · **P1** materially misleading · **P2** minor/cosmetic.

---

## 1. Stale or incorrect claims

### 1.1 CLI reference documents non-existent top-level commands — **P0**

The root command tree registers exactly these commands (`cmd/cli/main.go:66-80`):

```
auth · services · environments · state · deployments · adopt · workers
· logs · policies · secrets · orgs · package · souls
```

There is **no** `llm`, `payments`, `artifacts`, `builds`, or `notifications` top-level command. Yet the docs contain full CLI sections for all of them:

| Documented command | File(s) | Reality |
|---|---|---|
| `bahia llm deploy / approve / rollback / state / routes / releases` | `cli-reference.md` (LLM Routes), `features/llm-routes.md` | No `llm` command registered. LLM operations are signer-first Nostr / MCP only. |
| `bahia payments estimate / cost / history` | `cli-reference.md` (Payments), `features/payments.md`, `features/workers.md` | No `payments` command registered. |
| `bahia artifacts list / get / sbom / signatures / verify` | `cli-reference.md` (Artifacts) | No `artifacts` command registered. |
| `bahia builds list / get / register` | `cli-reference.md` (Builds) | No `builds` command registered. |
| `bahia notifications channels … / log` | `cli-reference.md` (Notifications) | No `notifications` command registered. |

### 1.2 CLI subcommands/flags that do not exist on real commands — **P0**

Verified against `cmd/cli/main.go` and `cmd/cli/{package,soulfactory}.go`:

| Documented | Reality (evidence) |
|---|---|
| `bahia auth login --nip07` / `--nip46`, `bahia auth status` | `auth` has only `inspect` (`main.go:91-119`). No `login`/`status`. |
| `bahia services create --name --repository` | Flags are `--name`, `--artifact-repo`, `--runtime-type` — **no `--repository`** (`main.go:154-158`). Command is also **deprecated / non-functional**: it returns `signerFirstMutationUnavailable("service/create")` (`main.go:150-153`). |
| `bahia services update`, `bahia services delete` | `services` has only `list`, `get`, `create`, `actions` (`main.go:169`). Direct runtime lifecycle is `bahia services actions deploy/restart/stop`. |
| `bahia environments create --name --slug`, `... --requires-approval` | Flags are `--name`, `--strategy`, `--protected` — **no `--slug`/`--requires-approval`** (`main.go:295-298`). Also deprecated (signer-first only). |
| `bahia environments update`, `bahia environments delete` | `environments` has only `list`, `get`, `create` (`main.go:300`). |
| `bahia deployments list / get / runs / logs` | `deployments` has only `deploy` and `rollback` (`main.go:409-417`). Run logs are the separate top-level `bahia logs run [run-id] --tail/--stream` (`main.go:704-773`). |
| `bahia workers get npub…` | Command is `bahia workers show [pubkey]` (`main.go:664-700`). No `get`. |
| `bahia workers pricing npub…` | `workers` has only `list`, `show`. No `pricing`. |
| `bahia orgs create --name --display-name` | `create` takes a **positional** `[name]`; only `--display-name` is a flag (`main.go:968-985`). |
| `bahia orgs invite …`, `bahia orgs invites accept/list` | No `invite`/`invites` subcommands exist. Membership is `bahia orgs members add/remove [org-id] [pubkey]` (`main.go:1009-1037`). |
| `bahia orgs members update … --role` | Only `list`, `add`, `remove` exist. No `members update`. |
| `bahia orgs members remove … --pubkey` | `pubkey` is **positional**, not a flag: `orgs members remove [org-id] [pubkey]`. |
| `bahia orgs delete acme-corp` | No `orgs delete` command. |
| `bahia services create --org-id`, `bahia environments create --org-id` | No `--org-id` flag on either create command. |

> **Real command tree** (authoritative, from the CLI-audit trace of `cmd/cli/main.go`) — recommend replacing the invented sections with this and only documenting flags that appear in the registration:
> `services {list, get [id], create, actions {deploy, restart, stop}}` · `environments {list, get [id], create}` · `state {list, drifted}` · `deployments {deploy, rollback}` · `logs {run [run-id], live [service-id] [env-id]}` · `workers {list, show [pubkey]}` · `orgs {list, get, create [name], members {list, add, remove}}` · `policies {list, get, create}` · `secrets {list, set, delete}` · `adopt {scan, import}` · `package {repo {apply, delete}, upload, promote, yank, drift}` · `souls {list, get, provision, suspend, resume, revoke, redeploy, regenerate, templates {list, get}}` · `auth {inspect}`.

### 1.3 MCP tool names do not match the registry — **P0**

`docs/user-guide/mcp-tools.md` documents many names that are not registered. Authoritative names come from `internal/mcp/server.go` and the per-domain `*_tools.go` files. Corrections:

| Documented (wrong) | Actual registered name | Source |
|---|---|---|
| `bahia_service_create` / `bahia_service_update` / `bahia_service_delete` | `bahia_create_service` / `bahia_update_service` / `bahia_delete_service` | `server.go:271,299,611` |
| `bahia_environment_create` | `bahia_create_environment` | `server.go:360` |
| `bahia_llm_route_create` | `bahia_llm_create_route` | `server.go:516` |
| `bahia_llm_release_register` | `bahia_llm_register_release` | `server.go:548` |
| `bahia_llm_approve` | `bahia_llm_approve_deployment` | `server.go:596` |
| `bahia_llm_get_route`, `bahia_llm_list_state` | *not registered* (use `bahia_llm_list_routes`, `bahia_llm_list_releases`) | `server.go:570-606` |
| `bahia_ml_model_import` / `bahia_ml_recipe_run` / `bahia_ml_inference_deploy` / `bahia_ml_inference_rollback` | `bahia_ml_import_model` / `bahia_ml_run_recipe` / `bahia_ml_deploy` / `bahia_ml_rollback` | `ml_tools.go:20-52` |
| `bahia_ml_list_models` / `bahia_ml_list_endpoints` | `bahia_ml_list_state` / `bahia_ml_get_state` / `bahia_ml_get_provenance` | `ml_tools.go:59-64` |
| `bahia_worker_resume` (method `worker/undrain`) | `bahia_worker_undrain` | `worker_tools.go:45` |
| `bahia_backup_definition_apply` / `bahia_backup_policy_apply` / `bahia_backup_run` / `bahia_backup_verify` / `bahia_backup_restore` | `apply_backup_definition` / `apply_backup_policy` / `request_backup_run` / `request_backup_verification` / `request_backup_restore` (each also aliased with a `bahia_` prefix) | `backup_tools.go:158-205` |
| `bahia_dns_list_zones` | *not registered* (only `bahia_dns_list_endpoints`, `bahia_dns_list_drift`) | `dns_tools.go:42-46` |
| `bahia_dns_zone_create` / `bahia_dns_policy_apply` | `bahia_assistant_dns_zone_create` / `bahia_assistant_dns_policy_apply` | `dns_tools.go:58-61` |
| `bahia_package_publish` | `bahia_package_upload` | `package_tools.go:18` |
| `bahia_payment_history` | `bahia_get_payment_history` | `server.go:1443` |

### 1.4 Two entire MCP tool categories are fictional — **P0**

- **Soul Factory tools** — `mcp-tools.md` lists `soul_factory_list_souls`, `soul_factory_get_soul`, `soul_factory_provision`, `soul_factory_action`, `soul_factory_regenerate`; `features/souls.md` uses these in five MCP examples. **None of these MCP tools exist** (`grep -r soul_factory internal/mcp` → 0 matches). Soul Factory is signer-first Nostr/CLI only — `features/souls.md` itself states "REST provisioning and lifecycle routes are intentionally not part of SoulFactory," which is inconsistent with the MCP examples it also shows.
- **Organization tools** — `mcp-tools.md` lists `bahia_org_list`, `bahia_org_create`, `bahia_org_detail`, `bahia_org_invite`; `features/organizations.md` shows a `bahia_org_create` MCP example. **No `bahia_org_*` MCP tools exist** (`grep -r bahia_org_ internal/mcp` → 0 matches). Org operations are the encrypted request/result facade (`5980`/`7980`), which `organizations.md` also correctly describes lower down — the MCP-tool example contradicts it.

### 1.5 README discovery kind `31974` is stale — **P1**

`README.md` repeatedly cites "`Nostr discovery events (kind 31974 + NIP-51 kind 30002)`". Current canonical discovery is **ContextVM `11316`–`11320` + NIP-51 `30002`**:
- `internal/kinds/kinds.go:211-215,222-226` (ContextVM announcements + `RelaySetDiscovery = 30002`).
- The system-discovery payload is published as `kinds.ContextVMServerAnnouncement` (11316) — `internal/adapters/nostr/projector.go:2247-2249`.
- `SystemDiscovery = 31974` still exists (`kinds.go:355-360`) but is a **legacy** kind mapped to canonical ContextVM discovery by the migration manifest (`internal/nostrmigration/manifest.go:352`), and `31974` additionally collides with `LegacyWorkerState` (`kinds.go:407-410`).

Note the internal inconsistency: `mcp-tools.md`, `cli-reference.md`, and `index.md` already use the correct `11316`-`11320` range, while `README.md` alone uses `31974`.

### 1.6 `index.md` architecture diagram uses a stale kind range — **P2**

The ASCII "Architecture Overview" in `index.md` says `ContextVM discovery + NIP-51 11316-11320 + relay sets`, conflating the ContextVM announcement range with NIP-51. NIP-51 relay-set discovery is kind `30002`; the `11316`-`11320` range is the ContextVM announcement family (`kinds.go:211-215,222`). Align the label with the README-style wording used elsewhere.

### 1.7 Worker capability kind inconsistency — **P2 (verify)**

`features/workers.md` documents worker capability announcements as **kind `31989`** (`worker:<pubkey>:ai-capability`), while `features/fleet-health.md` states Fleet Health consumes **kind `10100`** Loom worker advertisements plus `30900` worker state. These two pages disagree on the worker-telemetry kind. Reconcile both against the Loom worker advertisement kind actually consumed by `web/src/routes/fleet-health/` and `workers/`.

### 1.8 `getting-started.md` example inconsistencies — **P1**

- Step 2 MCP example uses `bahia_service_create` → should be `bahia_create_service` (§1.3).
- Step 2 CLI example uses `bahia services create --name --repository` → wrong flag and deprecated command (§1.2).

---

## 2. Missing coverage

### 2.1 Routes with no user-guide page — **P1**

These routes exist under `web/src/routes/` but have no documentation (no dedicated page and no mention in `index.md`):

| Route | Path | Suggested action |
|---|---|---|
| Continuity | `web/src/routes/continuity/` (`+page.svelte`, `SimulationPanel.svelte`, `TopologyView.svelte`) | Add a short feature page or a Fleet Health subsection; it is user-facing (topology + simulation). |
| Events | `web/src/routes/events/` | Add a brief page or fold into Nostr Integration/Troubleshooting. |
| Environment States | `web/src/routes/environment-states/` | Cross-link from `features/environments.md`. |

The `docs` viewer route (`web/src/routes/docs/`) and `settings` routes (`settings`, `settings/profile`, `settings/relays`) are covered indirectly in `index.md`/`getting-started.md` — acceptable, but `settings` has no dedicated section.

### 2.2 Feature-gating notes absent at point of use — **P1**

The guide never tells the reader that key features are off by default (see §4). Each affected page (`llm-routes.md`, `payments.md`, `dns.md`, and the `index.md` feature table) should carry a one-line availability note rather than only implying general availability.

---

## 3. Recommended minimal edits by file

Surgical edits only — correct names/paths/commands, add one-line gate notes, delete unsupported examples. Do not restructure.

**`README.md`**
- Replace all `kind 31974` discovery references with `ContextVM kinds 11316-11320 + NIP-51 kind 30002` (Current Status bullet, Architecture diagram label, Control planes section, Key Nostr event contracts table). (§1.5)
- CLI block: `bahia deploy …` / `bahia rollback …` are already shown as top-level — update to `bahia deployments deploy …` and `bahia deployments rollback …`, and note `--requested-by` is optional. (§1.2)

**`docs/user-guide/cli-reference.md`**
- Remove or rewrite the **LLM Routes, Payments, Artifacts, Builds, Notifications** CLI sections — those commands do not exist. Point readers to the MCP / signer-first Nostr surfaces instead. (§1.1)
- Fix `auth` (only `inspect`), `services` (no `update`/`delete`, `create` deprecated, `--artifact-repo` not `--repository`), `environments` (no `--slug`/`update`/`delete`), `deployments` (only `deploy`/`rollback`; logs are `bahia logs run …`), `workers` (`show` not `get`, no `pricing`). (§1.2)
- Global Flags table: replace `--api-url` with `--server`; the real global flags are `--server`, `-o/--output`, `--nostr-key-file`, `--relay`, `--bootstrap-relay`, `--service-pubkey`, `--trusted-service-pubkey`, `--http-fallback` (`main.go:56-63`). `-v/--verbose` is not a registered global flag — verify before keeping.

**`docs/user-guide/mcp-tools.md`**
- Apply every rename in §1.3.
- Delete the **Soul Factory Tools** and **Organization Tools** tables (no such MCP tools); replace with a one-line pointer to the signer-first Nostr flow (souls) and the encrypted `5980`/`7980` facade (orgs). (§1.4)
- Backup, DNS, and Package tool tables: use the registered names (and note the `bahia_`-prefixed aliases for backup).

**`docs/user-guide/getting-started.md`**
- Step 2 MCP example → `bahia_create_service`; CLI example → `bahia services create --name --artifact-repo` (and note it is signer-first/deprecated). (§1.8)

**`docs/user-guide/features/llm-routes.md`**
- Remove the `bahia llm …` CLI blocks (no `llm` command); keep the ContextVM/MCP paths. (§1.1)
- Fix MCP names: `bahia_llm_create_route`, `bahia_llm_register_release`, `bahia_llm_approve_deployment`. (§1.3)
- Add availability note: **feature-gated — disabled by default (`BAHIA_LLM_ENABLED=false`)**. (§4)

**`docs/user-guide/features/payments.md`**
- Remove the `bahia payments …` CLI blocks (no `payments` command). (§1.1)
- Fix MCP name `bahia_payment_history` → `bahia_get_payment_history`. (§1.3)
- Qualify "Payments via Lightning Network or Cashu": Cashu is **disabled by default and currently rejected** (`config.go:1101-1103`); mint-backed token flows are not implemented. Payment *history* is available but **requires the encrypted-ContextVM capability** (browser relays + configured Nostr private key — `projector.go:2200-2206`). (§4)

**`docs/user-guide/features/souls.md`**
- Replace the five `soul_factory_*` MCP examples with the signer-first Nostr flow the page already documents (or a CLI equivalent). (§1.4) The "disabled by default" configuration note here is **accurate** — keep it.

**`docs/user-guide/features/organizations.md`**
- Remove the `bahia_org_create` MCP example (no such tool). (§1.4)
- Fix CLI: `bahia orgs create [name] --display-name` (positional name); replace `orgs invite`/`orgs invites …`/`orgs members update`/`orgs delete` with the real `orgs members {add,remove} [org-id] [pubkey]`; drop `--org-id` from the services/environments create examples. (§1.2)

**`docs/user-guide/features/workers.md`**
- Fix CLI: `bahia workers show` (not `get`); remove `workers pricing` and the `bahia payments estimate` block. (§1.2)
- Fix MCP `bahia_worker_resume` → `bahia_worker_undrain`. (§1.3)
- Reconcile the `31989` capability kind with `fleet-health.md`'s `10100`. (§1.7)

**`docs/user-guide/features/deployments.md`**
- Fix the "Monitoring / Run Logs / History" CLI: `bahia deployments list/get/logs` do not exist. Use `bahia state list`, and run logs via `bahia logs run [run-id] --tail`. (§1.2) The MCP (`bahia_deploy`, `bahia_rollback`, `bahia_get_run_logs`) and ContextVM examples are correct — keep.

**`docs/user-guide/index.md`**
- Fix the architecture-diagram discovery label (§1.6).
- Add availability qualifiers to the feature table for LLM Routes, DNS, and Souls (see §4).

---

## 4. Claims to remove or qualify due to gating / prerequisites

Every claim below should be marked with a consistent availability note at each affected workflow, not only in a general page.

| Feature | Current default | Evidence | Doc action |
|---|---|---|---|
| **LLM routes / control plane** | **disabled by default** (`BAHIA_LLM_ENABLED=false`); capability `llm_control_plane` and `llm/*` methods are advertised only when enabled | `config.go:776-779`; `projector.go:2241,2365-2370` | `index.md`, `llm-routes.md`, `README` "Web UI for … LLM routes": qualify as feature-gated. |
| **LLM operational REST** | **disabled by default** (`BAHIA_LLM_ALLOW_OPERATIONAL_REST=false`), and additionally requires auth enabled, a non-empty operator allowlist, and `llm.enabled=true` | `config.go:1397-1407` | Where docs mention REST `POST /api/v1/llm/routes` (`llm-routes.md`), state these prerequisites. |
| **Soul Factory / Souls** | **disabled by default** (`BAHIA_SOUL_FACTORY_ENABLED=false`); reactor returns nil when off | `config.go:828-830`; `app/soulfactory.go:46-49` | `souls.md` already notes this (keep). Add the same qualifier to `index.md` feature table and README (README's "experimental / evolving" is weaker than "disabled by default"). |
| **Cashu payments** | **disabled by default and rejected if enabled** — `cashu.enabled=true` fails config validation ("mint-backed token flows are not implemented") | `config.go:880-882,1101-1103`; `app/app.go:830-836` | `payments.md`: remove/qualify "Payments via Lightning Network or Cashu"; state Cashu is unsupported. |
| **Payment history (encrypted)** | Service is always constructed, but the browser/encrypted path requires `encrypted_nostr_requests` capability = browser relays present **and** Nostr private key configured | `projector.go:2200-2206,2240` | `payments.md`, `mcp-tools.md`: note the encrypted-transport prerequisite rather than implying unconditional availability. |
| **DNS** | `dns.enabled=false` by default (projection sub-flags default true but are subordinate) | `config.go:807-816` | `index.md`/`dns.md`: qualify DNS features as feature-gated (verify `dns.md`, not read in this pass). |
| **ML models** | **available / unconditional** — no `ml.enabled` flag; ML registry constructed always and AI/ML advertised `enabled: true` | `app/app.go:538-542`; `projector.go:2377-2378,2399-2410` | No gating note needed; only fix the MCP tool names (§1.3). |
| **Security data** | `security.md` claim "There are no REST endpoints for security data — all communication is end-to-end encrypted" is consistent with `mcp-tools.md` (ContextVM `security/*`, no `bahia_security_*` MCP tools). Treat as **accurate**; no change beyond confirming no REST security route exists. | `mcp-tools.md` Security section | Keep; optionally add "requires a NIP-44-capable signer" prerequisite for parity. |

---

## 5. Notes on claims that are accurate (no change needed)

- Discovery kinds in `cli-reference.md` / `mcp-tools.md` (`11316`-`11320`, `30002`) are correct.
- `bahia_docs_list` / `bahia_docs_read` MCP tools and the `bahia://docs/<topic>` resource pattern exist as documented (`internal/mcp/docs_resources.go:22-31,109,123`).
- The MCP endpoints `/mcp` and `/api/v1/mcp` are correct (`internal/api/router/router.go:188-193`).
- `deployments.md` ContextVM/MCP examples (`bahia_deploy`, `bahia_rollback`, `bahia_get_run_logs`, `service/deploy`, `service/rollback`) are correct.
- `souls.md` "Soul Factory reactor is disabled by default" is correct.
- `orgs`/`organizations` naming: the code exposes a literal `orgs` CLI noun and `/orgs` route; prose using "Organizations" is fine, but CLI/route literals must stay `orgs` (do not globally rename to `organizations`).

---

## Appendix — authoritative evidence index

- CLI root registration: `cmd/cli/main.go:66-80`; per-command: `services` 125-247, `environments` 253-300, `state` 306-341, `deployments` 347-417, `workers` 664-700, `logs` 704-773, `orgs` 939-1039; `package` `cmd/cli/package.go:143-403`; `souls` `cmd/cli/soulfactory.go:63-386`.
- MCP tools: `internal/mcp/server.go` (core, line refs in §1.3), `ml_tools.go`, `agent_async_tools.go`, `dns_tools.go`, `fips_tools.go`, `worker_tools.go`, `package_tools.go`, `backup_tools.go:158-205`, `docs_resources.go`. Registry composition: `server.go:1757-1765`.
- Discovery kinds: `internal/kinds/kinds.go:211-226,355-360,407-410`; `internal/adapters/nostr/projector.go:2247-2249,2305-2306`; `internal/nostrmigration/manifest.go:352`; `web/src/lib/nostr/kinds.gen.js:132-140`.
- Feature gates/defaults: `internal/config/config.go:776-779,807-816,828-830,880-882,1101-1103,1397-1407`; `internal/app/app.go:538-550,830-836`; `internal/app/soulfactory.go:46-49`; `internal/adapters/nostr/projector.go:2200-2241,2365-2410`.
- Routes: `web/src/routes/**` (undocumented: `continuity/`, `events/`, `environment-states/`).
