# Bahia Spec Gap Report

## Scope

This report is based on sampled product docs, backend router/handlers/domain/control-plane code, web routes/stores, tests, CI workflows, and recent investigation artifacts as of **2026-05-05**.

Primary evidence used repeatedly:
- `docs/control-planes.md`
- `internal/api/router/router.go`
- `internal/api/handlers/system.go`
- `internal/domain/models.go`
- `web/src/lib/stores/controlplane.svelte.js`
- `web/src/lib/stores/auth.svelte.js`
- `web/src/lib/stores/payments.svelte.js`
- `.github/workflows/*.yml`
- `test/integration/e2e_ci_registry_test.go`

---

## Missing specification

### 1. `/api/v1/system/info` is a critical contract but under-documented
The browser depends on `/api/v1/system/info` for relay discovery, feature gating, service pubkey discovery, registry discovery, and control-plane kind maps. `docs/api.md` does not document it even though it is required for normal bootstrap.

**Evidence**
- `internal/api/handlers/system.go`
- `internal/api/router/router.go`
- `web/src/lib/stores/controlplane.svelte.js`
- `docs/api.md`

**Label:** `observed_not_specified`

### 2. Relay-read-model-first browser behavior is not clearly specified
Current shared UI state is not primarily loaded from REST list endpoints. The web app bootstraps relay subscriptions, waits for EOSE, then stays live on subscriptions.

**Evidence**
- `web/src/lib/stores/controlplane.svelte.js`
- `docs/control-planes.md`
- `README.md`
- `docs/api.md`

**Label:** `observed_not_specified`

### 3. The current public request-kind surface is wider than the docs describe
The documented 596x tables are not the whole current picture. The web client includes additional public helper kinds for service/environment/policy/artifact operations beyond the summarized tables.

**Evidence**
- `web/src/lib/nostr/client.js`
- `web/src/lib/stores/public-controlplane.svelte.js`
- `docs/control-planes.md`
- `docs/nostr-commands.md`

**Label:** `observed_not_specified`

### 4. Payment transport split is under-specified
The current UI uses encrypted `payments.history`, while REST still serves payment history, run cost, and estimates. The product contract does not clearly explain which payment operations are canonical on which transport.

**Evidence**
- `web/src/lib/stores/payments.svelte.js`
- `web/src/routes/+page.svelte`
- `internal/api/handlers/payments.go`
- `docs/control-planes.md`

**Label:** `observed_not_specified`

### 5. Souls are product-visible but not documented as a relay-only surface
`/souls` is a real route and is relay-backed, but there is no matching REST API surface documented in `docs/api.md`. The product boundary is therefore implicit in code rather than explicit in docs.

**Evidence**
- `web/src/routes/souls/+page.svelte`
- `web/src/lib/stores/souls.svelte.js`
- `internal/domain/soul.go`
- `docs/api.md`

**Label:** `observed_not_specified`

### 6. Adoption is documented operationally but not specified as a current web feature
Adoption/import is well documented as operator/API/CLI behavior, and the web API client exposes adoption methods, but there is no current adoption route in `web/src/routes`.

**Evidence**
- `docs/adoption-production-rollout.md`
- `web/src/lib/api/client.js`
- `web/src/routes/`

**Label:** `specified_not_observed`

---

## Conflicting specification

### 1. Web auth documentation conflicts with current implementation
`web/README.md` still says the dashboard currently uses JWT tokens in localStorage and that NIP-07/NIP-46 are future work. Current code implements signer-first NIP-07/NIP-46 session handling and direct NIP-98 compatibility.

**Evidence**
- `web/README.md`
- `web/src/lib/stores/auth.svelte.js`
- `docs/control-planes.md`

### 2. Older docs frame Bahia as REST-first while current behavior is relay-first
`README.md` and `docs/architecture.md` describe Bahia mostly as a REST deployment registry. Current browser behavior and control-plane docs show a sidecar-first, relay-read-model-first, signer-first control plane.

**Evidence**
- `README.md`
- `docs/architecture.md`
- `docs/control-planes.md`
- `web/src/lib/stores/controlplane.svelte.js`

### 3. `docs/protocol-compatibility.md` is internally inconsistent on NIP-46
The quick matrix calls NIP-46 stubbed, while later sections say Signet implements full NIP-46 bunker support.

**Evidence**
- `docs/protocol-compatibility.md`

### 4. 311xx status conflicts across docs
`docs/protocol-compatibility.md` still presents 31100-31105 as implemented command kinds. Current control-plane docs and command docs treat them as deprecated compatibility only.

**Evidence**
- `docs/protocol-compatibility.md`
- `docs/control-planes.md`
- `docs/nostr-commands.md`

### 5. Soul lifecycle completeness conflicts across docs
`docs/soul-factory.md` reads like a substantial complete lifecycle story, while `docs/protocol-compatibility.md` still lists Soul Lifecycle as an incomplete known gap.

**Evidence**
- `docs/soul-factory.md`
- `docs/protocol-compatibility.md`

### 6. Web production-plan claims conflict with current routes/tests
`docs/WEB_APP_PRODUCTION_PLAN.md` describes missing notifications UI and many old client/UI gaps that are no longer true in the current route/test surface.

**Evidence**
- `docs/WEB_APP_PRODUCTION_PLAN.md`
- `web/src/routes/notifications/`
- `web/tests/e2e/notifications-encrypted-smoke.spec.js`

---

## Untested behavior

