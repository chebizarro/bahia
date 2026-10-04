# ML Models

`ml/pin` is fleet-operator desired state on `endpoint:<endpoint_id>`. It sets `placement_policy.pinned_worker` on the canonical inference endpoint, guarded by the endpoint's `expected_updated_at` revision. See the [D80 fixture](../../../web/tests/fixtures/d80-intent-content.json).

**ML Models** in Bahia provide a generic AI/ML fabric for model registry, recipes, and inference deployment.

## Overview

The ML fabric supports:
- **Model registry** — Track models and versions from various sources
- **Recipes** — Automated workflows (import, convert, deploy)
- **Inference endpoints** — Deploy models for serving
- **Provenance** — Track artifact lineage

## Transport Semantics

The ML registry forms publish signed kind-`30900` intents for model, version, and endpoint create, update, and delete. Updates can change identity fields; the daemon tombstones the old canonical coordinate before publishing the replacement. Deletes and updates carry the selected record's canonical `updated_at` revision. The pending overlay clears only after a scoped `30315` acceptance or a newer canonical event. Import, recipe apply/run, inference deploy/approval/rollback, worker pinning, and the assistant ML tools also publish signed `30900` intents. Submission is not terminal workflow completion: follow the requester-scoped `30315` status and canonical ML read models below.

Browser pinning for an existing endpoint updates desired worker placement through `ml/pin` with the endpoint's canonical revision. External clients that still require compatibility HTTP can use backend compatibility endpoints, but the Bahia web route no longer depends on them.

## Key Concepts

### Model

A **Model** is a machine learning model definition:

```yaml
slug: "qwen2.5-coder-32b"
source:
  kind: "huggingface"
  uri: "hf://Qwen/Qwen2.5-Coder-32B-Instruct"
```

### Model Version

A **Model Version** is an immutable snapshot:

```yaml
model_slug: "qwen2.5-coder-32b"
version: "v1"
source:
  revision: "abc123..."
runtime_requirements:
  preferred_runtimes: ["vllm"]
  min_vram_gb: 48
```

### Recipe

A **Recipe** is an automated workflow:

```yaml
name: "hf-vllm-import-deploy"
steps:
  - import from Hugging Face
  - convert for vLLM
  - deploy to endpoint
```

### Inference Endpoint

An **Inference Endpoint** serves model predictions:

```yaml
name: "qwen-coder"
environment: "prod"
model_version: "qwen2.5-coder-32b:v1"
runtime: "vllm"
```

## Importing Models

### From Hugging Face

**Via MCP:**
```json
{
  "tool": "bahia_ml_import_model",
  "arguments": {
    "org_id": "018f6a60-0000-7000-8000-000000000001",
    "model": "model:qwen-coder",
    "source": "huggingface",
    "source_uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct",
    "revision": "abc123..."
  }
}
```

**Via Nostr:**
Publish a kind-`30900` `ml/model-import` intent with a client UUIDv7 `intent_id`:

```json
{
  "kind": 30900,
  "content": {
    "model": "model:qwen-coder",
    "source": "huggingface",
    "source_uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct",
    "revision": "abc123..."
  },
  "tags": [
    ["d", "model:qwen-coder"],
    ["domain", "ml"],
    ["op", "model-import"],
    ["t", "bahia-intent"],
    ["org", "018f6a60-0000-7000-8000-000000000001"],
    ["intent_id", "018f6a60-0000-7000-8000-000000000020"]
  ]
}
```

### From Other Sources

- **Local files** — Upload model artifacts
- **S3/GCS** — Import from cloud storage
- **ONNX Hub** — Import ONNX models

## Running Recipes

Recipes automate multi-step workflows:

### Example: HF to vLLM Deploy

```json
{
  "tool": "bahia_ml_run_recipe",
  "arguments": {
    "org_id": "018f6a60-0000-7000-8000-000000000001",
    "recipe": "recipe:hf-vllm-import-deploy:1",
    "inputs": {
      "model_source": "hf://Qwen/Qwen2.5-Coder-32B-Instruct"
    },
    "parameters": {
      "target_environment": "prod",
      "auto_deploy": true
    }
  }
}
```

### Nostr Event

Publish a kind-`30900` `ml/recipe-run` intent with the canonical recipe UUID:

```json
{
  "kind": 30900,
  "content": {
    "recipe_id": "018f6a60-0000-7000-8000-000000000006",
    "inputs": {
      "model_source": "hf://..."
    },
    "parameters": {
      "target_environment": "prod"
    }
  },
  "tags": [
    ["d", "recipe-run:018f6a60-0000-7000-8000-000000000006"],
    ["domain", "ml"],
    ["op", "recipe-run"],
    ["t", "bahia-intent"],
    ["org", "018f6a60-0000-7000-8000-000000000001"],
    ["intent_id", "018f6a60-0000-7000-8000-000000000022"]
  ]
}
```

## Deploying Inference

### Creating an Endpoint

```json
{
  "tool": "bahia_ml_deploy",
  "arguments": {
    "endpoint_id": "018f6a60-0000-7000-8000-000000000003",
    "model_version_id": "018f6a60-0000-7000-8000-000000000004",
    "runtime_preference": "vllm"
  }
}
```

### Nostr Event

