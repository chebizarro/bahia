# BAHIA_ROUTE_CANARY_POLICY_OVERRIDES: Verification Report

Task: `bahia-canary-policy-overrides-regex`
Beads: `bahia-6xztt` (per-route overrides), `bahia-j9liz` (regex body assertions)
Branch: `task/bahia-canary-policy-overrides-regex` (based on origin/master 2520343a)

## Scope of verification

This report covers implementation-level verification in the repository: unit, config, adapter and behavioral tests, plus full-repository build, vet and test runs. **It does not claim live acceptance.** No probe was run against production hosts and no fleet configuration was changed.

## Result summary

| AC | Beads | Statement | Result |
|---|---|---|---|
| AC1 | 6xztt | Route overrides expected status, body, probe timeout and TLS window without affecting other routes | PASS |
| AC2 | 6xztt | Route overrides its periodic probe interval; supervisor probes each route on its own cadence | PASS |
| AC3 | 6xztt | Overrides apply to the post-deploy gate | PASS |
| AC4 | 6xztt | Overrides are repo-configured control-plane policy keyed by hostname, not signed state | PASS |
| AC5 | 6xztt | Enabled-but-unusable override configuration fails at startup | PASS |
| AC6 | j9liz | Regex body assertion alongside substring, with pinned whole-body anchoring | PASS |
| AC7 | j9liz | Regex bounded in length and program size, validated at config load | PASS |
| AC8 | j9liz | Regex matches the raw bounded body; reuses `body_mismatch`, no migration | PASS |

## Evidence

Full repository gates, run on the branch after the final code change:

- `go build ./...`: exit 0
- `go vet ./...`: exit 0
- `GOFLAGS=-p=2 go test ./... -count=1`: exit 0, 76 packages ok, no failures

New targeted suites:

- `internal/domain/route_canary_policy_overrides_test.go`: 13 test functions. They cover anchoring, unsafe-pattern rejection (length, program size, non-RE2 constructs, anchor escape via `a)|(b`, unterminated `\Q`, empty-matching patterns, invalid UTF-8), linear-time matching, conjunction of both body forms, classification reuse, catch-all suppression, override derivation and isolation, inheritance, interval lookup, and override validation.
- `internal/config/route_canary_test.go`: 6 test functions. They cover YAML decoding with dotted hostnames, explicit empty and zero values, hostname canonicalization, unknown-key rejection with a hint, malformed entries, and fail-closed validation.
- `internal/adapters/runtime/route_probe_regex_test.go`: 4 test functions against a real `httptest` server. They cover JSON field assertion in any key order, raw-versus-sanitized matching, the bounded-body limit, and invalid-regex target rejection.
- `internal/service/route_canary_overrides_test.go`: 10 test functions. They cover per-route cadence, early-wake tolerance, forced sweeps, back-off on enumeration failure, immediate re-sweep after an overrun, the wake cap, re-added routes, backwards clock steps, status override isolation in the supervisor, and the gate applying overrides.

The pre-existing route canary suites pass unmodified, including `internal/db/route_canary_constraint_conformance_test.go`. No classification was added and no migration was written.

## Notable verification decisions

**No sleep-based waiting.** Scheduling tests inject the supervisor clock and call `EvaluateDue`, `EvaluateOnce` and `NextWakeDelay` directly. `Run` is never started.

**Real bytes for body assertions.** The service tests script `BodyMatched` so that verdicts isolate status policy. Regex matching itself is proven in the adapter tests against real HTTP responses.

**Config shape verified before committing to it.** Koanf uses `.` as its key delimiter, so a probe confirmed that hostname-keyed maps survive decoding intact and that pointer fields keep an explicit `""` or `0` distinct from an unset field.

## Production-readiness statement

No stubs, placeholder adapters, TODO markers or hardcoded production values were introduced in production code. Every configuration field added is consumed on a production path: overrides and the regex flow through `RouteCanaryConfig.Policy()` into the evaluator that `internal/app/app.go` already builds for both the supervisor and the gate. `app.go` needed no change.
