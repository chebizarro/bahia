# Test-Quality & Fake-Completion Audit — bahia

- **Date:** 2026-07-16
- **Auditor:** automated review agent (read-only)
- **Scope:** `test/**`, `web/tests/**`, all `*_test.go`, `.github/workflows/**`, `scripts/**`, and a ~10-record sample of `pstf/features/**`
- **Method:** static reading + `go test ./...` (full run), grep sweeps for skip/mask patterns, plus parallel targeted probes whose load-bearing claims were re-verified by hand against the cited source.

## Context / Scope

This repo was assembled by many successive LLM sessions, so the audit assumed tests may have been shaped to pass rather than to validate reality. The hunt targeted: skipped/disabled tests, "mock theater" (SUT is itself a stub), overly permissive assertions, golden files pinning placeholder output, e2e harnesses that stub the system under test, CI that masks failures, and `pstf` completion records overstating what shipped.

**Test-run result:** `go build ./...` and `go test ./...` both pass (exit 0) locally on go1.26.3. No failures or hangs were observed. Note this result is **cached** and, crucially, is **never reproduced in CI** (see F1). Integration tests self-skip without env vars (`INTEGRATION_TEST=1`, `BAHIA_TEST_DB=1`, `DATABASE_URL`) — a standard, acceptable pattern, not a finding.

**Severity key:** P0 critical · P1 high · P2 medium · P3 low · P4 informational.

## Summary of Findings

| # | Severity | Category | Title |
|---|----------|----------|-------|
| F1 | P1 | ci-gap | No CI runs `go test`/`go build` — the entire Go backend is ungated |
| F2 | P2 | fake-completion | `SBOM_WORKFLOW_E2E` claims verified E2E completion but E2E is mock-injected |
| F3 | P2 | fake-completion | `MULTI_REGISTRY_PACKAGE_CONTROLPLANE` claims complete; Nexus/Pulp are self-labeled skeletons |
| F4 | P2 | misleading-tests | e2e-agent "self-healing" mutates harness source until runs go green |
| F5 | P2 | misleading-tests | e2e-agent deployment scenarios warn-only on wrong/absent status, still report `passed` |
| F6 | P2 | misleading-tests | "Full Deployment Workflow" scenario never deploys |
| F7 | P2 | weak-tests | Registry factory tests assert only non-nil; never verify the selected adapter/routing |
| F8 | P2 | misleading-tests | Stale "not yet implemented / Agent 2 merges" K8s skip guards — feature is fully implemented |
| F9 | P3 | misleading-tests | Empty placeholder test with no assertions that can never fail |
| F10 | P3 | weak-tests | e2e-agent Nostr "smoke" scenario only checks env-var presence, never touches the system |
| F11 | P3 | weak-tests | `isDashboardLoaded()` only checks that `<body>` exists |
| F12 | P3 | weak-tests | NIP-44 empty-plaintext test skips on any encrypt error |
| F13 | P3 | weak-tests | Decoder-completeness test only detects one exact error string; other decode errors pass |
| F14 | P4 | misleading-tests | Scenario steps recorded `passed` before their assertions run |
| F15 | P4 | ci-gap | `hive-ci-build.yml` is `workflow_dispatch`-only (documented; image build never gates PRs) |

---

## Findings

### F1 — No CI runs `go test` or `go build` (P1, ci-gap)

**Files:** `.github/workflows/` (all four files: `deploy-edge.yml`, `hive-ci-build.yml`, `web-playwright-e2e.yml`, `web-vitest-unit.yml`)

**Evidence:** A grep for any Go build/test invocation across `.github/` returns nothing:

```
$ grep -rln "go test|go build|golangci|setup-go|go vet" .github/
(no matches)
```

The four workflows and their triggers:

- `deploy-edge.yml` — `on: push: branches: [master]` — **deploys** on merge; runs no `go test`.
- `hive-ci-build.yml` — `on: workflow_dispatch` — builds images only, manual (see F15).
- `web-playwright-e2e.yml` — `on: pull_request/push` scoped to `web/**` — web only.
- `web-vitest-unit.yml` — `on: pull_request/push` scoped to `web/**` — web only.

