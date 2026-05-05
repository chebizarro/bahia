# PSTF Coverage Heatmap

Date: 2026-05-04  
Agent: CoverageHeatmapAgent

## Summary

This heatmap is product coverage, not line coverage.

A capability is only `proven` when the required PSTF artifacts exist, verification passed, confidence clears threshold, major critic/cross-feature blockers are cleared, and final HITL approval exists.

Current portfolio result:
- `proven`: 0
- `partial`: 6
- `blocked`: 2
- `unmapped`: 7

This means the repo now has several strong feature slices, but no major capability family is yet clean enough to call fully proven at the product-system level.

## Heatmap

| Row | Domain | Capability | Feature slices | Spec | AC | Tests | Verification | Confidence | Critic | Cross-feature | HITL | Overall |
| --- | --- | --- | --- | --- | --- | --- | --- | ---: | --- | --- | --- | --- |
| HEAT-001 | Shared control-plane foundations | System discovery / feature gating | `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` backlog only | missing | missing | missing | not_run | null | unknown | high | missing | unmapped |
| HEAT-002 | Shared control-plane foundations | Relay read-model UI | `CORE_SERVICE_TO_DEPLOYMENT`, `LLM_ROUTE_RELEASE_DEPLOYMENT` | draft | partial | partial | partial | null | unknown | high | missing | partial |
| HEAT-003 | Deployment core | Core deployment registry / public service-to-deployment flow | `CORE_SERVICE_TO_DEPLOYMENT` | approved | complete | complete | passing | null | unknown | high | missing | partial |
| HEAT-004 | Sensitive browser transport | Encrypted browser operations transport family | `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` + backlog domains | draft | partial | partial | partial | null | unknown | high | pending | partial |
| HEAT-005 | Sensitive browser transport | Notifications | `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` | approved | complete | complete | passing | null | unknown | medium | approved_with_risk | partial |
| HEAT-006 | Sensitive browser transport | Payments history / filter / export | `PAYMENTS_HISTORY_FILTER_EXPORT` backlog only | missing | missing | missing | not_run | null | unknown | high | missing | unmapped |
| HEAT-007 | Operator control plane | Signer-first operator flows | `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME` | approved | complete | complete | passing | null | unknown | medium | approved_with_risk | partial |
| HEAT-008 | LLM control plane | LLM route / release / deploy / approval / rollback | `LLM_ROUTE_RELEASE_DEPLOYMENT` | draft | partial | partial | partial | 0.93* | medium | high | approved* | partial |
| HEAT-009 | Soul Factory | Soul Factory provisioning / tracking / soul actions | `SOUL_FACTORY_PROVISIONING_TRACKING` backlog only | missing | missing | missing | not_run | null | unknown | high | missing | unmapped |
| HEAT-010 | Artifact / CI pipeline | OCI registry + Hive-CI bridge | `HIVECI_RESULT_INGESTION_PIPELINE` backlog only | missing | missing | missing | not_run | null | unknown | high | missing | unmapped |
| HEAT-011 | Sensitive browser transport | Encrypted service secrets CRUD / reveal | `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL` backlog only | missing | missing | missing | not_run | null | unknown | medium | missing | unmapped |
| HEAT-012 | Sensitive browser transport | Encrypted org membership / invites / admin | `ORG_MEMBERSHIP_INVITES_ENCRYPTED_ADMIN` backlog only | missing | missing | missing | not_run | null | unknown | medium | missing | unmapped |
| HEAT-013 | Deployment core | Encrypted deployment run logs | `ENCRYPTED_DEPLOYMENT_RUN_LOGS` backlog only | missing | missing | missing | not_run | null | unknown | high | missing | unmapped |
| HEAT-014 | Shared control-plane governance | Public vs encrypted vs compatibility fallback transport policy | inferred only | missing | missing | missing | not_run | null | unknown | high | pending | blocked |
| HEAT-015 | Shared deployment governance | Rollback policy across deployment domains | inferred + `LLM_ROUTE_RELEASE_DEPLOYMENT` dependency | draft | partial | missing | not_run | null | unknown | high | pending | blocked |

Notes:
- `0.93*` for HEAT-008 applies only to the approved **non-rollback** LLM slice.
- `approved*` for HEAT-008 also applies only to the approved **non-rollback** LLM slice.

