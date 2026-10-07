# Agent instructions for Bahia

Bahia is a **Nostr-native** orchestration control plane: relays hold canonical
state as signed events, every mutation is a signed intent, and any database is
a derived, rebuildable index. Agents working here establish what should be
true, implement it with Nostr event semantics, prove it with deterministic
tests, track remaining work in Beads, and leave the branch pushed.

Production-ready means: no fake implementations, stubs, placeholder logic,
hidden mocks, hardcoded production-path values or TODO-shaped traps; verified
Nostr protocol behaviour; tests mapped to acceptance criteria; remaining work
captured in `bd`, not in comments, prose or markdown lists.

## Architecture

The normative charter is `docs/architecture.md` ("Charter: relay-canonical
Bahia"). The contracts agents need most are in `docs/architecture/`
([index](docs/architecture/README.md)); event kinds and shapes are in
`docs/nostr-event-implementation-guide.md` (the Bahia authority before
introducing or reusing any kind), `docs/event-spec.md`, `docs/control-planes.md`
and `docs/nostr-commands.md`. Code wins over any document; if they disagree,
fix the document (or report the code if it looks wrong — do not change
behaviour to match prose).

- **Canonical vs derived.** Canonical state is kind `30900` cp-state records
  signed by the service key and held on relays and in the daemon's local
  bbolt event store. Postgres is optional and derived: it is written after
  the canonical event and no path may require it. The daemon boots and
  serves relay state without a database.
- **Intents vs reads.** Mutations are operator-signed `30900` intents (NIP-59
  gift-wrapped for org, secret and notification domains) processed by
  `IntentProcessor`; outcomes are `30315` status events. Reads are REQ
  subscriptions against addressable state into a local store (daemon, CLI,
  MCP, web). See `docs/architecture/intents-and-authority.md`.
- **ContextVM** (kind `25910`, wrapped in `1059`/`21059`) is interactive
  RPC only: assistant turns, `services/secrets-reveal`,
  `deployments/run-logs-get`. It is never a read path and never gains new
  mutation methods (`TestArchitectureContextVMMethodConstants` fails the
  build if you add one). A ContextVM response is an acknowledgment; durable
  truth comes from subscriptions to the canonical observables.
- **Confidential state** (orgs, members, secrets metadata, notification
  channels, payments, security, operator allowlists) is OCK-encrypted per
  NIP-CAS-0011; `docs/architecture/confidential-state.md`.
- **Entity ids** are author-minted UUIDv7, fixed at creation;
  `docs/architecture/entity-identity.md`.
- **Outbox.** Every daemon publish goes through the local outbox first; the
  abandonment contract (undelivered markers, `canonical_delivery` health,
  `bahia_outbox_retry`) is `docs/architecture/outbox-delivery.md`.
- **Relay sidecar** read auth defaults to NIP-42 `enforce`; the web is
  store-first over relays with the bootstrap seed injected at container start
  (`PUBLIC_BAHIA_*`).
- **HTTP** on the daemon is limited to health/readiness/metrics, MCP, logs,
  DB-less payment reads, blob proxy and a few HTTP-native routes
  (`docs/architecture/cli-and-mcp.md`). Do not add REST read or write routes.

If code is "waiting and checking" instead of "subscribing and reacting", it
is wrong.

## Nostr rules

Nostr is an event stream, not a request/response API. Use `REQ` subscriptions,
`EVENT` handlers, `EOSE` for catch-up completion, `OK` for publish
verification, and `CLOSED`/`AUTH` handling. Do not build polling loops, inbox
polling, queues, ad hoc RPC over relays, timeout-based completion, or
request/response wrappers over relays.

**Subscriptions.** Filters are narrow: `kinds`, `authors`, `#p`, `#t`, `#d`,
`since`/`until`/`limit`. Relays index single-letter tags only, so never filter
on `#domain` or `#schema` (ratchet). Do not subscribe broadly and filter
locally unless justified in a comment. Subscriptions go through the relay pool
(`internal/adapters/nostr`) or the SoulFactory bus — never directly on
`fiatjaf.com/nostr` `Relay`/`Pool` values (ratchet).

**Backfill and realtime.** Subscribe with a persisted cursor, process stored
events, treat EOSE as catch-up complete, keep the subscription open. Never
sleep to wait for history.

**Publishing.** Every publish verifies `["OK", id, accepted, message]` —
both the flag and the message (`auth-required:`, `rate-limited:`,
`blocked:`, `invalid:`). Batch publishes collect every OK and handle partial
failure; there is no batch atomicity. Daemon publishes use the outbox.

**Relay management.** Query NIP-11 before assuming capabilities, support
NIP-42 AUTH, use NIP-65 relay lists where available, reconnect with
exponential backoff and re-issue subscriptions, track per-relay health, CLOSE
unused subscriptions and close cleanly on shutdown. Timers are allowed for
reconnect backoff, heartbeats/health checks, autoscaling and outbound rate
limiting — never for event delivery, relay response waiting or completion
detection. A ticker in `internal/service`, `internal/reconcile` or
`internal/app` needs `//nostr:allow-poll <reason>` (ratchet).

**Event correctness.** Validate before trust: id equals the NIP-01 hash,
Schnorr signature verifies for `pubkey`, timestamp is reasonable, required
tags exist, content is well-formed. Handlers are idempotent: dedupe by event
id (and `intent_id` for intents), correlate with `#t`/`#d`, and resolve
replaceable and addressable events per `docs/architecture/event-lifecycle.md`
(latest `created_at`, lowest id on ties; NIP-09; NIP-40).

## Architecture ratchets

`make lint-arch` runs the gates; they also run under `go test ./...` and
`pnpm run test:unit`, and every gate compares against a checked-in baseline
that may only shrink. **Fix the violation; never grow a baseline by hand.**
`make arch-baseline` regenerates baselines after violations are removed and
prints a per-gate `BASELINE SUMMARY` to review. The gates, how to add one, and
the `CPStateFamily` allocation rule (next unused `32xxx` discriminator, doc
comment, own `d` prefix and topic, registered in `cpStateFamilies`, mirrored in
`kinds.gen.js`; `TestCPStateFamilyDiscriminatorsAreUnique` rejects duplicates)
are in `docs/architecture/ratchets.md`.

## Forbidden smells

Flag and fix during implementation and review:

- Nostr: sleeps/`setTimeout`/`setInterval`/retry loops to wait for events;
  short-lived subscriptions used to peek; timeout-based "done"; ignored
  `OK`/`CLOSED`/`AUTH`; weak filters; queue/inbox/RPC abstractions over
  relays; non-idempotent handlers; assuming every relay supports the same
  NIPs; hardcoded relay lists or public relay hosts.
- Production readiness: `TODO`/`FIXME`/`XXX`/`HACK`/`TEMP`/`WIP`; "for now",
  "later", "MVP", "simplified"; stubs, mocks, fakes, dummy data or placeholder
  adapters in production paths; hardcoded ids, URLs, ports, tenants, keys,
  paths or magic constants; test-only logic in production (ratchet:
  `TestNoNewTestOnlyExports`); silent fallbacks; swallowed errors; no-op
  handlers; dead compatibility branches; partial integrations; tests that
  ratify fake behaviour.

Leave none of these behind unless they are out of scope, unreachable from
production paths, and tracked in Beads.

## Workflow

### Beads

This project uses `bd` for all task tracking. Run `bd prime` at session start
(and after compaction) for the full command reference and close protocol.

```bash
bd ready                 # available work
bd show <id>             # details and dependencies
bd update <id> --claim   # claim before changing code
bd close <id>            # done, with verification evidence
bd remember "<insight>"  # persistent knowledge (no MEMORY.md files)
```

Use Beads instead of TodoWrite, TaskCreate, markdown TODO lists or MEMORY.md.
Create or update issues for defects, incomplete implementation,
production-readiness gaps, protocol violations, weak or fake tests, product
ambiguities and follow-up work. A good issue has a concrete title, affected
files, observed and intended behaviour, acceptance criteria, priority,
dependencies and blocked/ready state. `.beads/issues.jsonl` is committed with
the code.

### Branches and worktrees

Work on a branch in a git worktree, not on `master`:
`git worktree add ../bahia-worktrees/<name> -b <branch>`. When an orchestrator
assigns you a worktree, stay in it, commit there, and do not merge, rebase or
push other branches; the orchestrator integrates and runs the full gate.
Otherwise push your branch and report the commit hashes.

### Implementation standard

Before changing code: run `bd prime`, inspect the relevant code and tests,
read the contract in `docs/architecture/` (and the feature's PSTF record if
one exists), and claim or create the Beads issue.

While changing code: preserve event-driven semantics; remove fake or
placeholder behaviour in touched paths; implement vertical slices end to end;
validate inbound events; verify publish outcomes; handle relay failures
explicitly; add deterministic tests.

After changing code: run the relevant tests, linters and builds; update the
documentation listed below; update PSTF artifacts when a feature record
exists; update or close Beads issues and file new ones for remaining work.

Escalate to a human — in the issue and, where a record exists, in
`hitl_decisions.md` — when docs contradict code, behaviour looks accidental,
acceptance criteria need product judgment, UX expectations are subjective, or
security, privacy, billing, permissions, destructive data or broad
architecture changes are involved. Do not silently resolve product ambiguity.

### Testing rules

Tests are deterministic and event-driven: inject `EVENT`, `EOSE`, `OK`,
`CLOSED` and `AUTH` messages; assert on handlers and state transitions; test
rejection, reconnect and dedupe paths; map tests to acceptance criteria. Do
not sleep for async behaviour, assert only that a mock was called, skip hard
cases without a Beads issue, or keep tests that encode placeholder behaviour.
Passing tests are not enough; they must prove the intended behaviour.

## PSTF feature records

`pstf/features/<FEATURE_ID>/` holds per-feature specification and
verification records (`feature_spec.json`, `acceptance_criteria.json`,
`test_matrix.json`, `defects.json`, `verification_report.md`,
`hitl_decisions.md`); `pstf/README.md` describes the layout. They are
engineering records, not product documentation. Rules when you touch one:
ground claims in repository evidence, separate observed from intended
behaviour, give every acceptance criterion a test and every failing test a
defect, and never mark a feature verified without evidence. Two records are
consumed by tooling and must stay valid:
`pstf/features/BAHIA_NOSTR_AUDIT_PARITY/route_transport_matrix.json`
(`web/tests/unit/route-transport-matrix.test.js`) and the coverage output
directories written by `make pstf-soulfactory-coverage`,
`web/vitest.coverage.*.config.js` and `test/integration/run-hf-vllm-verify.sh`.

## Build, test and lint

```bash
make build                      # all binaries into bin/ (server, cli, relay, sidecars, agents)
make run-dev                    # go run ./cmd/server -config config.yaml
CGO_ENABLED=0 go build ./...
go test ./... -count=1          # make test (verbose); make test-short, make race (CGO)
make lint                       # make lint-arch + golangci-lint run ./...
make lint-arch                  # Go gates (internal/archtest, internal/app) + web gates
make arch-baseline              # regenerate ratchet baselines after paying down debt
make fmt                        # gofmt + goimports (skips third_party/)
make migrate                    # go run ./cmd/bahia-migrate --config config.yaml up
cd web && pnpm install && pnpm run test:unit && pnpm run lint   # vitest + svelte-check
cd web && pnpm run test:e2e     # Playwright (mock relays, signed fixtures)
cd test/e2e-agent && npm install && npm run typecheck           # agent harness; see its README
```

`go test ./...` skips `third_party/` (a separate module). The Go gate used
for documentation changes is
`CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./internal/docs/ ./internal/archtest/ ./internal/pipeline/`.

## Documentation maintenance

User documentation lives in `docs/user-guide/` and is published to relays by
`internal/docs` (and served by the web `docs` route); the same pages serve
humans and agents. Architecture contracts live in `docs/architecture/`.
Undocumented features are incomplete features.

| Changed area | Update |
|---|---|
| A feature (services, environments, deployments, artifacts, builds, notifications, organizations, LLM routes, ML models, souls, workers, backup, DNS, packages, policies, payments, security, continuity, adoption, ...) | `docs/user-guide/features/<feature>.md`; a new feature area gets a new page linked from `docs/user-guide/index.md` |
| MCP tool | `docs/user-guide/mcp-tools.md` |
| CLI command or flag | `docs/user-guide/cli-reference.md` |
| Nostr kind, tag or payload | `docs/nostr-event-implementation-guide.md`, `docs/event-spec.md`, `docs/control-planes.md`, `docs/nostr-commands.md`, `docs/protocol-compatibility.md`, `docs/user-guide/nostr-integration.md` |
| Intent pipeline, trust, outbox, OCK, readiness, ratchets, web store, CLI/MCP transport | the matching page in `docs/architecture/` |
| Config keys or defaults | `docs/user-guide/getting-started.md` and the config reference the key belongs to |
| Operator procedures | `docs/runbooks/` |

Write in the present tense about the current product; do not narrate
history, phases or migrations in documentation or code comments. Include code
examples for CLI, MCP and Nostr where relevant.

## Review checklist

Before calling work complete:

- Nostr: no polling for delivery; no timeout-based completion; scoped
  filters; EOSE handled; OK checked for flag and message; CLOSED and AUTH
  handled; events validated; idempotent handlers; replaceable semantics
  respected; backoff and re-subscribe on reconnect; subscriptions cleaned up;
  NIP-11 checked; no queue/RPC abstraction over relays; kinds and ContextVM
  use follow `docs/nostr-event-implementation-guide.md`.
- Architecture: intents for mutations, subscriptions for reads, Postgres
  derived and optional, `make lint-arch` green with no baseline growth.
- Production readiness: no stubs, mocks, fakes, placeholders, TODOs or
  hardcoded production values in touched paths; explicit error handling;
  configuration externalized; integrations real or tracked as blocked.
- Records: documentation updated; PSTF record updated where one exists;
  remaining work in Beads.

## Session completion

Work is not complete until it is committed and pushed (or, in an assigned
worktree, committed and reported to the orchestrator):

1. Create Beads issues for remaining work.
2. Run the quality gates for what changed.
3. Update PSTF artifacts where a record exists.
4. Update Beads issue status.
5. Commit; then `git pull --rebase && git push && git status` — the branch
   must be up to date with its remote. If push fails, resolve and retry.

Never say "ready to push when you are", "left as future work", "good enough
for now" or "in a real system…". The handoff lists the Beads issues worked,
code changed, tests run, PSTF artifacts updated, remaining issues, blockers,
and a statement that no fake, stubbed, hardcoded or placeholder
production-path behaviour remains in the touched scope.

## Enforcement

These instructions are architectural constraints, not preferences.
Violations are bugs: fix them if in scope, otherwise file or update a Beads
issue, record product ambiguity in the PSTF record or issue, and never bury
the problem in comments or handoff prose.