**Why it's inadequate:** The repository contains **373 `*_test.go` files** and none of them are executed by CI on pull requests or pushes. Only the web (`web/**`) suites gate PRs. The entire Go control plane — runtime adapters, SBOM orchestration, registry inspection, backup, security scanner, Nostr projection — has **zero automated regression protection**. A PR that breaks or deletes Go tests, or breaks the build, passes all required checks. This is the single most consequential gap: every other Go finding below is invisible to CI, and the local "PASS" is not enforced anywhere.

**Recommended fix:** Add a `go-ci.yml` workflow triggered on `pull_request` and `push` to master that runs `go build ./...`, `go vet ./...`, `go test ./... -race`, and a linter (golangci-lint). Make it a required status check. If the Go suite currently depends on cached results to stay fast, wire module/build caching rather than skipping the run.

---

### F2 — `SBOM_WORKFLOW_E2E` overstates E2E completion (P2, fake-completion)

**Files:** `pstf/features/SBOM_WORKFLOW_E2E/verification_report.md:5`, `:22`, `:56-57`; production ok at `internal/adapters/sbom/generator.go`

**Claim (report:5):**
> Bead `bahia-1qe9.9` verified completed SBOM epics `bahia-1qe9.1` through `bahia-1qe9.8` against … **production-readiness constraints**.

**Reality:** The browser "E2E" does not exercise a real relay/Blossom/backend round trip. The report itself states the harness **injects mock relay outcomes** (`verification_report.md:56-57`):

> `web/tests/e2e/helpers.js` can now deterministically observe gift-wrapped SBOM generate requests and **inject `30315`, `4903`, `30078`, and `30004` mock relay outcomes** for browser verification without sleeps.

And epic `bahia-1qe9.8` admits the signer-first generate/import UI E2E is **not done** (`verification_report.md:22`):

> Browser E2E for signer-first generate/import UI/control flows **remains tracked as `bahia-wf2k`**.

**Why it's misleading:** The production SBOM core is real (Syft generator selection is enforced — not a stub), so the feature is not fake. But "verified completed … against production-readiness constraints" for an **E2E** feature is contradicted by (a) mock-injected relay events standing in for the real publication path and (b) an explicitly open follow-up for the actual signer-first UI flow. A reader trusts "E2E verified" to mean the end-to-end path was exercised; it was not.

**Recommended fix:** Downgrade the record's wording from "E2E verified/complete" to "deterministic mocked-relay browser coverage; true E2E tracked in `bahia-wf2k`." Keep `bahia-wf2k` open and linked.

---

### F3 — `MULTI_REGISTRY_PACKAGE_CONTROLPLANE` claims completion over skeleton backends (P2, fake-completion)

**Files:** `pstf/features/MULTI_REGISTRY_PACKAGE_CONTROLPLANE/verification_report.md:3-9`; `internal/backends/nexus/nexus.go:60-61`; `internal/backends/pulp/pulp.go:65-66`; `internal/backends/factory/factory.go`

**Claim:** backend abstraction/service core and signer-first package control plane "complete," supporting Nexus, Pulp, and multiple package formats.

**Reality — production code labels both adapters skeletons:**

```go
// internal/backends/nexus/nexus.go:60-61
// The skeleton Nexus adapter observes existence via HTTP but does not yet read
// independent backend checksums for byte-level drift.
```

```go
// internal/backends/pulp/pulp.go:65-66
// The skeleton Pulp adapter observes existence via HTTP but does not yet read
// independent backend checksums for byte-level drift.
```

Both set `caps.CanObserveDrift = false`. The backend factory also ships a backend literally named `filesystem_mock` (`internal/backends/factory/`).

**Why it's misleading:** Nexus/Pulp do perform real HTTP calls (not empty stubs), but the record's own body concedes unresolved API/version, auth, TLS, and secret integration, and the code self-describes as "skeleton." "Multi-registry package control plane complete" overstates production readiness — drift observation, a core control-plane guarantee, is disabled on both real backends.

**Recommended fix:** Re-scope the completion claim to "control-plane plumbing + skeleton Nexus/Pulp adapters (drift observation not yet implemented)." File issues for the missing checksum/drift, auth, and TLS work.

---

### F4 — e2e-agent "self-healing" mutates harness source until green (P2, misleading-tests)

**Files:** `test/e2e-agent/healing-loop.ts:33-68`; `test/e2e-agent/fixer.ts:43-64`, `:82-113`

**Evidence:** `healing-loop.ts` reruns scenarios and, on failure, auto-applies fixes (unless `requireApproval`), then loops until the report is green. `fixer.applyFix` rewrites source files on disk:

