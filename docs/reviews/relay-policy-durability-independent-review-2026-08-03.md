# Independent Review — `fp-bahia-relay-policy-durability`

**Task:** Make Bahia relay policy durable and upgrade-safe (P0, fleet ledger `fp-bahia-relay-policy-durability`)
**Ledger event:** `0a1481d00422dd50c728a9adb2e57ea2708cd6c212a9da707da8e7b2e0e1808e` (kind 30900, `relay.sharegap.net`)
**Reviewer:** independent review session, 2026-08-03. Read-only except this report.
**Commits under review:** `343cf06a`, `1d2d7c95`, `71a648f2` (all on `origin/master`)

**Overall verdict: CONDITIONAL — do not close on code evidence alone.**
The durability core (requirement 1–3 fail-safe semantics) is genuinely well built and I could not find any path that blanks or replaces a valid last-known-good projection. However, one confirmed defect breaks **recovery** after a relay outage, and two coverage gaps mean the central ordering invariant is asserted only against fakes. The task's acceptance criteria additionally require live edge-01 evidence that cannot be produced from code.

---

## 1. Scope and method

- Fetched the authoritative task text from the relay (not from commit messages).
- Read in full: `internal/controlplane/relay_settings_hydrator.go`, `relay_settings_handlers.go`, `internal/repository/pg_relay_policy_projection.go`, `relay_policy_projection.go`, the `000052`/`000053` migrations, `internal/app/app.go` wiring (L1179–1239), `internal/adapters/nostr/relay_pool.go` merge/EOSE machinery, and the upstream `fiatjaf.com/nostr` subscription lifecycle.
- Delegated and then spot-checked three probes (Svelte UI, deploy/rollout gate, backup/restore).
- Executed the test suite: `go test ./internal/controlplane/ ./internal/repository/ ./internal/adapters/nostr/ ./internal/app/ ./internal/service/` → **all pass** (exit 0).

---

## 2. Per-requirement verdicts

| # | Requirement | Verdict |
|---|---|---|
| 1 | Durable PG projection with validated promotion | **MET** |
| 2 | Multi-relay hydration, per-relay EOSE, bounded drain, dedupe, auth/outage tolerance, deterministic selection | **PARTIAL — outage recovery broken (B-1)** |
| 3 | Fail safe: only a newly validated event replaces the projection | **MET** |
| 4 | Truth states + provenance in UI/API; Apply gating | **PARTIAL — server API never reports `loaded-live` (F-1)** |
| 5 | Upgrade invariant: pre/post capture, health gate, rollback, digest pinning | **MET, with caveats (F-3, F-4)** |
| 6 | Backup/export/restore preserving provenance; restored = cached | **PARTIAL — distinction invisible to operators (F-2), untested transition (B-3)** |
| 7 | Browser override explicitly local/noncanonical, migrated | **MET** |
| — | Security: no secrets in logs/events/evidence/storage; signer-first; no unsigned REST | **MET, with one hardening gap (F-5)** |

### Requirement 1 — durable validated promotion: MET

`relayPolicyStateFromCanonicalEvent` (`relay_settings_hydrator.go:432-467`) enforces kind, trusted author, `d`/`domain`/`schema` tags, content schema, and full payload normalization. `nostradapter.ValidateInboundEvent` (`internal/adapters/nostr/validation.go:18`) verifies the ID derivation, the Schnorr signature, and a ±skew window (`InboundEventMaxFutureSkew = 10m`, `InboundEventMaxPastAge = 365d`). The projection stores canonical payload plus SHA-256 hash, and `relayPolicyStateFromProjection` (`:469-503`) re-derives and byte-compares both on load, so a corrupted row fails closed rather than becoming runtime state (`TestRelaySettingsHydratorRejectsCorruptStoredProjection`).

Preload is genuinely synchronous and pre-activation: `app.go:1203` calls `LoadProjection(ctx)` and returns a startup error on failure, before `bgManager.RegisterWithOptions(relaySettingsHydrator, ...)` at `:1234`.

### Requirement 3 — fail-safe semantics: MET

I traced every path the task enumerates and none can blank or replace a valid head:

