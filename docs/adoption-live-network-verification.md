# Adoption / Import Live-Network Verification Matrix

Issue: `bahia-ejj8`  
Scope: adoption/import and direct-runtime rollout readiness after the hardening work.

This matrix is the production gate. Production enablement remains **blocked** until every automated row is green in CI or the release branch, and every manual staging row has an explicit operator signoff with captured evidence.

For execution, use [`adoption-live-network-operator-checklist.md`](adoption-live-network-operator-checklist.md). That document turns this matrix into an operator/agent run sheet plus evidence template.

## Stage gates

| Stage | Gate | Required result |
| --- | --- | --- |
| 0. Code regression | In-repo automated tests listed below | All pass on the release commit. |
| 1. Auth/governance staging | Bahia staging runs with `auth.enabled=true`, `adoption.enabled=true`, `direct_runtime_actions.enabled=true`, operator allowlists set, and `adoption.allow_raw_docker_hosts=false` | Unauthenticated callers receive 401; Bearer credentials receive 401; valid NIP-98 non-operators receive 403; valid NIP-98 operators reach handlers. |
| 2. Managed endpoint staging | At least two `runtime.endpoints.<ref>` aliases are configured, including one remote Docker TLS/mTLS endpoint | Scans/imports use endpoint refs only; no raw Docker host or cert material appears in API responses, DB runtime config, logs, or metrics labels. |
| 3. First workload import | One non-critical Docker-origin workload is imported by explicit selection | Service, environment, build, artifact, state, runtime observation, audit event, and metrics are all present. |
| 4. Direct runtime action validation | Restart/stop/deploy are exercised only against the imported direct-runtime workload | Actions pass for adopted direct-runtime workloads and fail closed for non-adopted or mismatched-host workloads. |
| 5. Rollback validation | Disable adoption and direct-runtime flags, restart Bahia, and verify operational rollback procedure | Privileged routes return 404; already imported state remains inspectable; original workload owner can resume control. |
| 6. Production decision | Review automated results, staging evidence, rollback notes, and caveats | Production remains blocked unless all rows below are pass and compose takeover has explicit signoff if needed. |

## Automated in-repo verification

Run these from the repository root on the release commit.

| Area | Automated check | Pass criteria | Current coverage |
| --- | --- | --- | --- |
| Config gate safety | `go test ./internal/config` | Adoption/direct-runtime cannot be enabled without API auth and operator allowlists; endpoint refs and TLS cert/key pairs validate before boot. | `TestPrivilegedFeatureValidationRequiresAuthAndOperatorAllowlists`, `TestRuntimeEndpointValidationRequiresKnownRefsAndCompleteTLS` |
| Auth-enabled privileged routing | `go test ./internal/api/router -run 'TestPrivilegedRoutes|TestAdoptionRoutes'` | Disabled routes are absent; unauthenticated callers and Bearer credentials are rejected with 401; valid NIP-98 non-operators are rejected with 403; valid NIP-98 operators reach adoption handlers. | `TestPrivilegedRoutesDisabledByDefault`, `TestPrivilegedRoutesRequireOperatorAccess`, `TestAdoptionRoutesScanManagedEndpointsWithOperatorAuth` |
| Managed endpoint governance | `go test ./internal/service -run 'TestAdoptionService.*Endpoint|TestAdoptionServiceRejectsRaw'` and `go test ./internal/adapters/runtime -run 'Endpoint|TLS|Resolver'` | Endpoint refs resolve server-side; raw hosts are rejected when disabled; Docker and Compose TLS/mTLS settings are represented without client-owned endpoints. | Service, resolver, Docker, and Compose endpoint tests |
| Redaction and secret handling | `go test ./internal/service -run 'TestAdoptionService.*Sensitive|Redacts'` and `go test ./internal/api/handlers -run Adoption` | Sensitive env/label values are absent from scan/import responses and adopted runtime config; imported sensitive env values use Bahia secrets when configured. | Service and handler redaction tests |
| Transactional import and idempotency | `go test ./internal/service -run 'TestAdoptionServiceImportTransactional|TestAdoptionServiceImportRetries|TestAdoptionServiceImportSeeds'` | Failed per-candidate imports leave no partial rows; duplicate/racing imports converge to one canonical service/build/artifact identity. | Service transaction/idempotency tests |
| Direct runtime guardrails | `go test ./internal/service -run RuntimeLifecycle` and `go test ./internal/api/handlers -run RuntimeLifecycle` | Deploy/restart/stop are limited to adopted direct-runtime workloads; failed deploys and secret failures do not mutate desired state. | Runtime lifecycle and handler status tests |
| Client contract | `go test ./pkg/client -run 'Adoption|RuntimeAction|Privileged|NIP98'` | Client sends stable DTOs, preserves redaction metadata, signs privileged adoption/import/runtime action calls with fresh NIP-98 headers, and decodes action responses. | NIP-98 authorization-provider tests plus adoption/import/runtime client tests |
| CLI operator workflow parsing | `go test ./cmd/cli -run Adoption` | CLI parsing helpers accept endpoint refs, separate raw-host compatibility mode, and parse selections deterministically. End-to-end Cobra execution against a live Bahia API remains part of the manual staging workflow. | `TestParseAdoptionTargets`, `TestParseAdoptionSelections` |
| Full Go regression | `go test ./...` | All Go packages pass without race-prone sleeps or external live network dependencies. | Required release gate |

