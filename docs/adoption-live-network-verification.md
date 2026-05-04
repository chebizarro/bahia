# Adoption / Import Signer-First Live-Network Verification Matrix

Issue: `bahia-sqfx.5`
Scope: staged/live rollout readiness for signer-first adoption/import and direct-runtime operator flows.

This matrix is the normative production gate.
For execution, use [`adoption-signer-first-operator-checklist.md`](adoption-signer-first-operator-checklist.md). That document is the operator run sheet and evidence template.

Legacy privileged HTTP/NIP-98 verification is compatibility-only and secondary. It is not the primary production gate.

## Gate policy

Operator-only signer-first slices now require three distinct evidence layers:

1. deterministic in-repo verification for implementation behavior;
2. a stored local rehearsal artifact from a Docker+relay signer-first simulation before release approval;
3. staged/live signer-first execution using SF-01 through SF-11 before production enablement.

The local rehearsal artifact is a release gate, not a substitute for staged/live signoff.

## Stage gates

| Stage | Gate | Required result |
| --- | --- | --- |
| 0. Code regression | In-repo automated tests listed below | All pass on the release commit. |
| 0.5 Local rehearsal artifact | Stored Docker+relay signer-first rehearsal evidence captured from the release commit | Release approval stays blocked until a local rehearsal bundle proves scan/import/direct-runtime behavior over relay subscriptions without HTTP polling assumptions. |
| 1. Signer/operator staging | Bahia staging runs with signer-first operator transport enabled, operator pubkeys configured, and signer-capable CLI execution available | Valid signed operator requests are accepted; invalid/unauthorized requests fail closed; `/api/v1/system/info` and relay topology evidence match the release candidate. |
| 2. Managed endpoint staging | At least two `runtime.endpoints.<ref>` aliases are configured, including one remote Docker TLS/mTLS endpoint | Scans/imports use endpoint refs only; no raw Docker host or cert material appears in operator-visible request/result/log/metric surfaces. |
| 3. First workload import | One non-critical Docker-origin workload is imported by explicit selection through the signer-first CLI path | Service, environment, build, artifact, state, runtime observation, audit event, and metrics are all present. |
| 4. Direct runtime validation | Restart/stop/deploy are exercised only against the imported direct-runtime workload through signer-first requests | Actions pass for adopted direct-runtime workloads and fail closed for non-adopted or mismatched-host workloads. |
| 5. Rollback validation | Disable adoption and direct-runtime flags, restart Bahia, and verify rollback/disable procedure | Signer-first execution surface closes; imported state remains inspectable; original workload owner can resume control. |
| 6. Production decision | Review automated results, staged evidence, rollback notes, and caveats | Production remains blocked unless all signer-first rows below pass and compose takeover has explicit signoff if needed. |

## Automated in-repo verification

Run these from the repository root on the release commit.

| Area | Automated check | Pass criteria | Current coverage |
| --- | --- | --- | --- |
| Config gate safety | `go test ./internal/config` | Adoption/direct-runtime cannot be enabled without required signer/operator config and runtime endpoint validation. | Config validation tests |
| Signer-first control-plane operator transport | `go test ./internal/controlplane` | Adoption scan/import and direct-runtime requests publish/correlate correctly, scoped pubkey auth is enforced, and terminal results/status are event-driven. | Reactor/operator action tests |
| Managed endpoint governance | `go test ./internal/service -run 'TestAdoptionService.*Endpoint|TestAdoptionServiceRejectsRaw'` and `go test ./internal/adapters/runtime -run 'Endpoint|TLS|Resolver'` | Endpoint refs resolve server-side; raw hosts are rejected when disabled; Docker and Compose TLS/mTLS settings stay server-managed. | Service, resolver, Docker, and Compose endpoint tests |
| Redaction and secret handling | `go test ./internal/service -run 'TestAdoptionService.*Sensitive|Redacts'` and `go test ./internal/api/handlers -run Adoption` | Sensitive env/label values are absent from scan/import responses and adopted runtime config; imported sensitive env values use Bahia secrets when configured. | Service and handler redaction tests |
| Transactional import and idempotency | `go test ./internal/service -run 'TestAdoptionServiceImportTransactional|TestAdoptionServiceImportRetries|TestAdoptionServiceImportSeeds'` | Failed per-candidate imports leave no partial rows; duplicate/racing imports converge to one canonical service/build/artifact identity. | Service transaction/idempotency tests |
| Direct runtime guardrails | `go test ./internal/service -run RuntimeLifecycle` and `go test ./internal/api/handlers -run RuntimeLifecycle` | Deploy/restart/stop are limited to adopted direct-runtime workloads; failed deploys and secret failures do not mutate desired state. | Runtime lifecycle and handler tests |
| Signer-first operator client contract | `go test ./pkg/client -run 'Operator|SystemInfo|Adoption|RuntimeAction'` | Client publishes signed requests, correlates status/result events, deduplicates replies, validates signatures/timestamps, and reports pre-acceptance failures safely. | `operator_nostr_test.go`, `client_test.go` |
| Signer-first CLI workflow | `go test ./cmd/cli -run 'Operator|Adoption'` | CLI defaults to signer-first operator flow, resolves relays deterministically, keeps fallback explicit, and preserves structured stdout. | `operator_nostr_test.go`, adoption parsing tests |
| Full Go regression | `go test ./...` | All Go packages pass without live network dependencies or sleep-based event completion logic. | Required release gate |

