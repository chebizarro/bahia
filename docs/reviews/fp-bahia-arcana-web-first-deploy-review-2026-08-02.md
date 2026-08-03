# Independent review — `fp-bahia-arcana-web-first-deploy`

**Date:** 2026-08-02
**Reviewer:** independent agent review (read-only)
**Epic:** `fp-bahia-arcana-web-first-deploy` (Bahia browser-only first deployment of Arcana)
**Commits reviewed:** `35211fe5`, `fcb37e20`, `b7b1df66`, `e2446ce6`, `8f19214a`, `2ce6bf5d`, `4987ce93` (all on `origin/master`)
**Normative contract:** `docs/designs/fp-bahia-arcana-01-contract.md`

**Verdict: NOT READY to close the epic. Conditionally ready for the `fp-bahia-arcana-08-e2e` happy-path run.**
Two confirmed defects sit on the failure paths that item 08 is supposed to exercise. See [Verdict](#verdict-and-readiness-for-item-08).

---

## Context and scope

The epic's acceptance criteria, restated from the contract and the fleet item:

1. Browser-only end to end — no shell, DB, manual ContextVM console, or Compose file edits.
2. Secrets always file-backed/encrypted; never in Nostr events, browser logs, task notes, or argv.
3. Signer-first everywhere.
4. The signed desired-state hash shown to the operator is the hash that was signed.
5. Immutable digest-pinned artifacts only.
6. Route changes live inside the signed plan, **with rollback ordering**.
7. Rejected deploys make no runtime change.
8. A failed healthcheck offers rollback.

Method: read the contract and the five subtask `acceptance_criteria.json` sets; read the signed-plan core (`runtime_desired_state.go`, `runtime_desired_state_builder.go`, `runtime_lifecycle.go`, `encrypted_service_handlers.go`, `public_route.go`, `cloudflare.go`, `coordinator.go`, `registry.go`) directly; ran six parallel read-only probes across secret-leak, signer-first, hash-integrity, digest-pinning, approval/rollback, and routing dimensions; independently spot-verified every load-bearing claim before recording it. All Go tests for the affected packages pass:

```
ok  internal/domain      ok  internal/service     ok  internal/controlplane
ok  internal/workflow    ok  internal/adapters/routing    ok  internal/pipeline
```

---

## 1. Acceptance criteria verified against the code

### Item 02 — deployment-unit targeting UI (`fcb37e20`)
No `acceptance_criteria.json` exists for this item (only `pstf/features/fp-bahia-arcana-02-target-ui/verification_report.md`), unlike items 03–07. **Minor process gap** — not a code defect.

Code checks out: `web/src/lib/deployment-units.js` plus `DeploymentUnitsSection.svelte` drive full-set unit replacement through signed `environment/update`, matching the contract's §4 "complete-set replacement, not an incremental patch" with `expected_updated_at`. E2E coverage in `deployment-unit-targeting.spec.js`.

### Item 03 — private-repo build UI (`b7b1df66`) — **PASS**
- **AC1 ✅** `internal/controlplane/encrypted_build_handlers.go:276-285` allowlists exactly the nine `VITE_*` names; only `repository_credential_ref` (an opaque ref) crosses the wire — never a credential value.
- **AC2 ✅** Ownership validated, queued state registered after initiator acceptance.
- **AC3 ✅** `web/src/routes/builds/+page.svelte` renders canonical status from relay projections, no polling.
- **AC4 ✅** `immutableArtifactDigest()` (`web/src/lib/deployment-desired-state.js:47-54`) gates candidates on `^sha256:[0-9a-f]{64}$`.
- **AC5 ✅** Fails closed pending `bahia-1tgwr`; no direct private-GitHub fetcher exists. Correct posture.

### Item 04 — immutable artifact flow (`e2446ce6`) — **PASS with a scoped caveat**
- **AC2/AC3 ✅** `internal/service/registry.go:819-831` enforces `^sha256:[0-9a-f]{64}$`; `:715-734` and `:754-765` require manifest digest, tag-resolved digest, and artifact digest to agree. Mismatched or unverifiable references register no artifact.
- **AC5 ✅** Manual registration is config-gated off by default and still subject to registry verification.
- **Caveat (out of the Arcana path):** `imageRefForArtifact` (`internal/service/runtime_lifecycle.go:1254-1262`) falls back to `repo:tag` when `ImageDigest` is empty. This is unreachable for Arcana because the managed path hard-requires a digest (`runtime_desired_state_builder.go:81-84`), but it is reachable for adopted/legacy services. Combined with HiveCI ingestion not validating digest format (`internal/adapters/hiveci/subscriber.go:367-398`), a malformed-digest artifact can exist. Follow-up, not a blocker.

### Item 05 — signed desired-state wizard (`8f19214a`) — **PASS**
- **AC3 ✅** The browser does **not** compute the hash — it displays the server-authoritative `desired_state_hash` from `service/deploy-preview` and returns it as `expected_desired_state_hash` on `service/deploy`. The server rebuilds the snapshot and compares with a constant-time check (`encrypted_service_handlers.go:207`, `desiredStateHashesEqual` at `:439-448`). This is the right design; there is no cross-language canonical-JSON mismatch risk.
- **AC4 ✅** Secret values never enter the payload. `hashInput.SecretRefKeys` carries only sorted env-var **names**; `DesiredSecretRef.RedactedValue` is a `REDACTED(...)` placeholder, and `ContainsPlaintextSecret()` guards persistence.
- **AC5 ✅** Signer-first, idempotent, stale reviewed hashes rejected fail-closed.

### Item 06 — hostname / Cloudflare-Tunnel routing (`2ce6bf5d`) — **PASS except AC5**
- **AC2 ✅ (important, and correct)** The route plan is folded into the desired state **before** hashing, and `hashInput` includes `PublicRoute` for schema v4 (`internal/domain/runtime_desired_state.go:864, 949-953`). DNS, tunnel, proxy, TLS, provider config hash, apply order, and rollback order are all hash-covered. The obvious "hash computed before route was added" bug is genuinely avoided.
- **AC3 ✅** All validation precedes any provider mutation: zone membership, org ownership, protected-zone rule, origin allowlist, signed port exposure, upstream scheme (`internal/service/public_route.go:62-109`). The first mutation is the tunnel `PUT` at `cloudflare.go:151`.
  - *Note:* the `protected` bool returned by `Plan()` is discarded in the deploy path (`encrypted_service_handlers.go:198`). I checked whether this bypasses approval — **it does not**. `zone.Protected && !env.Protected` is a hard error (`public_route.go:73-75`), so a protected zone requires a protected environment, which independently forces `ApprovalStatusPending` (`registry.go:987-1001`). The discarded flag is redundant, not a hole.
- **AC4 ✅** `cloudflare.go` is API-only: bearer-token HTTP calls, no `os/exec`, no nginx/connector file editing. The API token appears only in the `Authorization` header and transport errors are deliberately opaque (`:340-344`).
- **AC6 ✅** `verifyHTTPS` gates run success.
- **AC5 ❌ FAILS — see Blocker 1.**

### Item 07 — observability, approval, rollback (`4987ce93`) — **PASS except AC4**
- **AC1 ✅** Approved intents execute once through the coordinator; phases persist; waits for healthy; finishes `in_sync`.
- **AC2 ✅ (verified end to end)** Rejection is atomic at the SQL level — `WHERE id = $1 AND approval_status = 'pending' AND status = 'pending'` (`internal/repository/pg_deployment.go:187-209`) — and `ExecuteDeployment` refuses any non-approved intent before artifact resolution, run creation, apply, or route publication (`coordinator.go:178-188`). **A rejected deploy provably makes no runtime change.** Approval re-evaluates current policy (`encrypted_service_handlers.go:491-505`).
- **AC3 ✅** `/deployments/<intent-id>` is the linkable aggregate with unit/endpoint, hash, digest, policy, phases, logs, health, drift, and failure.
- **AC5 ✅** Restart/reconcile and duplicate observations preserve the newest healthy `in_sync` state.
- **AC6 ✅ (notably good)** Stored-log redaction loads **every retained version** of each referenced secret, sorts longest-first, replaces raw and JSON-escaped forms, and **fails closed** — if any version cannot be decrypted, `GetRunLogs` returns no output at all (`encrypted_route_handlers.go:980-984, 1024-1076`). `pg_secret.go:92-118` exists specifically to make this safe across rotation. This satisfies the strictest reading of the secrets criterion.
- **AC7 ✅** Stable navigable URLs and rollback target links.
- **AC4 ❌ FAILS — see Blocker 2.**

---

## 2. Blockers — must be resolved before epic closure

### Blocker 1 — Route compensation executes in a different order than the signed rollback plan
**Severity: high.** Directly violates the epic criterion *"route changes inside the signed plan with rollback ordering."*

The operator reviews and signs a rollback ordering of **DNS → tunnel → application**:

```go
// internal/service/public_route.go:99
Rollback: []domain.DesiredPublicRouteChange{
  {Order: 1, Resource: "dns",          Action: "restore_or_withdraw", ...},
  {Order: 2, Resource: "tunnel_proxy", Action: "restore",             ...},
  {Order: 3, Resource: "application",  Action: "restore",             ...},
}
```

The implementation restores **tunnel first, DNS second**:

```go
// internal/adapters/routing/cloudflare.go:155-166
rollback := func(cause error) error {
    ...
    if err := b.putTunnelConfig(cleanupCtx, previousConfig); err != nil {   // :159  tunnel FIRST
        rollbackErrors = append(rollbackErrors, "restore tunnel: "+err.Error())
    }
    if dnsChanged {
        if err := b.restoreDNS(cleanupCtx, plan, previousDNS); err != nil { // :163  DNS SECOND
            rollbackErrors = append(rollbackErrors, "restore DNS: "+err.Error())
        }
    }
```

**Failure scenario.** `verifyHTTPS` fails after DNS was published (`dnsChanged == true`). Compensation removes the new tunnel ingress *while the new hostname is still published in DNS*. For the duration of that window — and **permanently if `restoreDNS` then fails** — DNS resolves the Arcana hostname to a Cloudflare tunnel that has no matching ingress, i.e. a live public hostname pointed at a dead upstream. The signed order exists precisely to prevent this: withdraw DNS first so no traffic arrives, *then* tear down ingress.

Two things are wrong here, and the second is the one that matters for this epic: the ordering is operationally unsafe, **and** the system did not do what the operator cryptographically approved. A signed plan whose declared rollback ordering is not the ordering executed undermines the whole "signed desired state" guarantee.

**Fix:** invert the two blocks in the `rollback` closure to match `Order: 1 = dns`, `Order: 2 = tunnel_proxy`. Better: drive compensation from `plan.Rollback` rather than hard-coding a sequence, so the signed plan and the executed plan cannot drift again. Add a `cloudflare_test.go` case asserting call order on `verifyHTTPS` failure.

### Blocker 2 — A healthcheck *timeout* does not offer rollback
**Severity: high.** Violates item-07 AC4 and the epic criterion *"failed healthcheck offers rollback."*

The UI gates the rollback affordance on two health values only:

```javascript
// web/src/routes/deployments/[id]/+page.svelte:82-84
let healthFailed = $derived(
  ['unhealthy', 'failed'].includes(String(runtimeState?.health_status || latestRun?.health_status || '').toLowerCase())
);
let canRollback = $derived(Boolean(intent && rollbackTargetIntent && healthFailed));
```

But the health-deadline path returns the *last observation seen*, whatever its status:

```go
// internal/service/runtime_lifecycle.go:659-663
return latest, &DeploymentHealthError{
    Code:    "health_check_timeout",
    Message: "The deployed service did not become healthy before the health deadline.",
}
```

and the coordinator persists that status verbatim: `run.ApplyMetadata["health_status"] = string(obs.HealthStatus)` (`coordinator.go:650`).

**Failure scenario.** Arcana's container starts but `/healthz` never passes within Bahia's health deadline. Docker still reports the container as `starting` (it has not exhausted its own `retries` yet). `health_status` is persisted as `"starting"`. `healthFailed` is `false`, so **no Rollback button renders** on precisely the failure this criterion names. `HealthStatus` has five values (`domain/models.go:83-87`) — `unknown`, `starting`, `healthy`, `unhealthy`, `stopped` — and **three of the four non-healthy values fail the gate.** This is the single most likely real-world Arcana failure (a misconfigured relay/inference URL yields a served-but-never-healthy nginx).

There is no test asserting the affordance appears on timeout; `web/tests/unit/deployment-observability.test.js` uses `health_status: 'starting'` only in a progress fixture.

**Fix:** derive `healthFailed` from the run's terminal outcome — the `failure.code` (`health_check_timeout`, `desired_state_mismatch`) or run status in `{failed, timeout}` — rather than from `health_status` alone. Add a unit test covering `health_status: 'starting'` + `failure.code: 'health_check_timeout'` ⇒ rollback offered.

---

## 3. Should-fix (strongly recommended alongside item 08)

### 3.1 Runtime apply recomputes the desired hash without comparing it to the signed hash
`desiredStateForDeployment` takes the persisted canonical snapshot, mutates labels and unit identity, then **recomputes** the hash with no equality check against `intent.DesiredHash`:

```go
// internal/service/runtime_lifecycle.go:966-967
spec.ComputeDesiredHash()
spec.Labels["bahia.desired_hash"] = spec.DesiredHash
```

Service, environment, artifact, and unit identity *are* checked just above (`:920-950`), so I found no concrete exploit — but `NormalizeDesiredServiceUnitIdentity` can populate previously-empty `DeploymentUnitKey`/`UnitRuntimeType`, and any future compatibility repair in this function would silently change the applied hash. The system then applies and records the *new* hash as authoritative. Add a fail-closed comparison: if the recomputed hash differs from `intent.DesiredHash`, abort the run.

### 3.2 `expected_desired_state_hash` is optional
`encrypted_service_handlers.go:207` only enforces the reviewed-hash match when the field is non-empty. Review-pinning is therefore a UI convention, not a protocol guarantee; any signer holding `deployments:write` can deploy without pinning a reviewed hash. Consider requiring it for Compose/managed deploys.

### 3.3 Rollback drops the public route from the signed plan
`service/rollback` never populates `PublicRoute` (`encrypted_service_handlers.go:326-332`), so the rollback intent's desired state has `public_route: nil`. The coordinator only applies routes when the field is non-nil (`coordinator.go:528-531`), so the previously published route survives — but it is now **outside** the signed plan, and the rollback hash no longer describes the live public surface. This contradicts "route changes inside the signed plan" for the rollback case and will read as drift. Carry the superseded intent's route plan forward into the rollback desired state.

### 3.4 No cross-process execution claim
Migration `000051` adds only a partial unique index on `deployment_runs(deployment_intent_id) WHERE status IN ('queued','running')`. Restart recovery lists the same rows in every process with no `FOR UPDATE`/`SKIP LOCKED` (`pg_deployment.go:161-181, 345-346`), and `activeExecutions` is process-local (`coordinator.go:858-875`). Direct-runtime deploys insert the run before applying, which mostly saves this — but two coordinators can still replay the same recovery. Benign at single-instance Bahia; must be fixed before any HA rollout. Related to open bead `bahia-udb2f`.

---

## 4. Acceptable follow-ups (do not block closure)

| Item | Assessment |
|---|---|
| `bahia-1tgwr` Gitea/HiveCI plumbing | **Correctly deferred.** Build UI fails closed; no private-GitHub fetcher was smuggled in. Item 08 cannot exercise a real private build until this lands — see caveat below. |
| `bahia-5teth` second route provider | Fine. Cloudflare is the only production provider and the abstraction (`routing.Resolver`) is in place. |
| `bahia-rwm0r` hostname reservations | Real but bounded: `Check` reads Cloudflare DNS/ingress and rejects non-owned records, then `Apply` mutates — a check-to-mutate race with no reservation. `applyMu` serializes within one process. Acceptable for a single-tenant first deploy. |
| `bahia-udb2f` service-update CAS | Fine; see 3.4. |
| Legacy `RegistryService.Rollback` approval bypass | `registry.go:1535-1543` pre-sets `ApprovalStatusNotRequired`/`IntentStatusApproved`, which `CreateDeploymentIntent` will **not** reset for a protected environment (`:987-1001`) — a genuine protected-approval bypass. **However it is dead code in production:** its only caller is `Reactor.handleRollbackRequest`, and `KindRollbackRequest` appears in no live dispatch switch (only tests). Not a blocker; **file an issue to delete it** before it gets re-wired. |
| Tenant RBAC missing on non-Arcana encrypted handlers | Backup, DNS, workers, Loom, notifications, SBOM, security, relay-settings handlers verify the signature at the transport but perform no tenant/RBAC check. Pre-existing and outside this epic's surface; every handler on the Arcana path (`service/*`, `environment/*`, `build/*`, `artifact/*`, `approval/*`) does authorize. Worth a separate hardening epic. |
| Loom accepts plaintext `env`/`params` values | `loom_contextvm_handlers.go:108-133` rejects the `secrets` map, payment tokens, and bunker URLs in cmd/args/env/params, but cannot detect an arbitrary secret pasted into `env`. Loom is declared unsuitable for Arcana and is not on this path. Out of scope. |
| Secret pasted into an approved `VITE_*` build arg | Reaches Nostr content and build metadata. This is inherent — those nine values are compiled into a **public** static bundle, so they are public by definition. Recommend an explicit "these values are public and baked into the bundle" warning on the builds form. |
| Renderer extensions excluded from the hash | `ComposeExtension`/`DockerExtension`/`KubernetesExtension` are outside `hashInput`. For Arcana this is inert (the Compose extension is empty for managed services — `runtime_desired_state_builder.go:258-264`). Material only for K8s, which is out of scope. |
| HiveCI digest-format validation | See item 04 caveat. Managed path re-validates, so Arcana is safe. |

---

## 5. Secret-leak assessment (explicit)

I hunted specifically for the four escape channels. Result for the Arcana path:

- **Nostr events:** clean. Only `repository_credential_ref` (opaque), the nine public `VITE_*` args, artifact digests, and desired-state snapshots carrying `REDACTED(...)` placeholders.
- **Logs:** **fails closed**, which is the strong outcome — stored run logs are withheld entirely if any retained secret version cannot be decrypted and redacted. One near-miss: `logRuntimeAction` logs raw `err.Error()` from runtime adapters with no scrubber (`runtime_lifecycle.go:1230-1233`); the inspected Compose adapter returns only `exec.ExitError` without secret-bearing output, so no leak is demonstrated — but there is no redaction boundary there. Worth a follow-up.
- **argv:** clean. Compose secrets go into `cmd.Env`, never argv (`compose.go:330-336`).
- **Browser state:** only the deliberate, permission-gated secret-reveal handler returns plaintext, over the encrypted channel, to an authorized caller.

No secret-leak finding blocks this epic.

---

## Verdict and readiness for item 08

**Epic closure: BLOCKED** on Blocker 1 (route rollback ordering contradicts the signed plan) and Blocker 2 (healthcheck timeout does not offer rollback). Both are explicit epic acceptance criteria, both are small, well-localized fixes, and both currently lack test coverage.

**`fp-bahia-arcana-08-e2e` live acceptance run: proceed, with conditions.**

The happy path is sound. The signed-plan architecture is genuinely well built — route-in-hash, server-authoritative hashing with a constant-time reviewed-hash gate, atomic rejection with provable no-op, fail-closed log redaction, and an API-only Cloudflare adapter with no shell-out. I found no way to make a runtime change without a verified signed event on the Arcana path.

Conditions:

1. **Fix Blocker 2 before the run.** It is a few lines in one Svelte derivation, and item 08 will almost certainly hit a not-yet-healthy deploy during a first live bring-up.
2. **Fix Blocker 1 before the run if item 08 includes a forced route-failure scenario** — which it should, since "rollback ordering" is an epic criterion that only a failure injection can demonstrate. If 08 is happy-path only, Blocker 1 can land immediately after, but the epic must not close until it does.
3. **Scope the run honestly.** With `bahia-1tgwr` open, item 08 cannot exercise a real private-GitHub build. The run should either use a pre-registered artifact or explicitly record the build step as simulated, so "browser-only end to end" is not over-claimed.
4. Add the three missing regression tests named above (route compensation order, rollback-on-timeout, recomputed-hash equality).

**Recommended issues to file:** route compensation ordering (P1); rollback affordance on health timeout (P1); recomputed-hash equality guard (P2); rollback carries the public-route plan (P2); delete dead legacy `RegistryService.Rollback` (P2); tenant RBAC sweep for non-Arcana encrypted handlers (P2, separate epic); redaction boundary on `logRuntimeAction` (P3); missing `acceptance_criteria.json` for item 02 (P3).
