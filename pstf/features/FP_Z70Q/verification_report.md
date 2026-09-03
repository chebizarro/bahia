# FP-Z70Q verification report

Verified 2026-09-02 on go1.26.3/darwin-arm64.

## Resolution

Bahia's developer and CI race gates now run:

```sh
CGO_ENABLED=1 go test -race -gcflags=fiatjaf.com/nostr=-d=checkptr=0 ./... -count=1
```

The exact package pattern exempts only the unsafe JSON serializer compiled in the `fiatjaf.com/nostr` root package. Imports through subpackages such as `keyer`, `nip44`, `nip46`, and `nip59` still reach that root serializer without requiring a broader `/...` pattern. Delete the flag once an adopted upstream revision carries the pointer-provenance fix; do not replace it with `-gcflags=all=-d=checkptr=0`.

## Evidence

- `make race` — PASS across `./...`.
- `go build ./...` — PASS.
- `go vet ./...` — PASS.
- `go test ./... -count=1` — PASS.
- The first race activation exposed and fixed test-only synchronization defects: an under-sized bootstrap timeout, outbox assertions that ran before persistence, async reactor assertions without completion signals, and an in-memory state repository that aliased mutable pointers without locking.

The fleet-level planted first-party checkptr probe was run in cascadia-go with the same scoped flag and failed at the intentional first-party `unsafe.Pointer(base + 1)` conversion, confirming the exemption does not disable checkptr in consuming-module code.