| Path | Behavior | Evidence |
|---|---|---|
| Zero-event EOSE | Drain fires, `MarkSynced` only; head untouched | `hydrator.go:239-256`; `TestRelaySettingsHydratorZeroEventEOSERetainsLastKnownGood` |
| Subscription timeout / ctx cancel | `subscribe` returns; backoff; head retained | `hydrator.go:126-152` |
| Auth failure | `handleRelayClosed` returns false → no state change | `:284-307`; `TestRelaySettingsHydratorPromotionFailureAuthAndTimeoutRetainHead` |
| Parse error / invalid event | Logged + dropped before promotion | `:314-330`; `TestRelaySettingsHydratorInvalidCandidatesNeverInvalidateValidHead` (6 sub-cases) |
| DB write outage | `Promote` error → warn + `return false`, head retained, event **not** marked seen so it can retry | `:355-362` |
| DB read outage (API) | `GetPolicy` returns `unavailable`, never config defaults | `handlers.go:194-201`; `TestRelaySettingsGetPolicyReturnsUnavailableWithoutInferringConfigDefaults` |
| Restart / image swap | Projection preloaded from PG independent of relay reachability | `TestRelaySettingsHydratorLoadsDurableProjectionWhenRelaysUnavailable` |

Empty is reachable only as an explicitly signed policy: the read path passes `requireRelayTopology=false` while the Apply path passes `true` (`handlers.go:648-651`), so absence can never be mistaken for a signed empty policy, and `relayPolicyIsIntentionallyEmpty` is a positive test on decoded content, never on absence.

There is **no** SQL `DELETE` against `relay_policy_projections` anywhere in the repository, and the `Promote`/`RestoreCached` `WHERE` clauses are strictly monotonic.

### Requirement 4 — Apply gating: enforced server-side

`authorizePolicyReplacement` (`handlers.go:588-627`) is the real gate and it fails closed:
- projection exists + no `expected_projection.event_id` → rejected;
- `expected_projection` mismatch on event ID or hash → rejected;
- `replacement_confirmation` supplied while policy is readable → rejected;
- confirmation supplied while truth is `never-configured` → rejected;
- confirmation is only honoured when `projectionStore.Get` actually errored, and must carry `confirmed=true`, `previous_truth_state=unavailable`, `reason_code=relay_hydration_unavailable`, and a charset-restricted change reference.

The probe's concern that the panel can leave Apply enabled with `expectedProjection = null` is real at the UI layer (`RelaySettingsPanel.svelte:428-438`) but **not exploitable**: the server rejects that request. The audited replacement is published *before* the canonical mutation (`handlers.go:283-287`), and `TestRelaySettingsApplyPublishesNoCanonicalStateWhenReplacementAuditFails` proves the ordering holds.

### Requirement 7 — local override: MET

`web/src/lib/nostr/subscriptions.js:11-78` stores `bahia_nostr_relays` as a versioned envelope (`bahia.browser-relay-override.v2`, `scope: 'browser-local-noncanonical'`) with in-place migration from legacy bare arrays. The panel labels it `LOCAL / NONCANONICAL emergency override` and its save path (`RelaySettingsPanel.svelte:544-547`) only touches localStorage and the browser client — no call reaches `applyRelayPolicy()` or event publication.

### Security: MET, one hardening gap

- No REST route touches relay policy: `grep -rn "relay-policy\|relay_policy" internal/api/` returns nothing, and `relay-settings-controlplane.js` contains no `fetch(`/`/api/` call. All mutations are signer-first encrypted ContextVM.
- Logs carry event IDs, hashes, pubkeys and `safeRelayURL`-sanitized relay URLs (userinfo, query and fragment stripped — `hydrator.go:557-568`). No nsec, bunker URI, token, or admin credential is logged.
- `/ready` is deliberately unauthenticated and exposes only public signed provenance (`app.go:1224-1231`).
- `relay_administration.targets[].authorization` is a classification label (`bahia_owned` / `bahia_authorized`), not a credential; the actual `AdministratorPrivateKeyRef` lives only in runtime config and is absent from the canonical policy type.

---

## 3. Confirmed blockers

### B-1 — Relay outage permanently wedges the hydrator; it never resubscribes (CONFIRMED)

**This is the finding that most directly threatens the task's own acceptance test.**

`mergeRelaySubscriptions` only closes `EndOfStoredEvents` once *every* relay subscription has signalled EOSE (`relay_pool.go:875-877`). A relay whose connection dies before EOSE exits its goroutine at `relay_pool.go:906-925` via the plain `return` without calling `markEOSE()` — the upstream 7-second fake-EOSE dispatch (`fiatjaf.com/nostr@…/relay.go:644-655`) is never observed because the merge goroutine has already returned. This is intentional at the pool layer (`TestMergeSubscriptionsDoesNotSignalEOSEWhenRelayClosesBeforeEOSE`), but the hydrator does not account for it.

