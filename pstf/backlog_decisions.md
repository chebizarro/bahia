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
