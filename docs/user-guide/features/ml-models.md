# ML Models

**ML Models** in Bahia provide a generic AI/ML fabric for model registry, recipes, and inference deployment.

## Overview

The ML fabric supports:
- **Model registry** — Track models and versions from various sources
- **Recipes** — Automated workflows (import, convert, deploy)
- **Inference endpoints** — Deploy models for serving
- **Provenance** — Track artifact lineage

## Transport Semantics

The ML web import and deploy forms publish signed Nostr ContextVM commands directly through the browser control-plane transport. Import uses `ml/model-import`; deployment uses `ml/inference-deploy`. Submission is not terminal workflow completion: operators should monitor correlated ContextVM result events, legacy ML result projections where present, and the ML read models listed below.

Browser pinning for an existing endpoint is also signer-first Nostr ingress for the worker placement command. External clients that still require compatibility HTTP can use backend compatibility endpoints, but the Bahia web route no longer depends on them.

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

### Web UI

Open **Inference** in the sidebar (`/ml`) and use the **Import Model** form. It publishes a signed ContextVM `ml/model-import` request.

### From Hugging Face

**Via MCP:**
```json
{
  "tool": "bahia_ml_import_model",
  "arguments": {
    "model": "qwen2.5-coder-32b",
    "model_version": "v1",
    "source": "huggingface",
    "uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct",
    "revision": "abc123...",
    "runtime": "vllm",
    "task": "chat_completions"
  }
}
```

Optional arguments include `artifact`, `tags`, and `idempotency_key`.

**Via Nostr:** publish a ContextVM `ml/model-import` request as kind `25910` (or inside encrypted `1059`/`21059`) and follow the correlated result plus canonical `30900` ML state.

> **NOTE (2026-09-11):** Hugging Face is the documented source family (`source: huggingface`). Earlier versions of this page listed local-file, S3/GCS, and ONNX Hub imports; those are not verified against the current importer.

## Running Recipes

Recipes automate multi-step workflows:

### Example: HF to vLLM Deploy

```json
{
  "tool": "bahia_ml_run_recipe",
  "arguments": {
    "recipe": "recipe:hf-vllm-import-deploy:1",
    "inputs": {
      "model_source": "hf://Qwen/Qwen2.5-Coder-32B-Instruct"
    },
    "parameters": {
      "target_environment": "prod",
      "auto_deploy": true
    },
    "runtime": "vllm"
  }
}
```

Optional arguments include `task`, `tags`, and `idempotency_key`. Recipe names and parameters depend on the recipes published in your fleet.

### Nostr

Publish a ContextVM `ml/recipe-run` request as kind `25910` (or encrypted `1059`/`21059`).

## Deploying Inference

### Creating an Endpoint

In the web UI, use **Deploy Inference Endpoint** on the **Inference** page.

```json
{
  "tool": "bahia_ml_deploy",
  "arguments": {
    "endpoint_id": "<endpoint-uuid>",
    "model_version_id": "<model-version-uuid>",
    "runtime": "vllm",
    "placement": {}
  }
}
```

You can pass `endpoint`/`model_version` references instead of IDs; `runtime_preference`, `tags`, and `idempotency_key` are optional.

### Nostr

Publish a ContextVM `ml/inference-deploy` request as kind `25910` (or encrypted `1059`/`21059`).

### Approving Deployments

If approval is required, use `bahia_assistant_ml_approve_deployment` or publish the ContextVM `ml/inference-approval` request, then follow canonical observables.

### Rolling Back

```json
{
  "tool": "bahia_ml_rollback",
  "arguments": {
    "endpoint_id": "<endpoint-uuid>"
  }
}
```

The ContextVM method is `ml/inference-rollback`.

## Viewing ML State

### Web UI

Navigate to **Inference** in the sidebar (`/ml`, under **Intelligence**):
- **Model Catalog**: Browse the model registry and each model's versions
- **Inference Endpoints**: View endpoints and pin placement
- **Live ML Operations**: Follow in-flight imports, deploys, and rollbacks
- **Import Model** and **Deploy Inference Endpoint** forms

### MCP tools

Use `bahia_ml_list_state` to list inference endpoint state. Use `bahia_ml_get_state` with both `endpoint_id` and `environment_id` for one projected state, and `bahia_ml_get_provenance` with `artifact_id` for provenance edges.

## Read Models (Nostr)

ML state is published as canonical kind `30900` events with `domain=ml`, schema `bahia.cp-state.v1`, and a `legacy_kind` tag identifying the historical read-model kind. Entities include `model`, `model-version`, `dataset`, `recipe`, `recipe-run`, `endpoint`, `endpoint-state`, `evaluation`, `provenance`, and `runtime-capability`.

| Entity | Historical `legacy_kind` |
|--------|--------------------------|
| `model` | `31980` |
| `endpoint-state` | `31986` |
| `runtime-capability` | `31989` (legacy ML capability profile; not a Loom worker advertisement) |

New Loom worker discovery uses kind `10100`. The historical `31980`-`31989` read-model kinds and `38390`-`38399` request/result kinds are migration inventory only; do not publish or subscribe to them directly.

## Nostr Methods

| ContextVM method | Purpose |
|------------------|---------|
| `ml/model-import` | Import a model |
| `ml/recipe-run` | Run a recipe |
| `ml/inference-deploy` | Deploy an inference endpoint |
| `ml/inference-approval` | Approve or reject an inference deployment |
| `ml/inference-rollback` | Roll back an endpoint |

## Runtimes

The ML runtime kinds recognized by Bahia are `vllm`, `ollama`, `llama_cpp`, `onnxruntime`, `rknn_server`, `triton`, `tensorrt_llm`, `torchserve`, `mlserver`, `tensorflow_serving`, `custom_container`, and `external_api`. Actual placement depends on which runtimes workers advertise.

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