## Row Details

### HEAT-001 — System discovery / feature gating
- Why not proven:
  - no standalone PSTF slice yet
  - no ACs, matrix, verification, or confidence artifact
- Evidence:
  - `pstf/product_map.md` lists this as a major capability
  - `pstf/feature_backlog.md` ranks `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` first
  - `pstf/cross_feature_analysis.md` XFA-001 identifies it as the shared gate for multiple verified slices
- Next actions:
  - `create_feature_slice`
  - `run_spec_reconstruction`
  - `generate_acceptance_criteria`
  - `generate_test_matrix`

### HEAT-002 — Relay read-model UI
- Why partial:
  - covered indirectly by verified core and LLM slices
  - not yet owned by a dedicated approved shared slice
- Evidence:
  - `CORE_SERVICE_TO_DEPLOYMENT` verifies relay-backed deployment pages
  - `LLM_ROUTE_RELEASE_DEPLOYMENT` verifies relay-backed LLM route/read-model state
  - `pstf/spec_gap_report.md` says relay-read-model-first frontend behavior is under-specified
- Next actions:
  - `create_feature_slice`
  - `generate_acceptance_criteria`
  - `add_cross_feature_test`

### HEAT-003 — Core deployment registry / public service-to-deployment flow
- Why partial:
  - feature-local verification is strong
  - no confidence artifact
  - no final HITL approval artifact
  - major cross-feature gaps remain around discovery, run logs, rollback governance, and upstream artifact provenance
- Evidence:
  - `pstf/features/CORE_SERVICE_TO_DEPLOYMENT/verification_report.md`
  - `pstf/cross_feature_analysis.md` XFA-001, XFA-004, XFA-005, XFA-006
- Next actions:
  - `request_hitl_approval`
  - `run_confidence_scoring`
  - `run_critic_review`
  - `resolve_blocker`

### HEAT-004 — Encrypted browser operations transport family
- Why partial:
  - notifications prove one encrypted domain end to end
  - the transport family itself is not yet specified and approved once across secrets, orgs, payments, and run logs
- Evidence:
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `pstf/cross_feature_analysis.md` XFA-003
- Next actions:
  - `request_hitl_approval`
  - `create_feature_slice`
  - `add_cross_feature_test`

### HEAT-005 — Notifications
- Why partial:
  - slice verification is complete and HITL approved with risk
  - no confidence report exists
  - no critic review exists
  - transport-family policy is still unresolved at the cross-feature level
- Evidence:
  - `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/verification_report.md`
  - `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/hitl_decisions.md`
- Next actions:
  - `run_confidence_scoring`
  - `run_critic_review`
  - `run_cross_feature_analysis`

### HEAT-006 — Payments history / filter / export
- Why unmapped:
  - product capability exists
  - backlog candidate exists
  - no PSTF slice exists yet
- Evidence:
  - `pstf/product_map.md`
  - `pstf/feature_backlog.md`
  - `pstf/spec_gap_report.md` risky behavior #3
- Next actions:
  - `create_feature_slice`
  - `run_spec_reconstruction`
  - `generate_acceptance_criteria`
  - `generate_test_matrix`

### HEAT-007 — Signer-first operator flows
- Why partial:
  - slice verification is strong and HITL approved with risk
  - no confidence report exists
  - no critic review exists
  - system-discovery dependency remains unresolved at the portfolio level
- Evidence:
  - `pstf/features/SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME/verification_report.md`
  - `pstf/features/SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME/hitl_decisions.md`
- Next actions:
  - `run_confidence_scoring`
  - `run_critic_review`
  - `resolve_blocker`

### HEAT-008 — LLM control plane
- Why partial:
  - non-rollback slice is approved, verified, and above confidence threshold
  - rollback remains explicitly deferred
  - cross-feature risk remains high because discovery, transport governance, and rollback governance are still unresolved
- Evidence:
  - `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/confidence_report.json`
  - `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/hitl_decisions.md`
  - `pstf/cross_feature_analysis.md` XFA-001, XFA-002, XFA-003, XFA-005
- Next actions:
  - `resolve_blocker`
  - `request_hitl_approval`
  - `run_cross_feature_analysis`

