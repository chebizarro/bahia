# Nostr Protocol Remediation Plan (2026-07-30)

Source: Nostr protocol audit of `bahia`. Each work item has a beads issue. This file is the
orchestration checklist — sub-agents implement one item each; the orchestrator updates status here.

Audit context in brief: the Go relay layer (`internal/adapters/nostr`) is strong (per-relay OK
reasons, NIP-42 on publish + subscribe CLOSED, EOSE-aware catch-up with DB `since` cursors,
insert-gated idempotency, jittered backoff). The findings below are edge-layer gaps.

---

## Work Item 1 — `bahia-6w4b5` (P1) — Loom/deployment lifecycle
- [x] Status: DONE (commit bb679f2e). Re-REQ with context-bounded backoff on channel closure; PollJobStatus*/pollForCompletion renamed to Await*; shutdown leaves runs non-terminal; startup recovery re-attaches queued/running runs preserving StartedAt+JobTimeout; poll_interval removed from config.go/.env.example/config.yaml. Tests added; go build ./... green. Bead closed.

**Findings BAH-02, BAH-03, BAH-09.**

1. **Re-REQ on subscription drop** — `internal/adapters/loom/client.go` `PollJobStatusFromWorker`:
   when `sub.Events` closes for a *non-auth* reason (transport drop), it returns
   `"subscription closed before terminal job result"` instead of resubscribing. The auth-CLOSED
   path already does `goto resubscribe` — extend the same treatment to channel closure, with a
   backoff wait bounded by the job context. The result filter is job-scoped (`#e`) and kind-5101
   results are stored, so re-REQ backfill replays the terminal result. Reserve error returns for
   context expiry.
2. **Shutdown != timeout** — `internal/workflow/coordinator.go` `pollForCompletion` maps ctx
   cancellation (process shutdown) to `RunStatusTimeout`. Instead: leave the run non-terminal on
   shutdown; add startup recovery that re-attaches to non-terminal runs (`LoomJobID` set) via the
   same subscription wait; only a genuine wall-clock `jobTimeout` measured from `StartedAt` should
   produce `RunStatusTimeout`.
3. **Remove vestigial polling surface** — delete dead `LoomConfig.PollInterval`
   (`internal/config/config.go` ~lines 401/768, `.env.example:30`, `config.yaml:31`; the field in
   `loom/client.go` is assigned but never read). Rename `PollJobStatus`/`PollJobStatusFromWorker`/
   `pollForCompletion` to `Await*` naming and fix the "Start polling" comment.

**Done when:** channel-closure triggers resubscribe (with test); shutdown no longer records
timeout; startup recovery re-attaches to non-terminal runs (with test); `poll_interval` gone from
config/env/yaml; `go build ./... && go test` passes for touched packages.

---

## Work Item 2 — `bahia-w4dq6` (P1) — Go adapter hygiene + publish outbox
- [x] Status: DONE (commit 0bc8081f). Subscriber backoff.Reset() on EOSE; reactor reconnect wait ctx-cancellable with backoff reset on resubscribe; durable publish outbox on nostr_events (migration 000050) with publish state, rate-limit-aware retry backoff, and background redelivery runner (nostr-publish-outbox). Fake-pool + backoff tests; go build ./... green. Bead closed.

**Findings BAH-07, BAH-08, BAH-06.**

1. **Backoff reset** — `internal/adapters/nostr/subscriber.go`: reconnect backoff never resets
   (a code comment admits it). Reset on evidence of a healthy session — e.g. `backoff.Reset()`
   when EOSE is received. Note `Run` owns the backoff and `handleEOSE` is on the Subscriber;
   plumb whichever way is cleanest.
2. **Ctx-aware reconnect wait** — `internal/controlplane/reactor.go:503` bare `time.Sleep(delay)`
   in the reconnect branch blocks shutdown up to 2 min. Use the `select { <-ctx.Done() /
   <-time.After(delay) }` pattern already used in `subscriber.go` `Run`.
3. **Durable publish outbox** — `internal/adapters/nostr/publisher.go` `publishEvent`: on total
   publish failure the event is dropped (early return even skips the audit record). Change to:
   record signed event first (audit table as outbox with publish-state), then publish, then mark;
   add a background redelivery loop retrying unpublished rows with backoff, honoring
   `IsRateLimited` OK reasons (`duplicate:` already counts as success). Keep scope pragmatic —
   a minimal reliable outbox, not a new subsystem. Repository layer:
   `internal/repository` (NostrEventRepository).

**Done when:** backoff resets after EOSE (test); reactor reconnect wait honors ctx; failed
publishes are persisted and retried by a background loop (test with fake pool); builds/tests pass.

---

## Work Item 3 — `bahia-jano7` (P1) — Web subscription recovery
- [x] Status: DONE (commit 75d27210). Shared `subscribeWithRecovery` helper in pool-subscriptions.js/pool-client.js; controlplane bootstrap, assistant, dns, fips-mesh stores migrated; discovery per-relay tracker + deadline; deterministic fake-timer tests; full vitest suite green (593 tests). Bead closed.

**Findings BAH-01, BAH-05. Part of epic `bahia-p50a`.**

