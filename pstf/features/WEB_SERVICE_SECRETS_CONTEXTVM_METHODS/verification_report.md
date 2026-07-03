# WEB_SERVICE_SECRETS_CONTEXTVM_METHODS Verification Report

## Beads

- Issue: `bahia-9m1d` — Fix services secrets ContextVM method-not-found on service route

## Root Cause

The web service-secrets store used dotted operation identifiers such as `services.secrets.list`. `buildContextVMRequest()` converts dotted operations into ContextVM slash/hyphen method names, so the actual Nostr request method was `services/secrets-list`. The backend route handler registered only the dotted name as a ContextVM handler, which caused JSON-RPC `-32601 method not found` responses.

A second compatibility issue was found during inspection: real ContextVM handler responses return the result object directly, while older encrypted route responses used `{status, payload}`. The web store only returned `payload`, dropping direct results.

## Changes

- Registered slash/hyphen ContextVM aliases for service secret list/create/update/delete/reveal handlers while retaining dotted encrypted-operation registrations.
- Updated backend tests to exercise `services/secrets-list` and `services/secrets-reveal` ContextVM aliases.
- Updated the web service-secrets result unwrap to return direct result payloads or legacy `payload` envelopes.
- Updated web tests to cover both direct result and legacy envelope shapes.

## Verification

Command:

```sh
go test ./internal/controlplane
```

Result:

- PASS: `github.com/openagentsinc/bahia/internal/controlplane`

Command:

```sh
cd web && pnpm exec vitest run --config vitest.config.js tests/unit/encrypted-route-stores.test.js
```

Result:

- PASS: 1 file, 5 tests.

Command:

```sh
cd web && pnpm lint
```

Result:

- PASS: `svelte-check found 0 errors and 0 warnings`.

## bahia-rxae relay-backed E2E fixture restoration — 2026-07-03

- Updated `web/tests/e2e/service-secrets-smoke.spec.js` to seed canonical relay-backed `30900` service/build/artifact projections instead of REST service/build/artifact routes.
- Added deterministic E2E operation queue support in `web/tests/e2e/helpers.js` for opaque NIP-44 gift-wrapped service-secret ContextVM requests. The queue drives list/create/reveal/update/delete fixture responses from relay-published requests without sleep waits and without rendering plaintext secret values except inside the explicit reveal dialog.
- Discovery fixture now advertises `features.encrypted_nostr_requests` and `contextvm_relays`, matching the current web client gate for encrypted service-secret operations.

Targeted gate:

```sh
cd web && npm run test:e2e -- sbom-workflow.spec.js service-secrets-smoke.spec.js
```

Result:

- PASS on 2026-07-03: 12 passed.

## Conclusion

The services route should no longer receive `method not found` for service secret ContextVM requests emitted by the current web client. No REST fallback or fake secret behavior was added.
