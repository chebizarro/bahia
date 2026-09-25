# bahia-185t0 — stage 1 verification

Date: 2026-09-25. Worktree: `/Users/bizarro/Documents/Projects/.worktrees/bahia-cord`.
Branch: `fix/concord-authority`, based on local `master` `5495a884`.

## Acceptance evidence

- **S1-AC1 / S1-AC2:** `TestConcordRotationUnresolvedAuthorityHasNoSideEffects`
  covers 20 refused combinations, including equal-version and higher-version
  inputs, with explicit zero mint/Store/relay calls, unchanged sealed file bytes,
  no receipt and diagnostic evidence requirements. Owner-only private-channel
  rekey, absent owner citation and concurrent epoch advancement remain tested.
- **Trust anchor:** `TestConcordRotationOwnerMustBeBoundToConfiguredCommunity`
  rejects a substituted owner field even when the signer matches that field.
- **S1-AC3:** `TestConcordCompactionOmitsExactAuthorityCitationEvidence` verifies
  an exact signed Grant exists before folding, but is absent from every emitted
  compaction event while an action retains its unchanged citation. Typed Role
  bodies and real pairwise `control_wrap` delivery avoid conflating the missing
  history with missing staff-key delivery. This is an availability witness, not
  an authority resolver or end-to-end Refounding acceptance.
- **S1-AC4:** operator refusal inventory/escalation in `docs/soul-factory.md`,
  user-facing limitation in `docs/user-guide/features/souls.md`, and fresh-joiner
  requirements/evidence/scope gate under `### Stage 1 findings` in the existing
  investigation report.

The counterfactual run added the containment test before changing production.
It failed against the original implementation, including:

```text
TestConcordRotationUnresolvedAuthorityHasNoSideEffects/owner/refound/empty
refused operation returned receipt=true, minted=2, custody writes=1
refused rotation changed sealed custody bytes
refused rotation reached relay AUTH, query, or publication
```

## Quality gates

Go 1.26.3, darwin/arm64. Local gates, not deployment/live-community acceptance.

| Command | Outcome |
|---|---|
| `go test ./internal/soulfactory -run '^TestConcord' -count=1` | PASS |
| `go test -race ./internal/soulfactory -run '^TestConcord' -count=1` | PASS |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | PASS |
| `make race` | PASS (`CGO_ENABLED=1 go test -race ./... -count=1`) |
| `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` | Expected non-zero: exactly one pre-existing unused finding, no additions |
| `git diff --check` | PASS |

The lint residual is `(*Reactor).handleStandbyNodeDefinition` at
`internal/controlplane/continuity_definition_handlers.go:42:19`. No suppression,
configuration relaxation or out-of-scope handler change was made.

## Review and limitations

Manual review used the physical worktree's source and git diff. RepoPrompt rejected
absolute worktree reads, routed logical reads to main, and rejected its own correct
worktree diff artifact aliases with a provenance mismatch. No source edits used
that routing; absolute-path native edits were followed by main/worktree status
checks. Main remained clean at `5495a884`, 19 commits ahead of origin.

The Oracle review consequently covered the exact supplied refusal boundary and
test/report descriptions, **not the full worktree diff**. It reported no substantive
findings within that limited review. Do not treat it as independent full-diff or
test verification.

Stage 2 remains under `bahia-185t0`: owner-rooted resolution for every candidate,
exact-citation indexing, and a cross-epoch authority-evidence availability contract
with recovery/deferral for already-compacted input. No resolver, archive, migration
or deployed-community recovery was implemented or certified in stage 1. Refused
operations sacrifice incident-response availability; this is not a claim that
compromised communities are safe. The user owns issue state; no `bd` operations,
push, or merge are part of this handoff.