1. **Re-REQ after CLOSED** — `web/src/lib/nostr/pool-subscriptions.js` reports every `onclose` as
   `{terminal: true}`; no store resubscribes (checked: `stores/controlplane/bootstrap.svelte.js:83`,
   `stores/assistant.svelte.js:514`, `stores/dns.svelte.js:649`, `stores/fips-mesh.svelte.js:392`
   only set status flags; `grep resubscribe web/src` = 0 hits). Add ONE shared
   subscribe-with-recovery helper (in the pool layer): on non-auth CLOSED, re-REQ with jittered
   backoff, using last-seen `created_at - 1` as `since` to bound backfill; on auth CLOSED run the
   existing `onAuth` path then re-REQ; only surface "disconnected" after N consecutive failures;
   reset backoff on EOSE. Migrate the live stores above onto it. nostr-tools `enableReconnect`
   only covers transport drops, not protocol CLOSED — do not rely on it.
2. **Discovery EOSE barrier** — `web/src/lib/stores/discovery.svelte.js` `discoverSystemInfo`
   (~lines 250–286): finalization requires EOSE from ALL configured relays even though connect
   only requires ≥1, and a single relay's terminal close rejects the whole promise; there is no
   overall deadline. Adopt the per-relay EOSE/CLOSED tracker pattern from `stores/souls.svelte.js`
   (markEose/markClosed/isComplete/isTerminal): finalize when every relay has EOSEd or terminally
   closed, succeed if ≥1 EOSEd, add an overall deadline, and drop or tightly bound
   `DISCOVERY_EOSE_DRAIN_MS`.

**Done when:** shared recovery helper exists with unit tests (CLOSED → re-REQ → events resume,
using fake timers); the four stores use it; discovery finalizes correctly with one dead relay
(test); `npm test` (vitest unit suite) passes.

---

## Work Item 4 — `bahia-v3szo` (P2) — Encrypted ContextVM ordering + test harness
- [x] Status: DONE (commit 888cbcda). Shared result subscription established in publishEncryptedRequest before build/publish (unauthenticated prebuilt-event path preserved); relay-harness sleep replaced with readiness-signal hook (remaining setTimeout is a 20s failure deadline only); deterministic ordering/fake-timer tests added (25 passing). Bead closed.

**Findings BAH-04, BAH-11 (test part relates to `bahia-fv4l`).**

1. **Subscribe before publish** — `web/src/lib/nostr/encrypted-controlplane.js`:
   `ensureSharedSubscription` is only invoked inside `awaitEncryptedResult`, i.e. after
   `publishEncryptedRequest` has already sent the request. The shared result subscription filters
   include ephemeral kinds 21059/25910 (not stored by relays), so a fast service response can be
   irrecoverably lost; the client then fails on `workTimeoutMs`. Fix: establish the shared
   subscription inside `publishEncryptedRequest` (and `buildEncryptedRequestEvent` path if
   relevant) before the request event is published. Mind the teardown/recreate paths
   (relay-set change, pubkey change).
2. **Deterministic e2e harness** — `web/tests/e2e/relay-harness.js:43` has a real
   `setTimeout(..., 100)` sleep. Replace with a deterministic hook (promise resolved on REQ
   receipt / subscription open / EOSE emission). Unit tests already use vi fake timers — extend
   that discipline.

**Done when:** subscription is live before publish (unit test asserting ordering); harness sleep
removed; existing encrypted-controlplane tests still pass.

---

## Work Item 5 — `bahia-ivkuy` (P2) — LLM coordinator intake review
- [x] Status: DONE (commit acbe975a). Finding: the 5s tick WAS primary intake (reactor/ContextVM handlers persisted intents via LLMRegistryService and emitted created/approved events, but the coordinator never subscribed). Now event-driven: created/approved events trigger serialized runOnce; ticker demoted to llm.recovery_poll_interval (default 30s) matching sibling coordinators; validation, app wiring, tests updated. Bead closed with findings.

**Finding BAH-10.**

`internal/service/llm_provisioning_coordinator.go` ticks at `CoordinatorPollInterval` (default 5s,
`config.go` ~804) — unlike sibling coordinators (backup_run/retention/restore, ml_recipe,
ml_inference, tool_provisioning) whose 30s `RecoveryPollInterval` is a crash-recovery backstop.
First VERIFY how LLM provisioning intents actually arrive (reactor/ContextVM handlers? check
`internal/app/app.go` wiring ~581 and reactor handlers). Then:
- If intake is event-driven: widen default to 30–60s, rename to `RecoveryPollInterval` for
  consistency (config key + koanf tag + validation message), document.
- If the tick is primary intake: wire intent events to trigger `runOnce` and keep the tick as
  backstop only.

**Done when:** intake path documented in the issue; interval demoted/renamed or event trigger
wired; config validation and defaults updated coherently; builds/tests pass.

---

## Deferred — `bahia-fukea` (P2) — Observability follow-up
Now unblocked (items 2 and 3 landed) but intentionally not dispatched in this round. Relay CLOSED-reason metrics in `RelayHealthTracker`/`HealthSnapshot`,
uniform web store health fields, outbox-depth + stale-run alerts. Not dispatched in this round.

---

## Coordination notes
- Wave 1 (parallel): Item 1 (Go, loom/workflow/config) + Item 3 (web, pool/stores).
- Wave 2 (parallel): Item 2 (Go, adapters/reactor/repository) + Item 4 (web, encrypted-controlplane/tests).
- Item 5 last (touches `config.go`, which Item 1 also edits).
- Go items and web items never overlap. Within a language wave, files are disjoint; agents are
  told about siblings.
