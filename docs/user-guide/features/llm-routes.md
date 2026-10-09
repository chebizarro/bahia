# LLM Routes

**LLM** (`/llm`) manages model-serving routes, immutable releases, environment deployments, approvals, rollback, and observed serving state. Enable the feature with `llm.enabled` and configure its adapter and protected credentials.

## Routes and releases

A route names the stable inference endpoint and its routing policy. A release pins the model reference and serving configuration used by a deployment. Secret-backed request headers are stored as secret references; values are resolved only at apply time and are not published in route records.

MCP provides:

- Routes: `bahia_llm_list_routes`, `bahia_llm_create_route`, `bahia_llm_update_route`
- Releases: `bahia_llm_register_release`, `bahia_llm_list_releases`
- Operations: `bahia_llm_deploy`, `bahia_llm_approve_deployment`, `bahia_llm_reject_deployment`, `bahia_llm_rollback`
- Assistant receipts: `bahia_assistant_llm_deploy`, `bahia_assistant_llm_approve_deployment`, `bahia_assistant_llm_rollback`

The web controls publish signed LLM intents and follow bounded intent status plus canonical route, release, deployment, and observation records.

**Provisioning is paused.** All seven MCP/assistant lifecycle tools refuse
before submitting an intent because a transport principal is not an operator
signature. The web's genuine operator-signed relay intent is accepted for
validation and receives an explicit paused rejection; it does not create a
deployment, approval, or rollback. The canonical execution snapshot and
restart-safe effect fence are not yet available. Do not treat an old SQL
deployment row or a local-only request event as authorization to provision.

## Deployment

Once provisioning is restored, the workflow is to create a route, register a
release with an immutable model reference, select an environment, submit a
signed deployment, approve it when required, and follow observed serving state
and drift. These steps do not describe a currently executable deployment path.

An accepted request is not proof that the gateway serves the release. Confirm the canonical deployment state and an observation from the configured adapter. Rollback selects a known release for the same route and environment.

## Gateway credentials

Configure adapter administration through secret-backed files or references. Do not place gateway admin tokens or provider API keys in route content, release metadata, or browser configuration. If the adapter is unavailable, deployment status remains failed or pending according to the returned evidence; Bahia does not invent serving state.

## Troubleshooting

- **No routes:** verify `llm.enabled` and relay catch-up.
- **Deployment pending:** check approval and adapter connectivity.
- **Drift:** compare desired release with the observed route state.
- **Unauthorized:** confirm fleet/operator and organization permissions for the route's environment.

## Related

- [Environments](environments.md)
- [ML Models](ml-models.md)
- [Policies](policies.md)