```ts
// fixer.ts:43-55
const original = await readFile(absolutePath, 'utf8');
if (!original.includes(proposal.search)) { throw ... }
const updated = original.replace(proposal.search, proposal.replace);
const backupPath = `${absolutePath}.heal-backup-${Date.now()}`;
await writeFile(backupPath, original, 'utf8');
await writeFile(absolutePath, updated, 'utf8');
```

The generated "fixes" weaken synchronization until the flake disappears — e.g. doubling the selector timeout and inserting a pre-click wait:

```ts
// fixer.ts:88-105
case 'timeout': // ...
  search:  'async waitForSelector(selector: string, timeout = 5000)',
  replace: 'async waitForSelector(selector: string, timeout = 10000)',
case 'ui_element_not_found': // ...
  replace: "  async click(selector: string) {\n    ...await page.waitForSelector(selector, { timeout: 5000, state: 'visible' });\n    await page.click(selector);\n  }",
```

**Why it's misleading:** A "green" produced by a loop that edits its own driver code between attempts is not a result against a fixed test definition — it is non-reproducible unless the exact patch is reviewed and retained. To its credit the fixer only touches harness driver files (`drivers/playwright.ts`, `drivers/api.ts`) and does **not** delete assertions, so the risk is bounded to synchronization tampering rather than assertion removal. Still, an automated harness that reports success after mutating itself cannot be trusted as evidence of system correctness.

**Recommended fix:** Never auto-apply source mutations in an unattended/CI run — gate healing behind `requireApproval` by default, emit patches as artifacts for human review, and treat a run that required healing as a failure (or at minimum a distinct "healed" state) rather than success.

---

### F5 — Deployment approval/rejection scenarios warn-only on wrong status (P2, misleading-tests)

**Files:** `test/e2e-agent/scenarios/deployments.ts:174-182` (approval), `:269-276` (rejection)

**Evidence:**

```ts
// deployments.ts:174-182
const status = (fetchedIntent.data as any)?.status;
if (status && status !== 'approved' && status !== 'pending_deployment') {
  console.warn(`Warning: Intent status is "${status}", expected ...`);
}
return { name: this.name, status: 'passed', ... };
```

The rejection scenario has the identical shape, expecting `'rejected'`.

**Why it's misleading:** The behavior the scenario is named for (approval / rejection actually taking effect) is never asserted — a wrong status only logs a `console.warn` and the scenario still returns `passed`. Worse, the guard `if (status && ...)` means a **missing** status field also passes silently. Any successful GET produces green regardless of the deployment intent's real state.

**Recommended fix:** Replace the warn with a hard assertion: fail the scenario if `status !== 'approved'` (resp. `'rejected'`), and fail if `status` is absent.

---

### F6 — "Full Deployment Workflow" scenario never deploys (P2, misleading-tests)

**Files:** `test/e2e-agent/scenarios/deployments.ts:297-300` (name/description), `:318-322` (env created unprotected), `:351-368` (conditional approve, no deploy)

**Evidence:** The scenario advertises `Complete deployment workflow: create intent, approve, deploy`, but the environment is created **unprotected**, and the only post-fetch action is `if (environment.data!.protected) { … approveDeploymentIntent … }`. Since the environment is unprotected, approval is skipped and **no deploy call or deployed-state assertion occurs** before it returns `passed`.

**Why it's misleading:** The scenario self-reports successful completion of a three-step workflow (create → approve → deploy) while executing only the first step. Its name is direct evidence of coverage that does not exist.

**Recommended fix:** Either drive a protected environment through real approve + deploy and assert the deployed state, or rename the scenario to reflect that it only creates an intent.

---

### F7 — Registry factory tests assert only non-nil (P2, weak-tests)

**File:** `internal/adapters/registry/factory_test.go:73-116` and `:169-191`

**Evidence:** Every factory-selection test checks only that construction returns non-nil, never that the correct concrete adapter/endpoint/auth was produced:

```go
// TestNewInspector_GHCR (:73-83), _DockerHub (:85-94), _AutoDetect (:96-105), TestNewVerifier (:107-116)
inspector, err := NewInspector(RegistryConfig{Type: RegistryGHCR, Password: "test-pat"}, logger)
if err != nil { t.Fatalf(...) }
if inspector == nil { t.Fatal("expected non-nil inspector") }
```

