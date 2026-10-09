# Architecture ratchets

Bahia's architecture invariants are enforced by tests that compare the current
tree against a checked-in baseline of pre-existing violations. A gate fails
only when a file gains violations beyond its baseline, so recorded debt can
shrink but never grow. **Fix the violation; never grow a baseline by hand.**

Run all gates with `make lint-arch`. They also run as part of
`go test ./...` and `pnpm run test:unit`, and `make lint` runs them before
`golangci-lint`.

## Go gates (`internal/archtest`, `internal/app`)

| Test | What it bans | Baseline |
|---|---|---|
| `TestNoNewLegacyKindUsage` | References to retired per-family kind constants, or their literals, outside `internal/nostrmigration`. Values typed `kinds.CPStateFamily` are the sanctioned discriminator contract and are exempt | `testdata/legacy_kinds.baseline` |
| `TestNoNewDirectLibraryRelaySubscriptions` | `Subscribe*`/`FetchMany`/`QuerySingle` on `fiatjaf.com/nostr` `Relay`/`Pool` values outside the relay pool (`internal/adapters/nostr`) and the SoulFactory bus | `testdata/relay_subscribe.baseline` |
| `TestNoNewUnannotatedPollTickers` | `time.NewTicker`/`time.Tick` in `internal/service`, `internal/reconcile` and `internal/app` without a `//nostr:allow-poll <reason>` annotation on the same or preceding line (reconnect backoff, heartbeats, housekeeping are the legitimate reasons) | `testdata/poll_tickers.baseline` |
| `TestNoNewTestOnlyExports` | Exported `internal/` symbols whose only callers are tests (methods matching an interface method are skipped) | `testdata/unwired_exports.baseline` |
| `TestNoNewMultiLetterREQFilters` | `"schema"`/`"domain"` keys in `nostr.TagMap` REQ filters — relays index single-letter tags only; use a `t` topic | `testdata/multi_letter_req_filter.baseline` |
| `TestArchitectureContextVMMethodConstants` | Any `ContextVMMethod*` constant other than `services/secrets-reveal` and `deployments/run-logs-get`, or registration outside the assistant handlers, encrypted route handlers, encrypted transport and DNS-agent fallback. Mutations are intents | none (closed set) |
| `TestLegacyKindConstClassification` | Drift between the kind catalog and the legacy classification used by the legacy-kind gate | none |
| `TestArchitectureDBLessDaemonBootGatesNilRepositoryRoutes` (`internal/app`) | A daemon with an unreachable database must boot, and every database-backed route must sit behind the DB gate | none |
| `TestNoAutomaticSQLToCanonicalPromotion` | Daemon construction, database recovery and managed-instance runner startup cannot call SQL-to-canonical backfills, import SQL outbox rows, or wire PG-backed reconciliation/queue runners that publish signed state. The constructor checks identify current PostgreSQL-backed argument names; the same abstractions may be fed canonical local/relay views instead. See the [source audit](../analysis/postgres-startup-source-audit.md) | none (zero violations required) |

`internal/kinds` adds the contract tests that keep the kind catalog, topics,
coordinates and the generated web constants (`kinds.gen.js`) in step, and
`TestCPStateFamilyDiscriminatorsAreUnique`.

## Web gates (`web/tests/unit/architecture-gates.test.js`)

- Application source never uses the Wheelhouse relay defaults or hardcoded
  public relay hosts (hosts ending in `example`/`example.com` are allowed).
- `SimplePool` and `PoolBackedClient` stay out of `web/src/lib` — the welshman
  pool is the only pool.
- Stores gain no new `setInterval` polling or daemon REST client imports
  (`$lib/api/client.js`). Baseline:
  `web/tests/unit/architecture-gates.baseline.json`.

## Extending a gate or regenerating baselines

- To add a rule, add a test next to the existing ones that collects
  `violations` keyed per file and calls `ratchet(t, "<name>", found)`; a new
  gate starts from the baseline it writes on first regeneration.
- `make arch-baseline` regenerates every Go and web baseline
  (`ARCHTEST_UPDATE_BASELINE=1`). It prints a `BASELINE SUMMARY` per gate: `+`
  lines are new or grown debt and need a stated reason in the commit, `-` lines
  were paid down. Run it only after removing violations or on an integration
  branch, and review the diff of `internal/archtest/testdata` and
  `web/tests/unit/architecture-gates.baseline.json`.

## CPStateFamily allocation

Record families inside the `30900` envelope are discriminated by the
`legacy_kind` tag, whose values are the `kinds.CPStateFamily` constants in
`internal/kinds/cp_state_family.go`. They are **never a wire kind** to publish
or subscribe on. To add a family:

1. Take the next unused number in the `32000+` discriminator range (the
   current high-water mark is in `cp_state_family.go`; the doc comment of each
   constant names its neighbours). `TestCPStateFamilyDiscriminatorsAreUnique`
   parses the file and fails if two constants share a value — parallel slices
   have picked the same next number independently more than once, so run
   `go test ./internal/kinds/` before pushing.
2. Declare the constant with a doc comment stating the family, its `d`
   coordinate prefix (`<entity>:<id>` — every family addresses its own
   prefix so two families never replace each other on the relay), its `t`
   topic (add it to `internal/kinds/tags.go`) and whether the content is
   plaintext or OCK-encrypted.
3. Register the family in `cpStateFamilies` (`internal/adapters/nostr/projector.go`)
   so it is covered by warm-start and dedupe, and mirror the constant, topic
   and coordinate prefix in `web/src/lib/nostr/kinds.gen.js` (maintained by
   hand; the `internal/kinds` drift tests fail until Go and JS agree).
4. Never reuse a retired catalog kind's number for a new family, and never
   introduce a new wire kind for state that fits the `30900` envelope.
