# ML Models

**Inference** (`/ml`) covers model registry records, model versions, recipes, imported artifacts, endpoint deployments, rollback, and provenance.

## Registry

A **model** is the stable identity. A **model version** pins source and immutable model artifact metadata. An **endpoint** binds a version and serving configuration to an environment. A **recipe** describes a governed transformation or deployment workflow.

Registry changes use signed ML intents. MCP exposes:

- `bahia_ml_model_create`, `bahia_ml_model_update`, `bahia_ml_model_delete`
- `bahia_ml_version_create`, `bahia_ml_version_update`, `bahia_ml_version_delete`
- `bahia_ml_endpoint_create`, `bahia_ml_endpoint_update`, `bahia_ml_endpoint_delete`

These tools take the handler's full desired-state JSON in `content`.

## Import and recipes

`bahia_ml_import_model` imports a model source into governed storage. `bahia_ml_run_recipe` starts a registered recipe. Both return bounded admission status; follow the canonical model/version/artifact or recipe-run records for completion and provenance.

Keep provider credentials in secrets. Published provenance should identify the source, immutable digest, recipe, trusted worker, and output artifacts without embedding credentials.

## Deploy and roll back

`bahia_ml_deploy` deploys a model version to an endpoint/environment. `bahia_ml_rollback` selects a known endpoint state. Approval uses `bahia_assistant_ml_approve_deployment` when invoked through the assistant workflow. Deploy and rollback derive organization scope from the endpoint environment.

The deployment is complete only when canonical endpoint state and an authenticated serving observation agree.

## Reads

- `bahia_ml_list_state` — list endpoint/inference state
- `bahia_ml_get_state` — one endpoint and environment
- `bahia_ml_get_provenance` — artifact provenance

## Operational rules

- Pin model inputs and outputs by digest.
- Accept worker results only from configured trusted publishers.
- Review runtime, accelerator, memory, and network requirements before approval.
- Treat stale endpoint evidence as unknown.
- Roll back to a verified version rather than editing observed state.

## Related

- [LLM Routes](llm-routes.md)
- [Workers](workers.md)
- [Artifacts](artifacts.md)
- [Policies](policies.md)