### 1. CI-to-registry-to-artifact end-to-end behavior is not truly verified
The file named `e2e_ci_registry_test.go` mostly contains placeholder `t.Log("Would test...")` cases rather than real end-to-end assertions.

**Evidence**
- `test/integration/e2e_ci_registry_test.go`

### 2. Go test suites are not run in CI
There are only two CI workflows and both are web-only. The large Go test surface is not automatically executed in GitHub Actions.

**Evidence**
- `.github/workflows/web-playwright-e2e.yml`
- `.github/workflows/web-vitest-unit.yml`
- `internal/**/*_test.go`

### 3. DB integration tests are opt-in only and not wired to CI
The registry integration test requires `BAHIA_TEST_DB=1` and is not automatically exercised in CI.

**Evidence**
- `test/integration/registry_test.go`
- `.github/workflows/`

### 4. Agent E2E harness exists but is not part of automated verification
The TypeScript harness under `test/e2e-agent/` is documented and scenario-driven but not currently wired into CI.

**Evidence**
- `test/e2e-agent/README.md`
- `test/e2e-agent/DELIVERY.md`
- `.github/workflows/`

### 5. Payments and orgs encrypted flows lack strong page-level E2E proof
Store/unit tests exist, but the sampled E2E suite does not show equivalent browser-flow coverage for payments or org pages.

**Evidence**
- `web/tests/unit/encrypted-domain-stores.test.js`
- `web/src/routes/payments/+page.svelte`
- `web/src/routes/orgs/`
- `web/tests/e2e/`

### 6. Relay bootstrap behavior is better tested in unit than in browser E2E
The product-critical EOSE/bootstrap/live-subscription flow is clearly implemented and unit tested, but not equally well proven by real browser end-to-end coverage.

**Evidence**
- `web/src/lib/stores/controlplane.svelte.js`
- `web/tests/unit/controlplane-store.test.js`
- `web/tests/e2e/controlplane-nostr-smoke.spec.js`

---

## Risky behavior

### 1. Mixed transport model raises acceptance risk
Bahia mixes relay read models, public signed Nostr writes, encrypted request/result flows, narrowed REST CRUD/query routes, direct NIP-98 auth, and MCP. Each feature needs explicit transport acceptance criteria or teams will test the wrong surface.

**Evidence**
- `docs/control-planes.md`
- `internal/api/router/router.go`
- `web/src/lib/stores/controlplane.svelte.js`
- `web/src/lib/stores/auth.svelte.js`

### 2. Public vs encrypted relay separation is a security-critical boundary
Sensitive operations must remain off public browser/sidecar relays. This is clearly encoded in the design, so drift here would be a serious product/security defect.

**Evidence**
- `docs/control-planes.md`
- `internal/controlplane/encrypted_transport.go`
- `web/src/lib/nostr/encrypted-controlplane.js`

### 3. SSE/log streaming still exists beside relay-first guidance
The API client still exposes EventSource-based live log streaming. That may be legitimate for logs, but it increases transport complexity and can easily be confused with the deprecated “shared state over SSE” model.

**Evidence**
- `web/src/lib/api/client.js`
- `docs/control-planes.md`
- `internal/api/router/router.go`

### 4. Soul provisioning uses timeout-based completion heuristics
The repo-wide Nostr guidance strongly discourages timeout-based completion logic for event flows. Soul provisioning tracking still appears to contain timeout-based failure behavior.

**Evidence**
- `web/src/lib/stores/souls.svelte.js`
- `docs/control-planes.md`
- `AGENTS.md`

### 5. Documentation drift is now a product risk, not just a docs issue
Auth, transport, and control-plane docs disagree in material ways. That makes PSTF acceptance work more expensive because intent is not singular.

**Evidence**
- `web/README.md`
- `README.md`
- `docs/architecture.md`
- `docs/protocol-compatibility.md`
- `docs/control-planes.md`

### 6. CI coverage may overstate confidence in backend behavior
Because GitHub Actions only run web tests, passing CI does not currently prove backend/router/control-plane regressions have been caught.

**Evidence**
- `.github/workflows/web-playwright-e2e.yml`
- `.github/workflows/web-vitest-unit.yml`
- `internal/**/*_test.go`

---

## Suggested first feature slice

### Encrypted control plane: notifications + transport contract

**Why first**
- Strongest alignment across docs, backend implementation, frontend implementation, and tests
- Bounded scope
- Exercises system discovery, auth/signing, relay separation, encrypted envelopes, correlation, and error handling
- High leverage for proving the repo's Nostr-native architecture correctly

**Primary evidence**
- `docs/control-planes.md`
- `internal/api/handlers/system.go`
- `internal/controlplane/encrypted_transport.go`
- `internal/controlplane/notification_encrypted_handlers.go`
- `web/src/lib/nostr/encrypted-controlplane.js`
- `web/src/lib/stores/notifications.svelte.js`
- `web/tests/unit/encrypted-controlplane.test.js`
- `web/tests/unit/notifications-store.test.js`
- `web/tests/e2e/notifications-encrypted-smoke.spec.js`

**Suggested acceptance concerns**
1. `/api/v1/system/info` clearly advertises encrypted-request capability and relay separation.
2. Sensitive request payloads are published only to encrypted-request relays, never public sidecar/browser relays.
3. Publish rejection (`OK false`) and encrypted terminal errors are surfaced to the caller.
4. Correlation by request event id and requester pubkey is explicit and testable.
5. Browser and backend agree on the supported notification operation catalog.
6. The slice proves event-driven completion rather than timeout/polling semantics.