### HEAT-009 — Soul Factory provisioning / tracking / soul actions
- Why unmapped:
  - capability exists and is promoted in backlog
  - no PSTF slice exists yet
  - product risk includes timeout-based failure behavior conflicting with repo event-driven guardrails
- Evidence:
  - `pstf/product_map.md`
  - `pstf/backlog_decisions.md`
  - `pstf/spec_gap_report.md`
- Next actions:
  - `create_feature_slice`
  - `run_spec_reconstruction`
  - `generate_acceptance_criteria`
  - `resolve_blocker`

### HEAT-010 — OCI registry + Hive-CI bridge
- Why unmapped:
  - capability exists
  - backlog promotion exists
  - end-to-end proof is still explicitly thin
- Evidence:
  - `pstf/product_map.md`
  - `pstf/backlog_decisions.md`
  - `pstf/cross_feature_analysis.md` XFA-006
- Next actions:
  - `create_feature_slice`
  - `run_spec_reconstruction`
  - `generate_acceptance_criteria`
  - `generate_test_matrix`

### HEAT-011 / HEAT-012 / HEAT-013 — Sensitive encrypted follow-up domains
These are all `unmapped` because each capability has concrete backlog and implementation evidence but no PSTF slice yet:
- encrypted service secrets CRUD / reveal
- encrypted org membership / invites / admin
- encrypted deployment run logs

Most urgent among these is `ENCRYPTED_DEPLOYMENT_RUN_LOGS` because it is already part of the deployment user journey and was elevated by cross-feature analysis as a live handoff gap.

### HEAT-014 — Shared transport policy
- Why blocked:
  - this is no longer just documentation debt
  - future slices and current release planning need one decision on how public signer-first, encrypted browser transport, and compatibility fallback are governed
- Evidence:
  - `pstf/cross_feature_analysis.md` XFA-003
  - `web/src/lib/auth/route-access.js` has compatibility hooks but no approved compatibility map
- Next actions:
  - `request_hitl_approval`
  - `create_feature_slice`
  - `run_spec_reconstruction`

### HEAT-015 — Rollback policy across deployment domains
- Why blocked:
  - LLM rollback is explicitly deferred pending approved semantics
  - core deployment rollback remains future follow-up
  - the gap is now portfolio-wide
- Evidence:
  - `pstf/cross_feature_analysis.md` XFA-005
  - `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/hitl_decisions.md`
  - `pstf/features/CORE_SERVICE_TO_DEPLOYMENT/verification_report.md`
- Next actions:
  - `request_hitl_approval`
  - `resolve_blocker`
  - `create_feature_slice`

## Backlog Feed

These items should be added or reprioritized in `pstf/feature_backlog.json`:

1. `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` — keep at the top
   - reason: highest-centrality shared contract; currently gates multiple approved slices

2. `ENCRYPTED_DEPLOYMENT_RUN_LOGS` — raise priority
   - reason: this is part of the deployment journey and is now a proven cross-feature gap, not just a deferred detail

3. `HIVECI_RESULT_INGESTION_PIPELINE` — keep promoted
   - reason: deployment-facing artifact/build assumptions remain under-proven upstream

4. `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL` — keep high
   - reason: strong evidence, sensitive domain, still unmapped

5. `TRANSPORT_POLICY_GOVERNANCE` — add as inferred candidate
   - reason: public/encrypted/fallback governance is now a portfolio-level decision surface

6. `ROLLBACK_POLICY_GOVERNANCE` — add as inferred candidate
   - reason: rollback semantics are now cross-domain policy debt, not only an LLM-local follow-up

## Recommended Prompt Follow-Up

Because there are still high-priority unmapped and blocked capabilities, the repo should likely run:
- `prompts/repoprompt/10_feature_backlog_expansion.md`

But do that after:
1. HITL on shared transport policy
2. HITL on rollback policy across deployment domains

## Bottom Line

The portfolio now has good local slice evidence.

What it does not yet have is clean coverage of the shared contracts that tie those slices together.

The next bottlenecks are:
- system discovery,
- transport governance,
- deployment run-log handoff,
- rollback governance,
- and Hive-CI artifact provenance.
