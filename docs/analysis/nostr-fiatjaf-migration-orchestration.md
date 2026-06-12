# Nostr Fiatjaf Module Migration Orchestration

Generated: 2026-06-12

## Goal

Remove all production/test/module dependency on `github.com/nbd-wtf/go-nostr` from Bahia and migrate every direct use to latest `fiatjaf.com/nostr`, preserving Bahia's Nostr-native semantics: scoped subscriptions, EVENT/EOSE/CLOSED/AUTH handling, OK publish verification, event validation, dedupe/idempotency, and replaceable event behavior.

## Initial evidence

- `bahia/go.mod` currently requires both:
  - `fiatjaf.com/nostr v0.0.0-20260429223247-05b426e67eb7`
  - `github.com/nbd-wtf/go-nostr v0.48.3`
- RepoPrompt search found 192 occurrences of `github.com/nbd-wtf/go-nostr` in `bahia`.
- Existing canonical imports already appear in `internal/app`, many `internal/controlplane` files, `internal/relaysidecar`, `internal/service`, and `pkg/client`.
- `go list -m -versions fiatjaf.com/nostr` printed only the module path; implementers must resolve `fiatjaf.com/nostr@latest` with Go tooling.

## Required workflow

- Run `bd prime` before implementation.
- Create or claim a Beads issue for this migration before code changes.
- Create/update PSTF artifacts under `pstf/features/NOSTR_FIATJAF_MODULE_MIGRATION/`.
- Update user-facing docs if behavior or public Go APIs are documented.
- Run relevant tests, `go mod tidy`, final import search, commit, pull --rebase, push, and verify clean/up-to-date git status.

## Acceptance criteria

1. No Go imports of `github.com/nbd-wtf/go-nostr` or its subpackages remain.
2. `go.mod` and `go.sum` no longer reference `github.com/nbd-wtf/go-nostr`.
3. `fiatjaf.com/nostr` is updated to latest resolved by Go tooling.
4. Publish OK semantics remain verified, including accepted flag/message and partial relay failure behavior.
5. Subscription semantics remain event-driven: scoped filters, EOSE catch-up, CLOSED handling, AUTH handling, no polling/wait-sleep completion logic.
6. Inbound event validation remains strict: ID hash, Schnorr signature, pubkey, timestamps, tags, JSON content where expected.
7. Tests are deterministic and mapped to PSTF acceptance criteria.
8. Remaining gaps, if any, are captured in Beads, not TODO comments or handoff prose.

## Work items

### [ ] Item 1 — Core canonical Nostr adapter and helper migration

**Goal:** Establish canonical `fiatjaf.com/nostr` helper boundaries and migrate the central `internal/adapters/nostr` package, where relay pools, publishing, subscribing, validation, cataloging, bootstrap, FIPS subscription, relay admin, replay cursors, and serialization are concentrated.

**Done when:** `internal/adapters/nostr` and new helper code compile without `go-nostr`; relay publish/subscribe semantics preserve OK, EOSE, CLOSED, AUTH, validation, dedupe, relay health, and metadata behavior; targeted tests for `./internal/adapters/nostr` pass or failures are recorded in PSTF/Beads with concrete defects.

**Key files/modules:** `internal/adapters/nostr/**`, optional new `internal/nostrutil/**`, `internal/controlplane/signer.go`, `go.mod` as needed for resolving latest module.

**Dependencies:** None.

**Size:** Large.

### [ ] Item 2 — Protocol/NIP consumer migration

**Goal:** Migrate NIP and protocol consumers outside the core adapter: NIP-44 secrets/notifications/loom, NIP-46 signet, NIP-98 auth, Blossom auth events, Hive-CI, FIPS bridge, SBOM, signing attestations, nostr migration runner, and discovery resolver.

**Done when:** These packages compile without `go-nostr`; canonical helper wrappers are reused rather than ad hoc conversions; tests for affected packages are run or blockers are recorded; no protocol behavior is weakened or replaced by polling/request-response shims.

**Key files/modules:** `internal/adapters/{secrets,signet,loom,hiveci,blossom,sbom,signing}/**`, `internal/auth/**`, `internal/fipsbridge/**`, `internal/notifications/**`, `internal/nostrmigration/**`, `pkg/discovery/**`.

**Dependencies:** Item 1 preferred, because helper/adapter types are central.

**Size:** Large.

### [ ] Item 3 — Public clients, CLI, residual imports, dependency cleanup, verification, and push

**Goal:** Migrate remaining public/client/CLI/controlplane/service/test imports, remove the old module from dependency metadata, run repo-wide cleanup and quality gates, update PSTF/Beads, commit and push.

**Done when:** `rg 'github.com/nbd-wtf/go-nostr|go-nostr'` has no disallowed Go/module references; `go mod tidy` has removed old sums; targeted and broad tests have been run with results recorded; PSTF artifacts are updated; Beads issues are closed/created for gaps; changes are committed and pushed with clean/up-to-date `git status`.

**Key files/modules:** `cmd/**`, `pkg/client/**`, `internal/api/**`, `internal/app/**`, `internal/controlplane/**`, `internal/mcp/**`, `internal/service/**`, `internal/soulfactory/**`, `test/integration/**`, `go.mod`, `go.sum`, PSTF artifacts, docs if needed.

**Dependencies:** Items 1 and 2.

**Size:** Large.

## Risk notes

- `relay_pool.go` is highest risk: relay connection/publish/subscription APIs are library-specific and must preserve OK/EOSE/CLOSED/AUTH handling.
- `internal/adapters/signet/client.go` may need either a `fiatjaf.com/nostr` NIP-46 wrapper or a local NIP-46 implementation behind the existing public API.
- NIP-44 stored-secret compatibility must be verified if changing key derivation behavior; create PSTF defect and Beads issue if existing ciphertext migration is needed.
- Do not leave a long-lived go-nostr-shaped compatibility layer. Temporary compile aids must be removed before final dependency cleanup.

## Progress log

- 2026-06-12: Orchestrator created this plan from repository scan and context-builder plan.
