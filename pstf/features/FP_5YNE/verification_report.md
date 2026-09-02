# FP-5yne Maintenance NIP-59 Verification

Policy source: closed `fp-3eme`; implementation issue: `fp-5yne`.

## Scope decision

Bahia's writer change is maintenance-scoped. Widening all ContextVM publishers would alter unrelated peer compatibility without evidence that those legs are ready. Cascadia's response rule is transport-generic: any wrapped request is answered in the same format, because plaintext fallback would violate the wrapper boundary independent of method.

## Compatibility decision

The formats are not wire-compatible, so rollout is reader-first rather than a flag day:

1. Deploy Cascadia workers with dual-read enabled (`require_nip59=false`). Plaintext requests still receive plaintext responses, legacy wrapped requests receive legacy wrapped responses, and NIP-59 requests receive NIP-59 responses; no wrapped request falls back to plaintext.
2. Deploy Bahia, which always writes conformant NIP-59 for `maintenance/*`.
3. After all Bahia writers are upgraded, set `require_nip59=true` on maintenance profiles. The worker then rejects plaintext and legacy-direct requests before driver dispatch. Remove dual-read only after legacy traffic is confirmed absent.

Deploying the writer before the reader is not supported: the old worker decoder cannot parse the NIP-59 seal. The ordered rollout prevents that mixed-version pairing without weakening the final policy.

The deployable worker pin for step 1 is Cascadia commit `5bc9efa` (or a later
descendant). No current tag contains that commit. If fleet deployment policy
requires a semver tag instead of an immutable source commit, cut and validate
`v1.3.1` or later at a descendant of `5bc9efa` before deploying the Bahia
writer. Bahia's Go-module pin to `cascadia-go v1.3.0` is separate and correct:
it supplies the client-side `WrapEventNIP59`/`UnwrapAny` helpers already present
in that release; Bahia does not obtain the `fleet-worker` binary through its Go
module graph.

NIP-59 randomizes the outer timestamp into the past. The updated worker uses a
bounded 12-hour outer-event lookback, then drops decrypted rumors older than
the subscription start before parsing, authorization, response publication, or
driver dispatch. This prevents the relay filter from losing newly published
wrappers without replaying historical commands.

Bahia's shared ContextVM ingress now gives kind 1059 a separate 12-hour outer
lookback, decrypts with `UnwrapAny`, validates conformant unsigned rumors at the
seal/rumor boundary, and retains its existing two-minute inner-event replay
window. Worker responses therefore reach JSON-RPC role dispatch. The response
role bug remains intentionally owned by `fp-5l35`, and the maintenance result
consumer remains intentionally owned by `fp-20aa`.

## Sibling channels

- `30315` never carries `Outcome.Result`. For maintenance it now uses the opaque rumor event id for request correlation, removes progress messages, and uses a stable failure string so path-bearing driver errors cannot leak.
- `4903` previously leaked paths through `Audit.Target`, even though `Audit.Correlation` derived from the JSON-RPC id/progress token rather than the result. Maintenance audits now omit `Target` and correlate with the opaque rumor event id.
- Each maintenance rumor includes a fresh high-entropy `privacy-nonce` tag that is hidden by NIP-59, so publishing the rumor id for status/audit correlation does not create a practical dictionary oracle for guessed path payloads.

## Verification

Run on 2026-09-02:

- `cascadia-go`: `make ci` — PASS (format, Go directive, tidy, build, vet, full tests, ContextVM conformance, Firecracker packaging).
- `cascadia-go`: `make race` — PASS with `checkptr` enabled, including worker, maintenance driver, and NIP-59 suites.
- `bahia`: `make build` — PASS for all six binaries.
- `bahia`: `go test ./... -count=1` — PASS on the final full rerun. An earlier run hit the existing concurrent-map fixture panic in `internal/service/mockStateRepo`; `go test ./internal/service -count=1` and `-count=10` both passed before the full rerun.
- `bahia`: `go test ./internal/controlplane -run TestMaintenanceCommandPublisher -count=1` — PASS.
- `bahia`: `go test -race ./internal/controlplane` — BLOCKED by the pinned `fiatjaf.com/nostr` `writeJSONString` unsafe-pointer provenance defect, tracked by `fp-z70q`. The failure occurs in the dependency's `event.go`, and was not suppressed.
- `bahia`: `make lint` — repository baseline remains red on 155 pre-existing findings. A package-scoped rerun after the ingress follow-up reports only the two pre-existing unused helpers in `encrypted_transport.go`; no new finding is introduced by FP-5YNE.