## Manual/staging live-network verification

These checks intentionally require real staging infrastructure and cannot be safely automated in-repo because they touch live Docker daemons, TLS credential material, real operator identities, runtime logs, and rollback ownership. Capture command transcripts, selected log excerpts with secrets redacted, metrics snapshots, and database row IDs as evidence.

| ID | Scenario | Procedure | Pass criteria | Fail/block criteria |
| --- | --- | --- | --- | --- |
| LN-01 | Auth-enabled operator access | With NIP-98-only auth enabled, call scan/import/restart as no credential, with `Authorization: Bearer ...`, with a valid NIP-98 non-operator credential, and with a valid NIP-98 operator credential. Also exercise the same path through the production ingress/proxy URL. | 401 for no credential, 401 for Bearer, 403 for valid NIP-98 non-operator, and operator reaches the configured flow. Signed NIP-98 requests validate end-to-end against the externally visible scheme/host/path/query. | Any unauthenticated, Bearer, or non-operator privileged action succeeds, or NIP-98 production auth cannot be validated end-to-end. |
| LN-02 | Multi-host managed endpoint scan | Configure at least two endpoint refs and run `bahia adopt scan --target host-a --target host-b` against the staging Bahia API. | Both hosts scan successfully; response target objects contain endpoint refs and not raw Docker hosts; logs include actor and aliases only. | Raw endpoint/credential leakage, broad local filtering, or incorrect host attribution. |
| LN-03 | Remote Docker TLS/mTLS | Use a TLS-protected remote Docker endpoint with CA/client cert/key files. | Scan/import succeeds through server-managed TLS; cert/key paths and contents never appear in client responses or metrics labels. | TLS bypass required, plaintext Docker endpoint required, or cert material leaks. |
| LN-04 | Raw-host rejection | With `allow_raw_docker_hosts=false`, run `bahia adopt scan --raw-target breakglass=tcp://127.0.0.1:2375`. | Request fails with a policy error and no Docker call is made. | Raw host is accepted outside break-glass policy. |
| LN-05 | Redaction/secret import | Scan/import a workload with known sensitive env and labels. | Scan/import responses show only safe values plus redacted key names; imported sensitive env appears only in Bahia secrets and is merged during deploy. | Any sensitive value appears in API response, logs, runtime config JSON, or unencrypted storage. |
| LN-06 | Transaction rollback | Induce a controlled persistence failure in staging or a disposable DB clone during import. | Import reports failure and leaves no service/environment/build/artifact/state/observation partial rows for that candidate. | Partial rows remain after failure. |
| LN-07 | Concurrent duplicate import | Start two operator imports of the same selected container at the same time. | Exactly one canonical service/build/artifact identity remains; one or both calls may report update/created but no duplicates exist. | Duplicate service/build/artifact identities or inconsistent state. |
| LN-08 | Direct-runtime guardrails | Attempt restart/stop/deploy on the imported workload, a non-adopted service, and an adopted service with mismatched host alias. | Imported workload action succeeds; non-adopted/mismatched cases fail with conflict and do not call runtime. | Any guardrail bypass or state mutation before failed runtime action. |
| LN-09 | Compose takeover decision | If compose-origin workloads are in scope, scan with takeover disabled, then enable only after operator approval and import one staging compose service. | Disabled policy marks compose candidates non-adoptable; enabled policy is explicitly signed off and restart/deploy semantics are accepted. | Compose takeover occurs without explicit approval or breaks expected compose ownership. |
| LN-10 | Observability/audit | During LN-02 through LN-08, inspect logs, metrics, and event subscribers. | Typed adoption/runtime events publish after persistence; metrics counters/durations move; logs have request ID, actor, endpoint ref, result, and no secrets. | Missing audit evidence, events before persistence, or secret leakage. |
| LN-11 | Rollback/disable | Set `adoption.enabled=false` and `direct_runtime_actions.enabled=false`, restart Bahia, then call privileged routes and inspect existing state. | Routes return 404; existing records remain readable; original Docker/Compose operator can resume workload control. | Routes still active or rollback blocks workload owner recovery. |

## Signoff record

Before production enablement, record:

- release commit SHA;
- output from the automated commands above;
- staging config excerpt with secrets and cert paths redacted;
- endpoint refs exercised and workload IDs imported;
- manual matrix rows LN-01 through LN-11 with pass/fail, evidence location, and approver;
- explicit decision for compose takeover (`disabled`, `enabled for named services only`, or `not applicable`);
- rollback rehearsal timestamp and approver.

## Production readiness statement

Until all automated and manual rows pass, adoption/import is **not ready for production enablement**. If any live-network row is skipped, production remains blocked unless the release owner documents why the scenario is not applicable to the target environment and obtains operator signoff.
