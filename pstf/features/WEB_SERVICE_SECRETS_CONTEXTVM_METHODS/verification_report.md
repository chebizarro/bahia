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

## Conclusion

The services route should no longer receive `method not found` for service secret ContextVM requests emitted by the current web client. No REST fallback or fake secret behavior was added.
