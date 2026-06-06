## Agent Prime Directive
This repository is **Nostr-native** and uses **PSTF** plus **Beads** to turn product intent into verified, production-ready behavior.
Do not merely make code compile. Establish what should be true, implement it using Nostr-native event semantics, prove it with tests, track all remaining work in Beads, and leave the repo pushed and handoff-ready.
Production-ready means:
- no fake implementations, stubs, placeholder logic, hidden mocks, hardcoded production-path values, or TODO-shaped traps
- verified Nostr protocol behavior
- deterministic tests mapped to acceptance criteria
- remaining work captured in `bd`, not memory, comments, or markdown lists

---

## Non-Negotiable Architecture
Nostr is an event stream, not a request/response API.
Use:
- `REQ` subscriptions
- `EVENT` handlers
- `EOSE` for historical catch-up completion
- `OK` for publish verification
- `CLOSED` handling
- `AUTH` handling when required
Do not build:
- polling loops
- inbox polling
- Redis-style queues
- ad hoc RPC-over-Nostr
- timeout-based completion
- fake request/response wrappers over relays

Approved exception: ContextVM JSON-RPC kind `25910`, optionally wrapped with CEP-4/NIP-59 `1059` or `21059`, is Bahia's canonical mutation intent transport. A ContextVM response is only an acknowledgment; durable progress and terminal truth still come from scoped subscriptions to canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, and relevant standard NIPs).

For event-kind selection, event shapes, legacy-kind migration boundaries, and Cascadia fleet interoperability, follow `docs/nostr-event-implementation-guide.md`. Treat that guide as the Bahia-specific authority before introducing or reusing any Nostr kind.

If code is “waiting and checking” instead of “subscribing and reacting,” it is wrong.

---

