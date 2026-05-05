# PSTF Backlog Decisions

## 2026-05-04 — Backlog expansion session

Artifacts created:
- `pstf/feature_backlog.json`
- `pstf/feature_backlog.md`

Status:
- Ranked candidate slices identified.
- User promotion selection captured.

User selection:
- `HIVECI_RESULT_INGESTION_PIPELINE`
- `SOUL_FACTORY_PROVISIONING_TRACKING`
- `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` (agent-selected third item)

Selection rationale:
- The user explicitly promoted `HIVECI_RESULT_INGESTION_PIPELINE` and `SOUL_FACTORY_PROVISIONING_TRACKING`.
- The agent selected `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` as the third item because it is the highest-ranked remaining candidate and is a dependency for nearly every relay-backed browser flow.

Deferred for now:
- `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`
- `LLM_ROUTE_RELEASE_DEPLOYMENT`
- `ORG_MEMBERSHIP_INVITES_ENCRYPTED_ADMIN`
- `PAYMENTS_HISTORY_FILTER_EXPORT`
- `ENCRYPTED_DEPLOYMENT_RUN_LOGS`

## 2026-05-04 — Cross-feature HITL policy update

Context:
- `pstf/cross_feature_analysis.md` identified two portfolio-level questions that changed what should be promoted next:
  1. shared transport-policy governance
  2. shared vs domain-specific rollback governance

Questions asked via RepoPrompt ask-user tool:

1. **Transport policy governance**
   - Question: How should transport policy be governed across public browser flows, encrypted browser flows, and compatibility-only operator/browser fallback?
   - User selection: `Create one dedicated shared transport-policy slice now`
   - Normalized decision: `CREATE_SHARED_TRANSPORT_POLICY_SLICE_NOW`

2. **Rollback policy governance**
   - Question: How should rollback target-selection and user-visible semantics be handled across core service deployments and LLM deployments?
   - User selection: `Keep rollback domain-specific and verify each slice separately`
   - Normalized decision: `KEEP_ROLLBACK_DOMAIN_SPECIFIC`

Backlog impact:
- Add inferred candidate `TRANSPORT_POLICY_GOVERNANCE` as a top-tier follow-up slice.
- Do not add a shared rollback-governance slice.
- Keep rollback follow-up work domain-specific under core deployment and LLM deployment slices.
- Priority override for the next backlog pass:
  1. `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
  2. `TRANSPORT_POLICY_GOVERNANCE`
  3. `HIVECI_RESULT_INGESTION_PIPELINE`
  4. `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
  5. `SOUL_FACTORY_PROVISIONING_TRACKING`
  6. `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`
