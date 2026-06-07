# Verification Report — bahia-8epx.7

## Scope

Beads epic `bahia-8epx.7`: NIP-42 AUTH and relay-unavailable handling across browser, backend, operator, and FIPS-adjacent relay clients.

NIP-86 relay administration is intentionally out of scope.

## Implementation Evidence

- Browser query lifecycle now marks AUTH-required relay closures as `excluded`, succeeds when remaining relays reach `EOSE`, and fails with `all_relays_excluded` when no relay remains.
- Browser subscription callbacks still surface relay AUTH via `onAuth` and now mark relay status as `auth-required`.
- Backend publisher-created pools receive `WithPrivateKey(cfg.PrivateKey)`, enabling existing NIP-42 AUTH retry behavior when credentials are configured.
- Backend relay health snapshots expose `LastError`; subscribers/FIPS subscribers record `auth-unavailable` metadata when AUTH is required but no usable key exists.
- Operator ContextVM client publishes through per-relay result metadata, preserves OK=false reasons, and consumes `CLOSED` while waiting for result events.
- FIPS bridge records AUTH-unavailable metadata when its pool supports Bahia relay health recording.

## Verification Commands

- `go test ./internal/adapters/nostr ./pkg/client ./cmd/cli ./internal/fipsbridge`
  - Result: pass.
  - Note: the first sandboxed run failed because Go could not write to `/Users/bizarro/Library/Caches/go-build`; the same command passed after rerunning with approved normal Go build-cache access.
- `npm run test:unit -- --run tests/unit/nostr-client-parsing.test.js tests/unit/discovery-store.test.js tests/unit/controlplane-requests.test.js tests/unit/encrypted-controlplane.test.js`
  - Result: pass (`4` files, `79` tests).

## Review Notes

- Oracle review found two issues before closeout:
  - Operator result subscriptions needed to attempt NIP-42 AUTH on `CLOSED auth-required` when the operator signer is available, then resubscribe without republishing the ContextVM request.
  - AUTH reason detection needed exact/prefix matching rather than substring matching.
- Both issues were fixed and covered by targeted tests before this report was updated.

## Remaining Gaps

No NIP-86/client-admin implementation was touched. Any optional relay administration work remains under `bahia-8epx.10`, not this slice.
