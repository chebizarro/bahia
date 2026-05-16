# Bucket 5 Verification Report

## Scope
- Bead `bahia-hjlr`: Blossom auth-header failures are explicit for read/proxy paths.
- Backend portion of `bahia-il2j`: deterministic cleanup for listed backend sleep-based tests.

## Acceptance Criteria Mapping
- Blossom auth generation failures are not swallowed: covered by `TestClient_DownloadAuthHeaderFailureDoesNotFallbackUnauthenticated` and `TestClient_ProxyAuthHeaderFailureDoesNotFallbackUnauthenticated`.
- HTTP Blossom list maps auth/config failures explicitly: `ListBlobs` maps `blossom.ErrAuthHeader` and local auth config errors to HTTP 503.
- Backend sleep-based tests replaced: `events_async_test.go` uses channel handshakes; `ratelimit_test.go` uses an injected fake clock and direct cleanup invocation.

## Verification
Command run:

```sh
go test ./internal/adapters/blossom ./internal/api/handlers ./internal/events ./internal/api/middleware
```

Result: touched packages reported `ok`, but the overall command exited non-zero because a concurrent/pre-existing compile issue in `internal/adapters/nostr/relay_pool.go` was reported by vet while loading dependencies:

```text
cannot use subs (variable of type []relaySubscription) as []*github.com/nbd-wtf/go-nostr.Subscription value in argument to mergeSubscriptions
```

This bucket did not edit `internal/adapters/nostr`.