`TestInspectorForImage` (`:169-191`) routes `ghcr.io/...`, `docker.io/...`, a generic registry, and a bare `nginx` — but each case asserts only `inspector != nil`:

```go
{"ghcr", "ghcr.io/myorg/myapp", true},
{"dockerhub", "docker.io/library/nginx", true},
{"generic", "registry.example.com/myorg/myapp", true},
{"no_host", "nginx", true},
// ... if inspector == nil { t.Fatal("expected non-nil inspector") }
```

**Why it's inadequate:** If auto-detection routed every image — GHCR, generic, unqualified — to a single wrong implementation, all cases would still pass. The tests validate that *something* was allocated, not that the *right* registry adapter was selected, which is the entire job of a factory. (Note: `TestVerifierAdapter` in the same file *is* a genuine test — it stands up an `httptest` server and asserts real digest/existence behavior — so the package is not wholly untested.)

**Recommended fix:** Type-assert the returned inspector/verifier to the expected concrete type (or assert an observable property such as the resolved base URL/registry host) per case.

---

### F8 — Stale K8s "not yet implemented" skip guards (P2, misleading-tests)

**Files:** `internal/adapters/runtime/desired_state_capability_test.go:234-251`; `internal/adapters/runtime/resolver_desired_state_test.go:199-231`; contradicted by `internal/adapters/runtime/kubernetes_desired_state.go:686`, `:700-810`

**Evidence:** Two tests skip themselves "until Agent 2's SupportsDesiredState() flip lands":

```go
// desired_state_capability_test.go:243-251
k8s := NewKubernetesRuntime("", "default", "", zap.NewNop())
if !k8s.SupportsDesiredState() {
    t.Skip("Kubernetes desired-state not yet implemented — will pass after Agent 2 merges")
}
_, err := k8s.ApplyDesiredState(ctx, DesiredStateApplyRequest{})
if errors.Is(err, ErrDesiredStateNotSupported) {
    t.Error("should not return ErrDesiredStateNotSupported when SupportsDesiredState() is true")
}
```

```go
// resolver_desired_state_test.go:218-230
applier, err := resolver.ResolveDesiredStateApplier(svc, env)
if err != nil {
    if errors.Is(err, ErrDesiredStateNotSupported) {
        t.Skip("Kubernetes desired-state not yet implemented")
    }
    t.Fatalf("unexpected error: %v", err)
}
```

But the K8s adapter is fully implemented and hardcodes support:

```go
// kubernetes_desired_state.go:686
func (k *KubernetesRuntime) SupportsDesiredState() bool { return true }
// :700-810  ApplyDesiredState validates, finds managed deployment, handles
// no-op/dry-run, generates manifests, and runs `kubectl apply -f -`.
```

`TestSupportedRuntimesReportCapability` (same package) even *asserts* `k8s.SupportsDesiredState()` is true.

**Why it's misleading:** The skip branches are **dead code** — `SupportsDesiredState()` never returns false — and the comments ("not yet implemented," "after Agent 2 merges") describe a state that no longer exists. A future reader (or agent) will believe K8s desired-state is unimplemented. When the guard *does* fall through, the assertion is also weak: `ApplyDesiredState` is called with an empty request and the test accepts **any** error except the single `ErrDesiredStateNotSupported` sentinel, so unrelated regressions pass.

**Recommended fix:** Delete the skip guards and stale comments; assert the real behavior — a nil `TargetService` should yield the specific "target service spec is nil" error, and the resolver should return a K8s applier whose `SupportsDesiredState()` is true.

---

### F9 — Empty placeholder test that can never fail (P3, misleading-tests)

**File:** `internal/adapters/runtime/desired_state_capability_test.go:158-161`

**Evidence:**

```go
func TestUnsupportedRuntimesReturnExplicitError(t *testing.T) {
    // No stub runtimes remain — all known runtimes support desired-state convergence.
    // This test is kept as a placeholder so the guard pattern is not lost.
    _ = context.Background()
}
```

**Why it's misleading:** The test name asserts a contract ("unsupported runtimes return an explicit error") but the body executes no production code and contains no assertion — it is green by construction and counts toward the suite as if it verified something. It inflates the apparent test count for the desired-state contract.

**Recommended fix:** Delete it, or replace it with a real negative case using a runtime that does not implement `DesiredStateApplier` (the package already defines `mockRuntimeNoDesiredState` for exactly this) and assert `ErrDesiredStateNotSupported`.

