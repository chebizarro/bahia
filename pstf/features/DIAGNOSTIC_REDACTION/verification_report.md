# Diagnostic redaction verification

Issues: `bahia-h2o2y`, `bahia-7bjpw`. Base: `6661c0eb`. Branch: `fix/redact`.

## Implementation and boundaries

The base already had DSN error scrubbing and config renderers; the open issue
text predated those protections. A newer `HiveCIDependencyGiteaConfig.Token`
was still exposed. The existing tag-based mechanism is now fail-closed for
unclassified leaves, with explicit classifications on every config leaf and
automatic schema/subtree tests. This preserves plaintext operational APIs without
requiring a Secret-type migration into sibling-owned controlplane callers.
Config primitives extracted directly remain plaintext: callers must not log them
or raw DSNs. Whole-config and subtree diagnostic rendering is the protected API.

Encoded-secret replacement is shared by database/config errors and Gitea.
Opaque errors do not retain credential-bearing causes or hidden struct fields.
The controlplane run-log scrubber and Signet public-key abbreviation were read
but not changed; neither sibling-owned area nor metric creation sites were edited.

Canary state and lineage are copied at the relay publication boundary. All four
free-text reason/evidence fields hide IP literals and host:port endpoints before
length capping. Public DNS route identity and failure classification remain.
Example: `internal_lan GET https://git.example.test/healthz: no HTTP response: dial tcp [REDACTED_ADDRESS]: connection refused`.
The original payload, including REST/persistence address detail, is unchanged.

## Acceptance evidence

Tests are mapped to R1-R3 in `acceptance_criteria.json`.

Counterfactual checks (temporary mutations restored before final gates):

- Before protecting the newer dependency Gitea token, `TestDependencyGiteaTokenIsRedacted`
  failed: `dependency Gitea config leaked protected representation`.
- Before the canary publication change, `TestRouteCanaryProjectorRedactsInternalLANAddressesOnlyOnRelay`
  failed because kind 30315 contained `192.168.40.10`.
- Adding an unclassified `DBConfig.FutureOpaqueValue` failed with
  `missing or invalid secret classification`.
- Adding a classified secret to previously public `ServerConfig` without safe
  renderers failed with `config subtree leaked a protected representation`.

Final gates (2026-09-25 UTC):

| Gate | Result |
| --- | --- |
| Focused config/db/gitea/redact/service suites | PASS |
| `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` | Exactly 1 existing finding: unused `handleStandbyNodeDefinition`, `internal/controlplane/continuity_definition_handlers.go:42`; exit 1, no new findings |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | PASS; 82 passing packages |
| `make race` | PASS; 82 passing packages; no race reports |
| `git diff --check` | PASS |

## Tooling and handoff

RepoPrompt rejected absolute worktree reads, so filesystem edits used the absolute
worktree path. Main-checkout status was checked early, after the first edit, and
before commit; its pre-existing staged `.beads/issues.jsonl` change was untouched.
Oracle review was attempted but unavailable (`targetBindingMismatch`); review
artifact selection also rejected its own advertised aliases. No independent
Oracle review is claimed.

Jev relevance probes used 20 requests / 130676 input tokens, about $0.0055.
They located the canary publication boundary; one broad credentials/DSN filter
ranked only DSN, so field coverage was established from the complete config leaf
inventory and reflection tests instead of trusting that shortlist.

Issue lifecycle remains with the user. No bd operations, pushes, or merges.