In `RelaySettingsHydrator.subscribe` (`hydrator.go:189-273`) the loop only returns when **all** of `eventsCh`, `eoseCh`, `allEOSECh`, `closedCh`, `drainCh` are nil. When every relay drops, `eventsWg` completes and `merged`/`relayEOSE`/`closed` all close — but `EndOfStoredEvents` is never closed, so `allEOSECh` stays non-nil forever. The loop then blocks on `select { <-ctx.Done(); <-allEOSECh }` indefinitely.

**Failure scenario:** operator suppresses canonical relay availability (exactly acceptance step 3), all relay connections drop mid-subscription, `subscribe()` blocks forever. The backoff/reconnect loop in `Run` (`:133-152`) is never reached. When the relay is restored, the hydrator does not resubscribe, never observes the canonical event, and never returns to `loaded-live` — until the process is restarted.

**Partial-outage variant:** if one relay drops pre-EOSE while others survive, events still promote (the event path does not depend on EOSE), but `allEOSECh` never closes, so the bounded drain never fires, `caughtUp` stays false and `MarkSynced` is never called. `last_sync_at` then ages past the 5-minute freshness window and the UI reports `Loaded — cached/stale` permanently even though a live relay is confirming events.

Fail-safe with respect to blanking: **yes**, the head is retained. Recoverable without operator action: **no**.

*Suggested fix:* bound the wait on `allEOSECh` (start the drain on a ceiling timer, or treat "all source channels closed" as subscription end and fall through to backoff), and add a regression test where the merged subscription closes `Events`/`RelayEOSE`/`Closed` without ever closing `EndOfStoredEvents`.

### B-2 — The PostgreSQL ordering invariant is never executed against PostgreSQL (CONFIRMED)

`internal/repository/pg_relay_policy_projection_test.go` uses `pgxmock`. `TestPgRelayPolicyProjectionPromoteUsesAtomicReplaceableOrdering` matches only the regex `"INSERT INTO relay_policy_projections"` and returns canned rows — it asserts that `Promote` returns `true` when the mock yields a row and `false` when it yields none. **The `WHERE` clause that actually implements newest-valid selection, the equal-timestamp lowest-event-ID tie-break, and the cached→confirmed transition is never evaluated by a database engine in CI.** The repository contains no Postgres integration harness (no testcontainers/dockertest/DSN-gated tests).

The only executable assertions on ordering live in `memoryRelayPolicyProjectionStore.Promote` (`relay_settings_hydrator_test.go:39-53`), a hand-written fake. Given the task classifies this as a P0 production durability regression, an invariant guarded only by a fake plus a string-matched mock is insufficient evidence.

### B-3 — The fake contradicts the SQL on the cached→relay-confirmed transition (CONFIRMED)

The SQL `Promote` has a third `WHERE` branch that re-promotes an *identical* event when `relay_confirmed_at IS NULL`, which is the mechanism that upgrades a restored (cached) projection to relay-confirmed (`pg_relay_policy_projection.go:79-87`). The in-memory fake rejects that case outright:

```go
if candidate.EventCreatedAt.Equal(current.EventCreatedAt) && candidate.EventID >= current.EventID {
    return false, nil   // same event ID → never promoted, relay_confirmed_at ignored entirely
}
```

So the single most important behavior of requirement 6 — "restored state remains cached **until relay confirmation**" — has no test that exercises the confirming transition; the fake models the opposite, and the pgxmock test canned the result.

Compounding this, `RelaySettingsHydrator.seenEventIDs` is never cleared (not on resubscribe, `hydrator.go:190-192`). If a restore lands in a process that has already observed that event ID, `handleEventFromRelay` short-circuits at `:341-346` before reaching `Promote`, so the restored projection stays `cached` for the life of the process even though the relay is serving the event.

---

## 4. Follow-ups (not blockers)

**F-1 — The server API never reports `loaded-live`.** `GetPolicy` (`handlers.go:222-227`) only emits `loaded-cached`, `loaded-stale`, `intentionally-empty`, `never-configured`, or `unavailable`, derived purely from the freshness window. `loaded-live` exists only as a browser-side synthesis from the panel's own live subscription (`relay-settings-controlplane.js:155-173`). Requirement 4 asks for the state on the **UI/API** surface; a non-browser API consumer cannot distinguish live from cached.

