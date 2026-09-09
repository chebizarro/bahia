# HIVECI_PRIVATE_BUILD_CONTEXT_STAGING verification

Task: `hiveci-private-build-context-staging-20260909`

## Implementation

- The fleet-owned `hiveci.policies[].build_dependencies` list is the only dependency authority.
- At dispatch, Bahia asks fleet Gitea for each repository's default branch and resolves that branch to a lowercase 40-hex commit before publishing.
- Kind-5100 contains one exact `["dep", name, credential-free HTTPS URL, commit SHA]` tag per authorized dependency.
- Resolution is all-or-nothing and happens before kind-5401/job publication on direct initiation and before job publication for subscribed kind-5401 runs.
- Existing job identity, replay guards, capability requirements, encrypted worker secret delivery, and no-payment behavior are preserved.

## Mutation verification

Each mutation was applied to production behavior, the named focused test was run uncached and observed failing, the source was restored, and the same test was observed passing.

| Guarantee | Mutation | Failing evidence | Restored evidence |
|---|---|---|---|
| Authorized dependency tags are emitted | Suppressed appending validated dependency tags | `dep tags = [], want [[dep cascadia-go ...] [dep drydock ...]]`; status 1 | `ok github.com/openagentsinc/bahia/internal/adapters/loom`; status 0 |
| Unresolvable dependencies fail before publication | Ignored resolver error | `StartHiveCIBuild() error = <nil>`; status 1 | `ok github.com/openagentsinc/bahia/internal/adapters/gitea`; status 0 |
| Floating revisions are rejected | Accepted every non-empty revision | `main`, `v1.2.3`, uppercase SHA, and 39-hex SHA each published; status 1 | `ok github.com/openagentsinc/bahia/internal/adapters/loom`; status 0 |
| Credential-bearing and non-HTTPS URLs are rejected | Accepted every non-empty URL | All six unsafe URL cases published; status 1 | `ok github.com/openagentsinc/bahia/internal/adapters/loom`; status 0 |
| Secrets do not reach logs/errors | Included the invalid clone URL in resolver errors | `dependency credential reached dispatcher logs`; status 1 | `ok github.com/openagentsinc/bahia/internal/app`; status 0 |
| Direct initiation replay is idempotent | Bypassed initiation-store replay return | `replay duplicated Loom dispatch, got 2 submissions`; status 1 | `ok github.com/openagentsinc/bahia/internal/adapters/gitea`; status 0 |
| Subscribed kind-5401 replay is idempotent | Dispatched both new and already-persisted events | `expected: 1`, `actual: 2`; status 1 | `ok github.com/openagentsinc/bahia/internal/adapters/hiveci`; status 0 |

## Final gates

- `GOFLAGS=-buildvcs=false go build ./...` — PASS
- `GOFLAGS=-buildvcs=false go test ./...` — PASS
- `gofmt -l internal cmd` — reports the same 14 unrelated baseline files; no changed Go file is listed:
  - `internal/adapters/harbor/client.go`
  - `internal/adapters/runtime/compose_fragment_apply_test.go`
  - `internal/adapters/runtime/compose_ownership_gate_test.go`
  - `internal/adapters/runtime/kubernetes_desired_state.go`
  - `internal/adapters/runtime/observation_normalizer_test.go`
  - `internal/adapters/runtime/vm/instance.go`
  - `internal/auth/nip05.go`
  - `internal/domain/oci.go`
  - `internal/domain/payment.go`
  - `internal/domain/rollout.go`
  - `internal/domain/secret.go`
  - `internal/repository/nulls.go`
  - `internal/repository/pg_oci_test.go`
  - `internal/soulfactory/nostr_client.go`
