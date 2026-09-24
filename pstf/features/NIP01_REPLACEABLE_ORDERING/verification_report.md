# NIP-01 replaceable convergence verification

Scope: `bahia-cmgb9`, worktree `bahia-nip01`, branch
`fix/nip01-replaceable-tiebreak`, base `e73c003c`. Issue lifecycle is user-owned.
No push, merge, or IPv6 sibling-path changes.

## Dependency and correction

The LLM workflow harness generated IDs ending in `Date.now()` and used
second-resolution `created_at`. Later revisions within a second therefore had
higher IDs. The old incorrect comparator selected the expected workflow state;
NIP-01 correctly discarded it. The shared harness now assigns successive seconds
to successive control-state snapshots and memoizes replay events. Workflow
assertions were not weakened or special-cased.

A second dependency existed in production: collection domain-time watermarks
could override a rejected replaceable upsert. Collections now reduce each wire
coordinate first and merge only its retained winner across legacy/corrected
logical coordinates, including tombstones and cases where a wire winner has an
older domain clock. Go decoded ordering metadata uses the signed wire timestamp,
not payload `updated_at`. Migration `000068` rebases existing metadata from its
source events and invalidates metadata with missing sources, preserving events
and projected entities.

SQLite, archive latest queries, projection cache persistence/hydration,
SoulFactory draft/template/fleet/runtime/relay-list selectors, and the FIPS
bridge now retain the lowest-ID timestamp tie. The existing relay-policy
projection already used that direction. Command chronology and immutable audit
sequencing are not replaceable-event retention and were not flipped.

## Related issue: bahia-1antv remains separate

`ConfigConsumer.publishStatus` publishes accepted and applied phases to the same
kind-30315 coordinate. NIP-01 cannot guarantee that applied has the lowest hash.
`TestConfigStatusNIP01DoesNotImplyAppliedPrecedence` exercises the actual signer,
Khatru publisher/policy and SQLite store with equal timestamps, both arrival
orders, and both possible lexical phase winners. Accepted can still be the
correctly retained event. Durable terminal-status semantics need separate work;
this change does not claim to resolve `bahia-1antv` or alter config wire shapes.
The existing concurrent publisher rendezvous test remains intact.

## Verification

| Gate | Result |
|---|---|
| Pristine web unit baseline | 97 files, 758 tests passed |
| Pristine E2E baseline | 192 passed, 1 skipped; 35.1s |
| Naive comparator flip, LLM spec repeated 3 times | Workflow failed 3/3 at second pending approval; other scenario passed 3/3 |
| Corrected shared fixture, same repeated spec | 6 passed |
| Old comparators restored against new web regression | 9 failed, 1 passed; restored corrected code afterward |
| Old SQLite predicate restored against signed/restart regression | All seven high-ID-first kind cases failed; restored corrected code afterward |
| Final web unit suite | 98 files, 769 tests passed |
| Final isolated E2E suite | 192 passed, 1 skipped; 42.7s |
| `go build ./...` | Passed |
| `go test ./...` | Passed: 81 tested packages, 7 packages without tests |
| `make race` | Passed: 81 tested packages, 7 packages without tests; no race reports |

The initial full E2E run overlapped a cold Go build/test run and had four auth/
assistant startup assertion timeouts; Go had a Firecracker delayed-socket timeout.
Isolated reruns of both full suites passed. No timeout expectations or unrelated
runtime tests were changed. Leaked `bahia-test-relay` processes were killed before
E2E runs; the separately tracked relay-harness leak was not changed.

The migration test executes the embedded, portable SQL transformation against
SQLite. This is not a live PostgreSQL migration/deployment acceptance claim.

## Review and tooling

Manual diff and scope review completed. Oracle review could not run: the tool
returned `targetBindingMismatch`, including a fresh-chat recovery attempt.
RepoPrompt's edit routing also misrouted the initial one-line comparator edit to
main despite the worktree binding; that exact edit was immediately restored.
All implementation edits thereafter used explicit paths in the requested
worktree. Git artifacts were correctly scoped using the absolute worktree root.

Jev relevance screening used 16 requests / 98,788 input tokens (about $0.004151),
including its setup check. It reduced broad ordering searches and identified
focused file ranges; the low-relevance LLM spec result led to examining the
shared harness. One empty-search invocation reported no input and was not used.