## Required Nostr Patterns
### Subscriptions
Use scoped subscriptions with callbacks:
```python
await subscribe_filter(
    filters=[{"kinds": [1234], "#p": [agent_pubkey], "#t": [task_id]}],
    on_event=handle_event,
    on_eose=handle_eose,
    on_closed=handle_closed,
)

Filters must be narrow:

* kinds
* #p
* #agent
* #t
* #d for parameterized replaceable events
* since / until / limit where appropriate

Do not subscribe broadly and filter locally unless explicitly justified.

Backfill + Realtime

For historical data:

1. Subscribe with since / until / limit
2. Process stored EVENTs
3. Treat EOSE as historical catch-up complete
4. Keep subscription open for realtime events when needed

Never use sleeps to wait for history.

Publishing

Every publish must verify OK:

["OK", <event_id>, <accepted:bool>, <message>]

Check both:

* accepted flag
* message

Handle rejection reasons such as auth required, rate limits, invalid events, and relay errors.

Batch publishes must collect all OKs and handle partial failure. Do not assume batch atomicity.

Relay Management

Agents must:

* query NIP-11 relay metadata before assuming capabilities
* support NIP-42 AUTH when required
* use NIP-65 relay lists when available
* reconnect with exponential backoff
* re-issue subscriptions after reconnect
* track per-relay health
* close unused subscriptions with CLOSE
* cleanly close subscriptions on shutdown

Timers are allowed only for:

* reconnect backoff
* health checks / heartbeats
* autoscaling
* outbound rate limiting

Timers are not allowed for:

* event delivery
* relay response waiting
* completion detection

⸻

Event Correctness

Inbound events must be validated before trust:

* event ID matches NIP-01 serialized hash
* Schnorr signature is valid
* signature pubkey matches event.pubkey
* timestamp is reasonable
* required kind-specific tags exist
* content is well-formed, including JSON where expected

Handlers must be idempotent:

* dedupe by event.id
* use #t or other correlation tags for workflow tracking
* respect replaceable event semantics:
    * replaceable kinds: latest by pubkey
    * parameterized replaceable kinds: latest by pubkey + d tag

⸻

Forbidden Code Smells

Flag and fix these during implementation and review:

Nostr protocol smells

* sleep, setTimeout, setInterval, or retry loops used to wait for events
* repeated short-lived subscriptions used to “peek”
* timeout-based “done” logic
* ignored OK, CLOSED, or AUTH
* weak filters that download excessive relay history
* queue, inbox, or RPC abstractions over Nostr
* missing dedupe or non-idempotent event handlers
* assuming all relays support the same NIPs

Production-readiness smells

* TODO, FIXME, XXX, HACK, TEMP, WIP
* “for now”, “later”, “future work”, “MVP”, “simplified”, “minimal”
* stubs, mocks, fakes, dummy data, placeholder adapters
* hardcoded IDs, URLs, ports, tenants, keys, paths, relay lists, or magic constants in production paths
* test-only logic leaking into production
* silent fallbacks that hide missing behavior
* swallowed exceptions
* no-op handlers
* dead compatibility branches
* incomplete migrations
* partial integrations that stop one layer short
* tests that validate fake behavior instead of intended behavior

Do not leave these behind unless they are outside scope, unreachable from production paths, and tracked in Beads.

⸻

PSTF Workflow

Use PSTF to convert intent into verified behavior.

For each meaningful feature or fix, maintain artifacts under:

/pstf/features/<FEATURE_ID>/

Relevant files may include:

feature_spec.json
acceptance_criteria.json
test_matrix.json
defects.json
verification_report.md
hitl_decisions.md

Rules:

1. Ground claims in repository evidence.
2. Separate observed behavior from intended behavior.
3. Do not silently resolve product ambiguity.
4. Every feature needs testable acceptance criteria.
5. Every acceptance criterion maps to tests.
6. Every failing test maps to a defect or flawed test assumption.
7. Prefer narrow vertical slices over broad rewrites.
8. Keep patches minimal and explain tradeoffs.
9. Never mark complete without verification evidence.

Escalate for human decision when:

* docs contradict code
* behavior appears accidental
* acceptance criteria require product judgment
* UX expectations are subjective
* security, privacy, billing, permissions, destructive data, or broad architecture changes are involved

Record these in hitl_decisions.md and Beads.

⸻

Beads Issue Tracking

This project uses bd for all task tracking.

Run:

bd prime

Use Beads instead of:

* TodoWrite
* TaskCreate
* markdown TODO lists
* MEMORY.md

Quick commands:

bd ready
bd show <id>
bd update <id> --claim
bd close <id>
bd remember

Create or update Beads issues for:

* defects
* incomplete implementation
* production-readiness gaps
* protocol violations
* weak or fake tests
* PSTF ambiguities
* follow-up work

A good Beads issue includes:

* concrete title
* affected files/subsystems
* observed behavior
* intended behavior
* acceptance criteria or verification notes
* priority/severity
* dependencies
* whether it is blocked or ready

Do not hide remaining work in prose. Track it in bd.

⸻

Implementation Standard

Before changing code:

1. Run bd prime
2. Inspect relevant code and tests
3. Identify intended behavior via PSTF
4. Claim or create the relevant Beads issue

While changing code:

1. Preserve Nostr event-driven semantics
2. Remove fake or placeholder behavior in touched paths
3. Implement vertical slices end-to-end
4. Validate inbound events
5. Verify publish outcomes
6. Handle relay failures explicitly
7. Add deterministic tests without sleep-based waiting

After changing code:

1. Run relevant tests, linters, builds
2. Update PSTF verification artifacts
3. Update or close Beads issues
4. Create Beads issues for any remaining real work

Passing tests is not enough. Tests must prove the intended behavior, not merely ratify current fakery.

⸻

Testing Rules

Tests must be deterministic and event-driven.

Do:

* inject mock EVENT, EOSE, OK, CLOSED, and AUTH messages
* verify handlers and state transitions directly
* test rejection paths
* test reconnect and dedupe behavior
* map tests to acceptance criteria

Do not:

* sleep to wait for async behavior
* assert only that a mock was called while real behavior is absent
* skip hard cases without a Beads issue
* preserve tests that encode placeholder behavior as truth

Bad:

await asyncio.sleep(0.5)
assert len(received_events) > 0

Good:

mock_relay.inject_event(test_event)
assert handler.call_count == 1

⸻

Review Checklist

Before calling work complete, verify:

Nostr

* no polling for message delivery
* no timeout-based completion
* scoped filters
* correct EOSE handling
* OK checked for accepted flag and message
* CLOSED logged and handled
* AUTH supported where required
* event validation performed
* dedupe and idempotency implemented
* replaceable event semantics respected
* reconnect uses exponential backoff
* subscriptions are cleaned up
* relay capabilities checked with NIP-11
* no ad hoc queue/RPC abstraction replaces Nostr semantics; ContextVM use and event-kind selection follow `docs/nostr-event-implementation-guide.md`, `docs/control-planes.md`, `docs/nostr-commands.md`, and `docs/event-spec.md`

PSTF

* intended behavior is documented
* acceptance criteria exist
* tests map to acceptance criteria
* defects map to failing tests or observed gaps
* verification evidence exists

Production Readiness

* no stubs, mocks, fakes, placeholders, TODOs, or hardcoded production values in touched paths
* no silent fallbacks hiding missing behavior
* no test-only logic in production
* error handling is explicit
* configuration is externalized where appropriate
* integrations are real or clearly tracked as blocked
* remaining work is in Beads

⸻

Documentation Maintenance

User-facing documentation lives in `docs/user-guide/`. When you add or change app behavior, update the corresponding documentation.

Documentation scope:

| Changed Area | Documentation to Update |
|--------------|------------------------|
| Services | `docs/user-guide/features/services.md` |
| Environments | `docs/user-guide/features/environments.md` |
| Deployments | `docs/user-guide/features/deployments.md` |
| Artifacts | `docs/user-guide/features/artifacts.md` |
| Notifications | `docs/user-guide/features/notifications.md` |
| Organizations | `docs/user-guide/features/organizations.md` |
| LLM Routes | `docs/user-guide/features/llm-routes.md` |
| ML Models | `docs/user-guide/features/ml-models.md` |
| Souls/Soul Factory | `docs/user-guide/features/souls.md` |
| Workers | `docs/user-guide/features/workers.md` |
| Backup | `docs/user-guide/features/backup.md` |
| DNS | `docs/user-guide/features/dns.md` |
| Packages | `docs/user-guide/features/packages.md` |
| Policies | `docs/user-guide/features/policies.md` |
| Payments | `docs/user-guide/features/payments.md` |
| MCP tools | `docs/user-guide/mcp-tools.md` |
| CLI commands | `docs/user-guide/cli-reference.md` |
| Nostr events | `docs/nostr-event-implementation-guide.md`, `docs/user-guide/nostr-integration.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/protocol-compatibility.md` |
| Nostr control planes / migration app | `docs/nostr-event-implementation-guide.md`, `docs/control-planes.md`, `docs/user-guide/nostr-integration.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/verification_report.md` |
| Core concepts | `docs/user-guide/core-concepts.md` |
| Setup/config | `docs/user-guide/getting-started.md` |

Rules:

1. When adding a new MCP tool, document it in `docs/user-guide/mcp-tools.md`.
2. When adding a new CLI command, document it in `docs/user-guide/cli-reference.md`.
3. When changing Nostr event kinds or payloads, update `docs/nostr-event-implementation-guide.md`, `docs/user-guide/nostr-integration.md`, `docs/control-planes.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, and `docs/protocol-compatibility.md`.
4. When changing legacy-kind migration behavior, update `docs/nostr-event-implementation-guide.md`, the startup migration app documentation, and PSTF verification evidence; legacy kind support must remain migration-only unless a Bead explicitly reopens runtime compatibility.
5. When adding a new feature area, create a new file in `docs/user-guide/features/` and add it to the index in `docs/user-guide/index.md`.
6. When changing behavior described in existing docs, update those docs to match.
7. Documentation uses markdown. Include code examples for CLI, MCP, and Nostr where relevant.
8. Keep documentation accessible to both human users and AI agents — the same docs serve both audiences via web and MCP.

Documentation is part of the deliverable. Undocumented features are incomplete features.

⸻

Session Completion

Work is not complete until changes are committed and pushed.

Mandatory closeout:

1. Create Beads issues for remaining work
2. Run quality gates if code changed
3. Update PSTF artifacts
4. Update Beads issue status
5. Commit changes
6. Push to remote

git pull --rebase
git push
git status

git status must show the branch is up to date with origin.

Never say:

* “ready to push when you are”
* “left as future work”
* “good enough for now”
* “in a real system…”

If push fails, resolve and retry.

Final handoff must include:

* Beads issues worked
* code changed
* tests run
* PSTF artifacts updated
* remaining Beads issues
* blockers, if any
* confirmation that no fake, stubbed, hardcoded, or placeholder production-path behavior remains in the touched scope

⸻

Enforcement

These instructions are architectural constraints, not preferences.

Violations are bugs.

When you find a violation:

1. Fix it if in scope
2. Otherwise create or update a Beads issue
3. Record PSTF ambiguity when product intent is unclear
4. Do not bury the problem in comments or handoff prose

Build real Nostr systems: subscribe, react, validate, verify, track, test, push.