**F-2 — `relay_confirmed_at` is invisible to the operator surface.** It is read by `Get`, both conflict clauses, and the `/ready` health detail (`app.go:1218-1221`), but `RelayPolicyProjectionView` has no confirmation field. A restored-but-unconfirmed projection and a relay-confirmed one render identically in Settings. Requirement 6's guarantee is enforced in the database but not surfaced where operators would act on it.

**F-3 — "fresh" is not evidence that canonical policy was observed.** `MarkSynced` is called on every completed drain regardless of whether any event arrived (`hydrator.go:239-247`). A restored projection therefore flips from `loaded-stale` to `loaded-cached` after a single zero-event EOSE, purely from a successful *connection*. Combined with F-2, an operator has no signal distinguishing "the relay confirmed this policy" from "the relay was reachable and had nothing".

**F-4 — Rollback is triggered but not enforced.** The `EXIT` trap in `.github/workflows/deploy-edge.yml:136-151` restores the pre-rollout Compose file and re-runs readiness plus the gate, but every post-rollback check is suffixed `|| true`. A rollback that itself fails to restore a healthy relay policy exits with the original status and no distinct signal. Also `relay_policy_rollout_gate.py:68-76` swallows `HTTPError` and parses the error body, so a non-2xx `/ready` carrying an otherwise-passing projection would pass the gate.

**F-5 — Policy URL validators do not reject embedded credentials.** `validateWebsocketRelayURLForSettings` / `validateRelayAdministrationHTTPURLForSettings` (`handlers.go:738-770`) check scheme and host but permit userinfo and query strings. Since the canonical policy is published as a **public signed event** and copied verbatim into backup metadata, an operator who pastes `wss://user:token@relay` would publish that credential irreversibly. `safeRelayURL` already does this sanitization for logging — apply the same rejection at the Apply validation boundary.

**F-6 — Backups are integrity-checked, not authenticated.** `ValidateRelayPolicyProjectionBackup` recomputes an unkeyed SHA-256 over the payload (`relay_policy_projection_backup.go:91-95`). This detects corruption, but a coherently tampered backup (payload + recomputed hash + arbitrary 64-hex event ID) passes, because the backup omits the signature, kind and tags and never re-derives the event ID. Author is checked only by string comparison against the configured service pubkey. Mitigated by the restore's `WHERE` monotonicity and by `relay_confirmed_at = NULL`, but the format does not preserve *cryptographic* provenance as the requirement's wording implies.

**F-7 — `seenEventIDs` grows unbounded** for the process lifetime (`hydrator.go:341-353`). Minor, but it is also the mechanism behind B-3's second half.

---

## 5. Regression-matrix coverage