---

### F10 — e2e-agent Nostr "smoke" only checks env-var presence (P3, weak-tests)

**File:** `test/e2e-agent/scenarios/events.ts:20-40`

**Evidence:** The scenario ignores all drivers (`run(_drivers)`) and reads env vars, then passes solely because the strings are non-empty:

```ts
const relays = splitList(process.env.BAHIA_BOOTSTRAP_RELAYS || process.env.BAHIA_NOSTR_RELAYS);
const servicePubkeys = splitList(process.env.BAHIA_SERVICE_PUBKEYS || process.env.BAHIA_SERVICE_PUBKEY);
if (relays.length === 0) throw ...;
if (servicePubkeys.length === 0) throw ...;
// ... status: 'passed'
```

**Why it's inadequate:** Tagged as a `smoke` test for sidecar discovery, it never contacts a relay, sidecar, or Bahia read model. Arbitrary non-empty values pass. It validates configuration presence, not discovery behavior.

**Recommended fix:** Actually perform a discovery round-trip against the configured relays/pubkeys and assert on the returned system info, or retag it as a config-precondition check rather than a smoke test.

---

### F11 — `isDashboardLoaded()` only checks that `<body>` exists (P3, weak-tests)

**File:** `test/e2e-agent/drivers/playwright.ts:193-202`

**Evidence:**

```ts
async isDashboardLoaded(): Promise<boolean> {
  const page = this.getPage();
  try { await page.waitForSelector('body', { timeout: 5000 }); return true; }
  catch { return false; }
}
```

**Why it's inadequate:** Virtually any HTML document — an error page, a reverse-proxy page, a blank app shell, or an unrelated site — contains `<body>`. This proves navigation returned HTML, not that the Bahia dashboard rendered or functions.

**Recommended fix:** Wait for a dashboard-specific selector (a known nav landmark, heading, or data-testid) and assert its presence/content.

---

### F12 — NIP-44 empty-plaintext test skips on any encrypt error (P3, weak-tests)

**File:** `internal/adapters/secrets/nip44_test.go:195-201`

**Evidence:**

```go
ct, err := enc.Encrypt("", domain.EncryptionNIP44)
if err != nil {
    // Empty plaintext rejection is acceptable.
    t.Skipf("NIP-44 rejects empty plaintext: %v", err)
}
```

**Why it's inadequate:** Any encryption failure — not only an intentional empty-plaintext rejection — turns the test into a skip. A regression that broke encryption for empty input (or for any reason surfaced here) would be silently tolerated rather than caught.

**Recommended fix:** Decide the contract. If empty plaintext must be rejected, assert a specific error. If it must round-trip, assert the round-trip. Do not convert an arbitrary error into a skip.

---

### F13 — Decoder-completeness test only detects one exact error string (P3, weak-tests)

**File:** `internal/adapters/nostr/catalog_test.go:69-85`

**Evidence:**

```go
_, err := decoder(requiredDecoderFixture(kind))
if err != nil && strings.Contains(err.Error(), "not yet implemented") {
    notImplemented = append(notImplemented, kind)
}
// ... fails only if notImplemented is non-empty
```

**Why it's inadequate:** The test's purpose is to prove every catalog kind has a real decoder, but it only flags decoders returning the exact substring `"not yet implemented"`. A decoder that panics-to-error, returns a malformed result, or uses a *different* placeholder message passes silently. It cannot demonstrate decoders actually *work* — only that they don't emit one specific sentinel.

**Recommended fix:** Assert that each decoder returns a non-nil, well-formed result (no error) for its fixture, rather than pattern-matching on one placeholder string.

---

### F14 — Scenario steps recorded `passed` before assertions run (P4, misleading-tests)

**Files:** `test/e2e-agent/scenarios/services.ts:27-47` (representative; pattern recurs across scenarios)

**Evidence:** A step is pushed as `passed` immediately after the call returns, before the validation that could invalidate it:

```ts
const result = await drivers.api.createService(...);
steps.push(step('Create service', 'passed', ...));
if (!result.data?.id) { throw new Error('Service creation did not return an ID'); }
```

**Why it's misleading:** The overall scenario still fails via the catch block, so the top-line result is correct. But the per-step evidence in the report shows the operation as "passed" even when the subsequent assertion throws — the detailed step log overstates what was actually verified.