Publish a kind-`30900` `ml/inference-deploy` intent at `inference-deploy:<endpoint UUID>` with `endpoint_id` and `model_version_id` in content. The MCP tool returns accepted or pending intent correlation; follow the bounded kind-`30315` status and canonical ML state for completion.

### Approving Deployments

If approval is required:

```json
{
  "kind": 38392,
  "content": {
    "deployment_id": "dep-123",
    "approved": true
  }
}
```

### Rolling Back

```json
{
  "tool": "bahia_ml_rollback",
  "arguments": {
    "endpoint": "endpoint:qwen-coder:prod"
  }
}
```

## Viewing ML State

### Web UI

Navigate to **ML** in the sidebar:
- **Models**: Browse model registry
- **Endpoints**: View inference endpoints
- **Recipes**: See available recipes

### MCP tools

Use `bahia_ml_list_state` to list inference endpoint state. Use `bahia_ml_get_state` with both `endpoint_id` and `environment_id` for one projected state, and `bahia_ml_get_provenance` with `artifact_id` for provenance edges.

## Read Models (Nostr)

| Kind | d-tag | Content |
|------|-------|---------|
| 31980 | `model:<slug>` | Model registry |
| 31981 | `model-version:<slug>:<version>` | Model version |
| 31983 | `recipe:<name>:<version>` | Recipe registry |
| 31984 | `recipe-run:<run-id>` | Recipe run state |
| 31985 | `endpoint:<name>:<env>` | Endpoint registry |
| 31986 | `endpoint-state:<name>:<env>` | Endpoint state |
| 31988 | `artifact:<sha256>` | Provenance graph |
| 31989 | `worker:<pubkey>:ai-capability` | Legacy ML runtime-capability profile; not a Loom worker advertisement |

New Loom worker discovery uses kind `10100`. Current projected worker state uses canonical kind `30900`; kind `31989` remains a compatibility input for legacy ML capability data.

## Nostr Event Kinds

| Kind | Name | Description |
|------|------|-------------|
| 38390 | MLRecipeRunRequest | Run a recipe |
| 38391 | MLInferenceDeployRequest | Deploy inference |
| 38392 | MLInferenceDeploymentApproval | Approve deploy |
| 38393 | MLInferenceRollbackRequest | Rollback |
| 38394 | MLModelImportRequest | Import model |
| 38395 | MLRecipeRunResult | Recipe result |
| 38396 | MLInferenceDeployResult | Deploy result |
| 38397 | MLInferenceDeploymentApprovalResult | Approval result |
| 38398 | MLInferenceRollbackResult | Rollback result |
| 38399 | MLModelImportResult | Import result |

## Runtimes

Supported inference runtimes:

| Runtime | Use Case |
|---------|----------|
| **vLLM** | High-throughput LLM serving |
| **ONNX** | Cross-platform inference |
| **RKNN** | Edge deployment (Rockchip NPU) |
| **TensorRT** | NVIDIA optimized inference |

## Best Practices

1. **Version models** — Track source revisions
2. **Use recipes** — Automate repetitive workflows
3. **Track provenance** — Know where artifacts came from
4. **Test locally first** — Verify before production deploy
5. **Monitor endpoints** — Watch for latency/errors

## Troubleshooting

### Import Failed

- Check source URI is valid
- Verify network connectivity
- Check storage availability

### Recipe Stuck

- Check recipe run status
- View recipe run logs
- Verify worker availability

### Endpoint Not Serving

- Check deployment status
- Verify runtime requirements (GPU, memory)
- Check worker health

## Related

- [LLM Routes](llm-routes.md) — LLM-specific routing
- [Workers](workers.md) — ML execution hosts
- [Artifacts](artifacts.md) — Container artifacts

## Signed ML registry intents

Fleet operators can publish `bahia.intent.ml.v1` kind-30900 intents for model, model-version, and inference-endpoint create/update/delete. The daemon writes the durable local registry, publishes canonical records through `MLCanonicalPublisher`, and emits bounded kind-30315 status. Deletes publish `deleted=true` tombstones. A slug, version identity, or endpoint name/environment change tombstones the old canonical coordinate before publishing the replacement. Model-version records include `updated_at`; update/delete intents may provide the canonical RFC3339 `expected_updated_at` revision. The encrypted registry ContextVM mutation methods are removed.

The additional fleet-operator operations are `model-import` (`d=model:<slug>`, model and optional version desired state), `recipe-apply` (`d=recipe:<name>:<version>`, definition desired state), `recipe-run` (`d=recipe-run:<recipe-id>`), `inference-deploy` (`d=inference-deploy:<endpoint-id>`), `inference-approval` (`d=inference-approval:<deployment-intent-id>`, `decision=approve|reject`), and `inference-rollback` (`d=inference-rollback:<endpoint-id>`). The latter four are requests: the daemon authors the run, deployment intent, approval state, or rollback intent and returns its ID in requester-scoped `30315` status `data`. Import accepts `model`, `source`, `source_uri` (or `uri`), and optional `revision`/`model_version`; source URI is a registry reference, not a request to download model bytes. Encrypted ML command methods dual-dispatch through these operations while enabled. See the [D79 wire fixtures](../../../web/tests/fixtures/d79-intent-content.json).
