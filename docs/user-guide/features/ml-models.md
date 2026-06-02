# ML Models

**ML Models** in Bahia provide a generic AI/ML fabric for model registry, recipes, and inference deployment.

## Overview

The ML fabric supports:
- **Model registry** — Track models and versions from various sources
- **Recipes** — Automated workflows (import, convert, deploy)
- **Inference endpoints** — Deploy models for serving
- **Provenance** — Track artifact lineage

## Transport Semantics

The ML web import and deploy forms are **REST-to-Nostr bridge** ingress. The browser submits to `/api/v1/ml/imports` or `/api/v1/ml/deployments`; Bahia validates the HTTP payload, signs the corresponding ML request event (`38394` import or `38391` deploy), publishes it to relays, and returns a `202 Accepted` receipt with `request_event_id`, `request_kind`, `result_kind`, read-model kinds, relay acceptance count, and idempotency metadata.

The HTTP response is only publish acceptance. Completion comes from the correlated terminal Nostr result (`38399` import result, `38396` deploy result, or related ML result kinds) and the ML read models listed below. Browser pinning for an existing endpoint is separate signer-first Nostr ingress for the worker placement command.

Direct Nostr clients and MCP tools may publish/return the same ML command metadata without using the web bridge.

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
  "tool": "bahia_ml_model_import",
  "arguments": {
    "source": "huggingface",
    "uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct",
    "revision": "abc123..."
  }
}
```

**Via Nostr:**
Publish a `38394` MLModelImportRequest:

```json
{
  "kind": 38394,
  "content": {
    "source": {
      "kind": "huggingface",
      "uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct"
    }
  },
  "tags": [
    ["d", "import:qwen-coder"],
    ["source", "huggingface"],
    ["task", "chat_completions"]
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
  "tool": "bahia_ml_recipe_run",
  "arguments": {
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

Publish a `38390` MLRecipeRunRequest:

```json
{
  "kind": 38390,
  "content": {
    "recipe": "recipe:hf-vllm-import-deploy:1",
    "inputs": {
      "model_source": "hf://..."
    },
    "parameters": {
      "target_environment": "prod"
    }
  },
  "tags": [
    ["d", "recipe-run:qwen-prod-20240115"],
    ["recipe", "recipe:hf-vllm-import-deploy:1"],
    ["runtime", "vllm"]
  ]
}
```

## Deploying Inference

### Creating an Endpoint

```json
{
  "tool": "bahia_ml_inference_deploy",
  "arguments": {
    "model_version_id": "mv-123",
    "environment_id": "env-prod",
    "runtime": "vllm",
    "config": {
      "replicas": 2,
      "gpu_type": "a100"
    }
  }
}
```

### Nostr Event

Publish a `38391` MLInferenceDeployRequest.

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
  "tool": "bahia_ml_inference_rollback",
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

### MCP Tools

```json
{
  "tool": "bahia_ml_list_models",
  "arguments": {}
}
```

```json
{
  "tool": "bahia_ml_list_endpoints",
  "arguments": {
    "environment": "prod"
  }
}
```

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
| 31989 | `worker:<pubkey>:ai-capability` | Runtime capabilities |

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