**Recommended fix:** Record a step as `passed` only after its assertion succeeds; otherwise mark it `failed`/`error`.

---

### F15 — `hive-ci-build.yml` is `workflow_dispatch`-only (P4, ci-gap)

**File:** `.github/workflows/hive-ci-build.yml:21-31`

**Evidence:** The workflow has only a `workflow_dispatch` trigger; there is no `pull_request` or `push`.

**Why it's (mildly) inadequate:** GitHub branch protection cannot use a manual-only workflow as a required check, so the image build never gates PRs on GitHub. **However**, the file documents this as intentional: the durable path is executed by `hive-ci-runner` via `nektos/act` (it needs Harbor credentials GitHub-hosted runners lack, and the repo's `allowed_actions=local_only` policy would make a GitHub run `startup_fail`). This is a documented architectural choice, not failure-masking, and is listed here only for completeness. The related `|| true` uses at lines ~113/117 are two digest-resolution attempts followed by an explicit `exit 1` validation, so they do **not** mask the outcome.

**Recommended fix:** None required beyond ensuring the external Hive-CI path is itself monitored; optionally add a lightweight GitHub `pull_request` job that runs `docker build` (no push) to catch Dockerfile breakage early.

---

## Areas Reviewed That Were Sound (no finding)

- **Golden files** (`internal/adapters/runtime/testdata/golden/*`, consumed by `compose_renderer_test.go`): the five golden files encode the Compose/env/metadata rendering of a synthetic fixture. `registry.example.com/...` images and `REDACTED(...)` secret values are intentional (the redaction golden specifically proves secret material is *not* emitted). `render_metadata.json` pins short fake digests (`sha256:abc123`) but those are **test inputs** fed to a JSON-formatting assertion, not code output — the test validates serialization/field propagation, which is legitimate (though it should not be mistaken for digest-computation coverage). The `UPDATE_GOLDEN=1` regenerate path is the standard pattern; normal-mode runs byte-compare committed files.
- **e2e-agent drivers** (`drivers/api.ts`, `drivers/mcp.ts`, `drivers/playwright.ts`): perform real `fetch`/JSON-RPC/Chromium I/O against a running stack — not canned/mock responses. The harness genuinely targets a live system; its weakness is over-reported success (F4–F6, F10, F11, F14), not fabricated I/O.
- **`deploy-edge.yml`**: every shell block uses `set -euo pipefail`; build and health-check failures propagate.
- **`web-vitest-unit.yml` / `web-playwright-e2e.yml`**: run `vitest run` / `playwright test` with no `--passWithNoTests` or zero-test escape; failures fail the job.
- **pstf spot-checks that held up:** `AI_FABRIC_SIGNER_FIRST_PROTOCOL`, `DESIRED_STATE_RUNTIME_KUBERNETES`, `LLM_ROUTE_RELEASE_DEPLOYMENT`, `SOUL_FACTORY_RUNTIME_LIFECYCLE`, `EDGE_DEPLOY_WORKFLOW`, `SECURITY_OSV_SBOM`, `RELAY_STRATEGY_NIP65_SERVICE_PREFS`, `BACKUP_CONTROL_PLANE_ORCHESTRATION` — claims matched real, non-stub production code, with limitations disclosed appropriately.
- **Skips that are legitimate:** integration tests behind `INTEGRATION_TEST=1` / `BAHIA_TEST_DB=1` / `DATABASE_URL`, the opt-in HF/vLLM production verification, POSIX-shell-fixture and symlink-availability platform guards, and the `BAHIA_REAL_SIDECAR_SMOKE` opt-in web smoke — all standard environment-gated patterns.

## Top Recommendations (priority order)

1. **F1 — stand up Go CI** (`build` + `vet` + `test -race` + lint on PRs, required check). Without this, every Go finding here is unenforced and future regressions are undetectable.
2. **F4 — disable unattended self-healing** in the e2e-agent; treat "healed" as not-passed and emit patches for human review.
3. **F5/F6 — convert warn-only deployment checks into hard assertions** and make the "full deployment" scenario actually deploy (or rename it).
4. **F2/F3 — correct the overstated `pstf` records** and reopen/track the genuinely incomplete work (`bahia-wf2k`; Nexus/Pulp drift/auth/TLS).
5. **F7/F8/F9 — strengthen the Go adapter tests**: assert concrete factory selection, delete dead K8s skip guards and stale comments, and remove or implement the empty placeholder test.