## Manual/staging signer-first verification

These checks intentionally require real staging infrastructure and cannot be safely automated in-repo because they touch live Docker daemons, TLS credential material, real operator identities, runtime logs, relay topology, and rollback ownership. Capture CLI transcripts, selected log excerpts with secrets redacted, metrics snapshots, request/result event IDs, and database row IDs as evidence.

| ID | Scenario | Procedure | Pass criteria | Fail/block criteria |
| --- | --- | --- | --- | --- |
| SF-01 | Signer/operator authorization | Publish malformed, unauthorized, and authorized signer-first requests for scan/import/restart through the staging relay path. | Malformed/unsigned requests are rejected; signed non-operator requests are rejected; signed operator requests are accepted; replies correlate by request event id and requester pubkey. | Any unauthorized request succeeds or correlation cannot be verified. |
| SF-02 | Relay routing and result/status correlation | Run a signer-first operator request and capture request, status, and result events. | Request reaches intended relay path; status/result events correlate by `#e` and `#p`; duplicate or unrelated events are ignored safely; terminal outcome is visible without polling. | Relay path mismatch, bad correlation, or timeout/polling assumptions required for completion. |
| SF-03 | Multi-host managed endpoint scan | Run `bahia adopt scan --target host-a --target host-b`. | Both hosts scan successfully; requests/results use endpoint refs only; no raw Docker host or cert material appears in request/result/log/metric surfaces. | Raw endpoint/credential leakage or incorrect host attribution. |
| SF-04 | Raw-host rejection / compatibility boundary | Run `bahia adopt scan --raw-target breakglass=tcp://127.0.0.1:2375` with and without explicit HTTP fallback approval. | Signer-first path rejects raw-host usage; compatibility mode requires explicit fallback; no unmanaged runtime call occurs when disabled. | Raw host is accepted on signer-first path or fallback occurs silently. |
| SF-05 | Redaction/secret import | Scan/import a workload with known sensitive env and labels. | Scan/import results show only safe values plus redacted key names; imported sensitive env appears only in Bahia secrets and is merged during deploy. | Sensitive values appear in results, logs, runtime config JSON, or unencrypted storage. |
| SF-06 | Transaction rollback | Induce a controlled persistence failure during import. | Import reports failure through signer-first result flow and leaves no partial service/environment/build/artifact/state/observation rows for that candidate. | Partial rows remain after failure. |
| SF-07 | Concurrent duplicate import | Start two signer-first imports of the same selected container at the same time. | Exactly one canonical service/build/artifact identity remains; no duplicates or inconsistent state. | Duplicate identities or inconsistent state. |
| SF-08 | Direct-runtime guardrails | Attempt restart/stop/deploy on the imported workload, a non-adopted service, and an adopted service with mismatched host alias. | Imported workload action succeeds; non-adopted/mismatched cases fail closed and do not call runtime. | Any guardrail bypass or state mutation before failed runtime action. |
| SF-09 | Compose takeover decision | If compose-origin workloads are in scope, scan with takeover disabled, then enable only after operator approval and import one staging compose service. | Disabled policy marks compose candidates non-adoptable; enabled policy is explicitly signed off and restart/deploy semantics are accepted. | Compose takeover occurs without explicit approval or breaks expected compose ownership. |
| SF-10 | Observability/audit | During SF-02 through SF-08, inspect logs, metrics, and correlated event streams. | Typed adoption/runtime events publish after persistence; metrics counters/durations move; logs have request ID, event ID, actor pubkey, endpoint ref, result, and no secrets. | Missing audit evidence, events before persistence, or secret leakage. |
| SF-11 | Rollback/disable | Set `adoption.enabled=false` and `direct_runtime_actions.enabled=false`, restart Bahia, then retry signer-first operator requests and inspect imported state. | Signer-first execution surface closes; existing records remain readable; original Docker/Compose operator can resume workload control. | Execution surface remains active or rollback blocks workload owner recovery. |

## Secondary compatibility-only checks

Run these only if the release owner explicitly requires legacy HTTP compatibility evidence:

- HTTP privileged endpoints still reject `Authorization: Bearer ...` with `401` when auth is enabled.
- Any legacy NIP-98 operator flow under test is documented as compatibility-only.
- Compatibility results do not override signer-first production signoff unless a release requirement explicitly depends on them.

## Signoff record

Before production enablement, record:

- release commit SHA;
- stored local rehearsal artifact bundle location and timestamp;
- output from the automated commands above;
- staged config excerpt with secrets and cert paths redacted;
- relay URLs exercised and signer mode used;
- request event IDs and correlated status/result event IDs for SF rows;
- `/api/v1/system/info` capture and relay `/relay` reachability evidence if applicable;
- manual matrix rows SF-01 through SF-11 with pass/fail, evidence location, and approver;
- explicit decision for compose takeover (`disabled`, `enabled for named services only`, or `not applicable`);
- rollback rehearsal timestamp and approver.

## Production readiness statement

Until all automated and manual signer-first rows pass, adoption/import/direct-runtime is **not ready for production enablement**.
Legacy HTTP/NIP-98 checks may still be captured as compatibility evidence, but they are not the primary rollout gate.
