# K1 — fail-closed OCK rotation

Issue: `bahia-irsry.83`; base: `92f4da24`; branch: `fix/irsry-83-fail-closed-rotation`. Implementation commit: `8f915f34`.

## Recovered partial work

Kept the previous agent's rotate-before-publish approach, explicit `RotateKeyExcluding` interface and test doubles, deferred repository mutation, typed pending error, pending-org health snapshot, strict-refounding split, and initial failure tests.

Replaced its detached, uncancellable rotation/wrap retry timer loops with event-triggered cooldowns. Serialized key lifecycle operations and encryption, preserved candidate key material across ambiguous publish acknowledgments, prevented recovery failures from silently creating v1, and added deterministic backoff, concurrency, cold-recovery, wrap-retry and health tests. No new timer or goroutine is used for retries.

## Ordering and failure semantics

For removal: rotate with an explicit excluded pubkey while the old member registry is still intact. For downgrade: rotate while retaining the downgraded reader, as the NIP's lifecycle specifies (authorization remains separate). Only after successful rotation is canonical membership published, then the derived member repository is updated. A rotation error propagates to rejection with no membership publication or repository mutation.

Strict revocation additionally finishes refounding **before** committing the membership record. This retains strict errors while avoiding a rejected operation that already removed membership but left the repository unchanged. A partial refounding may refresh some old coordinates; membership is unchanged until the final member event succeeds. The publisher's existing rekey mutex now covers the complete membership/rekey sequence. Post-publish trust hydration applies the actual committed event even if Postgres still contains the old role.

## Detection, retry, and health

A required rotation establishes a per-org pending entry before attempting recovery/distribution; every failure retains it. `EncryptConfidential` returns `*OCKRotationPendingError` while pending, rechecks the guard under the lifecycle lock, and cannot use an epoch that raced with rotation. Successful distribution/activation clears the guard. Historical decrypts remain available.

Subsequent encryption/EnsureKey requests retry after cooldowns of 1, 2, 4, 8, 16, 32, then 60 seconds (capped), using the incoming context. Explicit RotateKey/membership/rekey retries attempt immediately. Idle orgs do not run background retries. Recovery clears the guard but does not replay a rejected membership intent. The excluded member and candidate key are retained across failures; excluded recipients cannot be queued for delayed wraps.

Failed add-member wraps warn and retry on subsequent encryption requests with the same cooldown. The current member source is checked before delivery; removed recipients are dropped. A wrap failure does not rotate or block other confidential writes.

`App.Health.Readiness()` registers `ock_rotation`: pending scopes report `warn`/aggregate `degraded` with one `org <id>: confidential publishes withheld pending key rotation` message per org, then return to pass/healthy after recovery. `ReadinessTracker` remains the independent EOSE/catch-up gate; liveness and repair intent availability are not disabled. The inspected 30315 paths are DNS-agent health and continuity heartbeat events, not an aggregate daemon-health publisher. This slice adds no 30315 wire schema or new health timer.

### Warm-start limitation

No exact recipient/registry mismatch detector was added. `OCKEnvelopeHistory` exposes only opaque random d-tag handles and NIP-44 ciphertexts; its service-decryptable wrap has no historical recipient roster. `OCKMemberSource` exposes current pubkeys, not departed members or a complete historical epoch roster. Envelope counts are unreliable because handles are random and wrap retries can produce duplicates. A reliable detector requires additional service-readable historical recipient metadata or a historical-membership source; neither exists in the current interfaces. The pending guard is process-local and does not claim to identify pre-K1 stale epochs after restart. Recovery query errors and missing service wraps for an existing scope are now errors rather than permission to reset to v1.

## Acceptance evidence

The machine-readable acceptance-to-test matrix is `acceptance_criteria.json`; all named tests were checked against the source. New/expanded tests cover strict/non-strict rejection and unchanged repositories, no canonical member event after failure, use of the rotated epoch, strict refounding failure before membership commit, stale-repository hydration, typed guards, historical reads, unrelated-org isolation, exclusion, candidate-key reuse, bounded retry, wrap-only recovery/removal cancellation, cold recovery, concurrency, and health recovery.

## Verification

Final gates passed after all code changes (2026-10-04):

| Gate | Result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | PASS |
| `CGO_ENABLED=0 go vet ./...` | PASS |
| `CGO_ENABLED=0 go test ./...` | PASS; includes `internal/archtest` (18.860s) |
| `gofmt -l internal/ cmd/ pkg/ | (! grep .)` | PASS; no output |
| `CGO_ENABLED=1 go test -race ./internal/controlplane ./internal/adapters/nostr` | PASS; 51.379s / 70.961s |
| `git diff --check` | PASS |
| `legacy_kinds`, `unwired_exports`, `poll_tickers` baselines vs base | Byte-for-byte unchanged |

The transient test-only export ratchet violations during development were fixed by retaining genuine production use of `EnsureKey` and `GetKey`; no baseline was expanded. The final strict failure regression uses rejected publication without sleep-based retry assumptions.

## Tooling and scope

RepoPrompt/Oracle tools were not exposed in this session, so exploration, edits and manual diff review used filesystem/shell tools scoped to this worktree. No subagents were spawned. Jev used find-lines/filter-search/rank-files plus doctor: 29 requests, 135,620 input tokens, approximately $0.005696. Broad health searches over-ranked unrelated DNS results; direct source inspection corrected the scope. Jev is advisory and was not used to generate code.

No `bd`, push, merge, or rebase was performed. `web/`, the main checkout, fleet-planning, `.beads/`, and architecture baselines are outside the edit scope. Commit hooks are disabled for these local commits because the configured hook path is `.beads/hooks`, which this slice is prohibited from accessing.

## Files

- `docs/user-guide/features/organizations.md`
- `internal/adapters/nostr/legacy_ock_migration_test.go`
- `internal/adapters/nostr/notification_canonical_publisher_test.go`
- `internal/adapters/nostr/operational_view_rest_parity_external_test.go`
- `internal/adapters/nostr/org_canonical_publisher.go`
- `internal/adapters/nostr/org_refounding.go`
- `internal/adapters/nostr/org_refounding_test.go`
- `internal/app/app.go`
- `internal/app/health.go`
- `internal/app/health_test.go`
- `internal/controlplane/confidential_encryptor.go`
- `internal/controlplane/ock_rotation_pending_test.go`
- `internal/controlplane/org_content_key_manager.go`
- `internal/controlplane/org_intent_handler.go`
- `internal/controlplane/org_intent_handler_test.go`
- `pstf/features/OCK_FAIL_CLOSED_ROTATION/acceptance_criteria.json`
- `pstf/features/OCK_FAIL_CLOSED_ROTATION/verification_report.md`