| Task-mandated scenario | Covered | Where |
|---|---|---|
| Restart / image replacement while canonical relay unavailable | ✅ | `TestRelaySettingsHydratorLoadsDurableProjectionWhenRelaysUnavailable`; `TestRelayPolicyHydrationRelayURLsRetainsStoredPolicyRelaysAcrossUpgrade` |
| Event only on secondary relay | ⚠️ partial | `TestRelaySettingsHydratorDrainsEventAfterEOSEFromSecondaryRelay` conflates this with the drain case; no test where the primary EOSEs empty and only the secondary ever carries the event |
| EVENT after EOSE within drain | ✅ | same test |
| Zero-event EOSE | ✅ | `TestRelaySettingsHydratorZeroEventEOSERetainsLastKnownGood` |
| Explicit signed empty policy | ✅ | `TestRelaySettingsHydratorAcceptsExplicitSignedEmptyPolicy`; `TestRelaySettingsGetPolicyDistinguishesStaleAndSignedEmpty`; e2e `distinguishes an explicitly signed empty policy from unavailable` |
| Older / malformed / invalid-signature / wrong-author / wrong-schema / future events | ✅ | `TestRelaySettingsHydratorInvalidCandidatesNeverInvalidateValidHead` (6 sub-cases) + `TestRelayPolicyCanonicalTagsAreRequired` |
| Equal-time ordering | ⚠️ fake only | `TestRelaySettingsHydratorEqualTimestampUsesLowestEventID` runs against the memory fake; the SQL tie-break is unexecuted (**B-2**) |
| Auth failure | ✅ | `TestRelaySettingsHydratorPromotionFailureAuthAndTimeoutRetainHead` |
| Timeout / cancel | ✅ | same test |
| **Relay outage then recovery / resubscribe** | ❌ **MISSING** | no test drops a live subscription and asserts the hydrator resubscribes — this is the gap that hides **B-1** |
| **Restored-cached → relay-confirmed transition** | ❌ **MISSING** | fake models the opposite; pgxmock cans the result (**B-3**) |
| **DB `Promote`/`RestoreCached` ordering against real Postgres** | ❌ **MISSING** | pgxmock only (**B-2**) |
| UI reload / cached-stale not blank | ✅ | e2e `reloads a cached stale projection with provenance instead of rendering blank truth` |
| UI unavailable + Apply gating | ✅ | e2e `distinguishes unavailable truth and gates Apply behind audited replacement confirmation`; `TestRelaySettingsApplyRequiresExpectedProjectionHead`, `…RejectsMissingOrFalseUnavailableConfirmation` |
| Older live replay cannot downgrade cached | ✅ | e2e `does not let an older live replay downgrade a newer cached projection` |
| New browser profile | ⚠️ implicit | Playwright's default fresh context; no explicit assertion |
| Local override persistence + schema migration | ✅ | `web/tests/unit/relay-override-storage.test.js` |
| Pre/post hash mismatch rollback | ⚠️ partial | `test/scripts/test_relay_policy_rollout_gate.py` covers 5 gate decisions; **no test exercises the workflow rollback trap itself** |
| DB backup / restore | ✅ | `TestRelayPolicyProjectionBackupRestoreRoundTripPreservesProvenanceAndStaysCached`; `TestPgRelayPolicyProjectionRestoreCachedPreservesProvenanceAndClearsConfirmation` (mocked) |
| Secret-redaction audit | ⚠️ partial | redaction assertions exist in `web/tests/unit/relay-settings-controlplane.test.js`; no dedicated audit test over log/event/backup output for the new paths |

**Suite status:** `go test` across the five affected packages passes (exit 0).

---

## 6. Acceptance steps that cannot be proven from code

The task's acceptance clause is explicitly environmental. The following require live edge-01 and are **out of scope for this review**:

1. Independently deploying digest-pinned backend/web builds and recording the resulting image digests.
2. Restarting backend / web / relay in controlled permutations.
3. Suppressing canonical relay availability and observing that the accepted policy never appears blank — **and that it then recovers to `loaded-live` with a same-or-newer signed event/hash.** Note that **B-1 predicts this step will fail**: recovery after a full relay outage requires a process restart.
4. Proving automatic rollout rejection/rollback against a simulated loss on the real workflow (the gate's decision logic is unit-tested; the trap is not).
5. Producing the evidence bundle: pre/post event IDs and hashes, restart matrix, backup restore transcript, rollback transcript.

One deployment caveat worth confirming in that environment: the workflow pins by **local Docker image ID** (`.github/workflows/deploy-edge.yml:129-135`), not a registry `repo@sha256:<manifest-digest>`. That is immutable on the host but is not a portable/verifiable supply-chain digest, and it differs from what `docker-compose.deploy.yml` demands.

---

## 7. Recommendation

**Do not close `fp-bahia-relay-policy-durability` yet.** Required before acceptance:

1. Fix **B-1** and add the relay-outage-then-resubscribe regression test. Without this, acceptance step 3 cannot pass.
2. Close **B-2** / **B-3** — exercise `Promote` and `RestoreCached` against a real PostgreSQL instance covering: newer wins, older rejected, equal-time lowest-ID wins, same-event re-observation upgrades `cached` → `relay_confirmed`, and restore cannot demote a confirmed head. Align `memoryRelayPolicyProjectionStore` with the SQL semantics or delete it in favour of the integration store.
3. Clear `seenEventIDs` for the head event on restore (or key dedupe by `(event_id, confirmed)`), so a restored projection can be relay-confirmed without a process restart.
4. Then run the live edge-01 acceptance matrix and attach the evidence bundle.

Items **F-1** through **F-7** should be filed as follow-up issues; **F-5** (credential-bearing relay URLs published in a public signed event) is worth prioritising despite being classified as a follow-up.

The implementer must not self-close. This review does not constitute acceptance.
