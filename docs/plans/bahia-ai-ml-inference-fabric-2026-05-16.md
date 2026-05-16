# Bahia AI/ML Inference Fabric: Plan

> Status: Draft
> Date: 2026-05-16
> Scope: Refactor Bahia's LLM service into a Nostr-native AI/ML control plane and inference fabric.

## Goal

Evolve Bahia from an LLM deployment surface into a Nostr-native AI/ML control plane for model registry, artifact provenance, recipe workflows, hardware-aware scheduling, training/fine-tuning, inference deployment, evaluation, observability, and edge rollout.

The target product shape is MLflow + LM Studio + Hugging Face + Kubernetes-style scheduling, but with Nostr as the source of truth, external artifact stores, and worker/runtime adapters instead of centralized REST workflow engines.

## Background

Bahia already has most of the substrate for this evolution:

- The LLM provisioning path is wired in `internal/app/app.go:257-299`, with registry, placement, provisioning coordinator, gateway manager, and reconciler.
- Current LLM domain objects in `internal/domain/llm.go:9-193` cover routes, releases, deployment intents/runs, observations, and route state.
- Current LLM and worker HTTP/UI surfaces are wired through `internal/api/router/router.go:224-249`, `internal/api/router/router.go:363-368`, `internal/api/router/router.go:434-446`, `web/src/routes/llm/+page.svelte:1-180`, and `web/src/routes/workers/+page.svelte:1-32`.
- The Nostr substrate already supports OK-aware publishing, subscriptions, EOSE/CLOSED handling, NIP-11 relay capability checks, dedupe, and backoff in `internal/adapters/nostr/relay_pool.go:109-672`, `internal/adapters/nostr/subscriber.go:167-335`, `internal/adapters/nostr/dedup.go:20`, and `internal/adapters/nostr/backoff.go:24`.
- Bahia docs already require signer-first async behavior: publish a request event, subscribe for correlated status/result events, and never treat HTTP as completion truth (`docs/control-planes.md:155-159`, `docs/protocol-compatibility.md:196-203`).
- Existing LLM kind families are `5971-5975`, `6973`, `7971-7973`, and read models `31964/31965` (`docs/control-planes.md:70-153`). Keep those stable as compatibility surfaces.
- Worker discovery already exists through Loom advertisements, parsed at `internal/adapters/nostr/processor.go:271-353`, with worker resources and accelerators modeled in `internal/domain/worker.go:28-65`.
- Related protocols provide useful patterns: Loom worker ads use replaceable `10100` events and addressable `30100` job status; Swarmstr runtime capabilities use addressable `30317`; Blossom/NIP-94 metadata covers `sha256`, size, MIME type, and blob URLs.

External alignment should stay pragmatic: use MLflow concepts for experiments/runs/registry aliases, Hugging Face model/dataset cards for metadata import, OCI 1.1 artifacts/referrers, Blossom/NIP-94, and S3-compatible object stores such as SeaweedFS for artifact publication/mirroring, ONNX/GGUF/Safetensors/RKNN/TensorRT/OpenVINO/TFLite as artifact formats, and runtime adapters for vLLM, llama-server, Ollama, ONNX Runtime, RKNN, Triton, TensorRT-LLM, and custom containers.

## Product Shape

Bahia should become a Nostr-native MLOps fabric, not a monolithic ML platform.

### Core objects

Everything important should be addressable by Nostr event coordinates and persisted in Bahia read models:

- `Model`
- `ModelVersion`
- `Dataset`
- `Artifact`
- `Recipe`
- `RecipeRun`
- `Runtime`
- `Worker`
- `HardwareProfile`
- `InferenceEndpoint`
- `DeploymentIntent`
- `DeploymentRun`
- `Evaluation`
- `Experiment`
- `FineTuneJob`
- `ProvenanceEdge`

### UI areas

1. **Model Explorer** — Hugging Face + LM Studio style catalog. Search/filter by modality, task, format, quantization, runtime compatibility, hardware compatibility, context size, license, source, and provenance health.
2. **Deployment Builder** — visual pipeline: model → version/artifact → recipe → target hardware → runtime → validation → deploy.
3. **Worker Fleet View** — GPU/NPU/CPU resources, runtime support, format support, queue depth, active jobs, deployed endpoints, health, temperature/power when available.
4. **Recipe Editor** — phase-2 visual blocks plus YAML editor, with save/share as Nostr recipe events.
5. **Inference Playground** — phase-2 endpoint testing and benchmark surface.
6. **Experiments/Evaluations** — phase-3 MLflow-inspired runs, metrics, artifacts, lineage, aliases/promotion state, and reports.

## Approach

Add a generic AI/ML core beside the current LLM implementation, then migrate LLM onto it as a compatibility slice. Do not mutate the existing `597x/697x/797x/3196x` LLM semantics into generic AI semantics.

### 1. Add a generic ML domain

Add `internal/domain/ml.go` with closed sets for:

- task kinds: `chat_completions`, `embeddings`, `reranking`, `image_generation`, `vision_inference`, `speech_to_text`, `text_to_speech`, `onnx_inference`;
- artifact kinds: `model`, `adapter`, `dataset`, `tokenizer`, `preprocessor`, `postprocessor`, `container`, `evaluation_report`;
- artifact formats: `huggingface_snapshot`, `safetensors`, `gguf`, `onnx`, `rknn`, `oci_image`, `oci_artifact`, `blossom_blob`, `tensorrt_engine`, `openvino_ir`, `tflite`;
- runtime kinds: `external_api`, `vllm`, `ollama`, `llama_cpp`, `onnxruntime`, `rknn_server`, `triton`, `tensorrt_llm`, `torchserve`, `mlserver`, `tensorflow_serving`, `custom_container`.

Primary records:

- `MLModel`
- `MLModelVersion`
- `MLArtifactRef`
- `MLProvenanceEdge`
- `MLRecipe`
- `MLRecipeRun`
- `MLInferenceEndpoint`
- `MLDeploymentIntent`
- `MLDeploymentRun`
- `MLInferenceObservation`
- `MLInferenceState`
- `MLEvaluationSpec`
- `MLEvaluationRun`

Additive DB tables should mirror these records. During cutover, `MLRegistryService` becomes canonical first; `LLMRegistryService` becomes a read/write façade over ML tables for `task_kind=chat_completions`. Do not dual-write to old and new sources after cutover. The migration should backfill current LLM routes/releases/intents/runs/state into ML tables, run parity checks, then freeze old LLM tables as rollback/reference data. After cutover, rollback requires DB snapshot restore.

### 2. Make recipes first-class

Recipes are the key abstraction. Start with ordered linear recipes, not arbitrary DAGs. This fits the current coordinator/recovery pattern and avoids inventing a workflow engine.

Use YAML for authoring, validate recipes with CUE in phase 1, and store a normalized JSON representation in DB/read models. YAML remains the human-editable format; CUE is the schema/constraint layer for required fields, step contracts, input/output types, retry policy, artifact refs, and runtime capability requirements.

Recipe event/read-model content should include:

```yaml
name: hf-vllm-import-deploy
version: 1
inputs:
  model:
    source: huggingface
    repo: Qwen/Qwen2.5-Coder-32B-Instruct
    revision: <commit-sha>
steps:
  - action: fetch_source
  - action: validate_provenance
  - action: publish_artifact
    targets: [oci, blossom, seaweedfs]
  - action: deploy_endpoint
    runtime: vllm
    target:
      accelerator: gpu_nvidia_cuda
  - action: run_smoke_eval
outputs:
  endpoint:
    protocol: openai-compatible
```

Initial step kinds:

- `fetch_source`
- `validate_provenance`
- `convert_model`
- `quantize_model`
- `package_artifact`
- `publish_artifact`
- `deploy_endpoint`
- `run_smoke_eval`
- `promote`
- `rollback`

Recipe failure contract:

- Each step writes `queued`, `running`, `succeeded`, or `failed` with input/output artifact refs before the next step starts.
- Steps are idempotent by `(recipe_run_id, step_index, input_digest_set)`.
- Retry policy is explicit in recipe YAML; default is no automatic retry except coordinator recovery of a previously `running` step whose worker lease expired.
- Phase 1 has no automatic compensation. Failed runs keep successful outputs for inspection and optional manual resume from the last successful step.
- Terminal failure publishes a result event and updates the `31984` recipe-run state; it does not poll for completion.

### 3. Generalize placement and hardware targeting

Add `internal/service/ml_placement.go`; keep `internal/service/llm_placement.go` as a wrapper for LLM compatibility.

Workers should advertise:

- supported runtimes;
- supported artifact formats;
- supported task kinds/modalities;
- accelerator classes (`gpu_nvidia_cuda`, `cpu_generic`, `npu_rk3588` first);
- GPU/NPU memory and driver versions;
- toolchains (`rknn_toolkit2`, `tensorrt`, `onnxruntime`, etc.);
- conversion capabilities;
- cache/artifact locality;
- queue depth and price.

Placement should filter by explicit selector, task, runtime, artifact format, hardware class, memory, and toolchain, then score by exact runtime match, cached artifact locality, lower queue depth, hardware preference, and lower price. Tie-break on worker pubkey for deterministic results.

### 4. Add generic coordinators

Add:

- `internal/service/ml_registry.go` — canonical lifecycle and state service.
- `internal/service/ml_recipe_coordinator.go` — checkpointed linear recipe execution.
- `internal/service/ml_inference_provisioning_coordinator.go` — deployment run execution.
- `internal/service/ml_provenance_service.go` — artifact lineage and fail-closed validation.

Digest verification ownership:

- Artifact resolvers compute or verify digests at fetch/import time.
- Workers must verify digest before consuming a pulled artifact and include the verified digest in status/result payloads.
- The coordinator compares worker-reported digests against `MLArtifactRef` before advancing the run.
- If OCI, Blossom, SeaweedFS/S3, Hugging Face, GitHub, or local mirrors disagree, the run fails closed and records the mismatch as a provenance defect.

Reuse the current LLM coordinator shape (`internal/service/llm_provisioning_coordinator.go:57-375`) and Bucket 3 recovery/serialization patterns from `internal/service/tool_provisioning_coordinator_test.go`.

Deployment serialization rule: serialize by `(endpoint_id, environment_id)` while allowing different endpoints/environments to run concurrently. Recovery must process same-target stranded work oldest-first.

### 5. Keep Nostr canonical

REST and MCP may exist for compatibility and tooling, but every long-running action must return Nostr correlation metadata and complete through status/result events.

Extend the control-plane reactor/projector/subscriber for new AI/ML kinds. Do not build request/response or polling abstractions around deployments, recipes, or evaluations.

## Nostr Event Namespace

Keep existing LLM kinds stable as the LLM compatibility namespace. Do not use the `5000-7000` range for new Bahia AI/ML events: NIP-90 reserves `5000-5999` for Data Vending Machine job requests, `6000-6999` for job results, and `7000` for feedback (`nips/90.md:13-22`). Introduce a separate AI/ML family using addressable command events and replaceable read models. Treat `38390-38399` and `31980-31989` as a public spec candidate track, but do not let standardization block implementation phases; implementation should document field names, compatibility notes, and migration risks as the candidate namespace evolves.

### Addressable/read-model events

| Kind | Purpose |
| --- | --- |
| `31980` | Model registry/read model |
| `31981` | Model version registry/read model |
| `31982` | Dataset registry/read model |
| `31983` | Recipe registry/read model |
| `31984` | Recipe run state |
| `31985` | Inference endpoint registry |
| `31986` | Inference endpoint state |
| `31987` | Evaluation/experiment state |
| `31988` | Artifact provenance graph |
| `31989` | Runtime/capability profile |

Use `d` tags for stable coordinates such as `model:<slug>`, `model-version:<model-slug>:<version>`, `recipe:<name>:<version>`, `endpoint:<name>:<environment>`, and `artifact:<sha256>`.

### Phase-1 command/result events

Use addressable command/result events with `d=<idempotency-key-or-request-id>` so reconnect/replay can collapse duplicates without polling.

| Kind | Purpose |
| --- | --- |
| `38390` | Recipe run request |
| `38391` | Inference deploy request |
| `38392` | Inference deployment approval/rejection |
| `38393` | Inference rollback request |
| `38394` | Model/model-version import request |
| `38395` | Recipe run terminal result |
| `38396` | Inference deploy terminal result |
| `38397` | Approval/rejection terminal result |
| `38398` | Rollback terminal result |
| `38399` | Model/model-version import terminal result |

Defer dataset import, evaluation, benchmark, fine-tune, and experiment command kinds until after Slice 1 proves the namespace. Evaluation and benchmark state can still appear in `31987` read models once implemented.

Every command/result event must tag:

- `e=<request_event_id>`;
- `p=<requester_pubkey>`;
- `status=<queued|running|succeeded|failed|rejected>`;
- relevant scoped tags: `model`, `version`, `recipe`, `run`, `endpoint`, `environment`, `deployment`, `artifact`, `worker`, `runtime`.

### Worker/runtime capability events

Bahia can ingest current Loom `10100` and Swarmstr `30317`, but should also project normalized Bahia AI capability read models with `31989`.

Example capability tags:

```json
[
  ["d", "worker:<pubkey>:ai-capability"],
  ["role", "worker"],
  ["runtime", "vllm"],
  ["runtime", "onnxruntime"],
  ["artifact_format", "safetensors"],
  ["artifact_format", "onnx"],
  ["task", "chat_completions"],
  ["task", "embeddings"],
  ["accelerator", "gpu_nvidia_cuda"],
  ["gpu", "nvidia-l40s"],
  ["cuda", "12.4"],
  ["ram_gb", "128"],
  ["vram_gb", "48"],
  ["status", "ready"]
]
```

RK3588 example:

```json
[
  ["d", "worker:<pubkey>:ai-capability"],
  ["role", "worker"],
  ["runtime", "rknn_server"],
  ["toolchain", "rknn_toolkit2"],
  ["artifact_format", "onnx"],
  ["artifact_format", "rknn"],
  ["task", "vision_inference"],
  ["accelerator", "npu_rk3588"],
  ["npu", "rk3588"],
  ["ram_gb", "16"],
  ["status", "ready"]
]
```

### Example event shapes

#### `31980` model definition

```json
{
  "kind": 31980,
  "content": {
    "summary": "Qwen coder model for code generation.",
    "card": {},
    "metadata": {}
  },
  "tags": [
    ["d", "model:qwen2.5-coder-32b"],
    ["name", "Qwen2.5-Coder-32B-Instruct"],
    ["family", "qwen"],
    ["modality", "text"],
    ["task", "chat_completions"],
    ["capability", "code"],
    ["license", "apache-2.0"]
  ]
}
```

#### `31981` model version

```json
{
  "kind": 31981,
  "content": {
    "source": {
      "kind": "huggingface",
      "uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct",
      "revision": "<commit-sha>"
    },
    "runtime_requirements": {
      "preferred_runtimes": ["vllm"],
      "min_vram_gb": 48
    },
    "artifacts": [
      {
        "uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct@<commit-sha>",
        "format": "safetensors",
        "sha256": "...",
        "size_bytes": 0
      },
      {
        "uri": "blossom://...",
        "format": "safetensors",
        "sha256": "..."
      }
    ]
  },
  "tags": [
    ["d", "model-version:qwen2.5-coder-32b:v1"],
    ["model", "model:qwen2.5-coder-32b"],
    ["version", "v1"],
    ["format", "safetensors"],
    ["runtime", "vllm"],
    ["sha256", "..."]
  ]
}
```

#### `38390` recipe run request

```json
{
  "kind": 38390,
  "content": {
    "recipe": "recipe:hf-vllm-import-deploy:1",
    "inputs": {
      "model_source": "hf://Qwen/Qwen2.5-Coder-32B-Instruct@<commit-sha>"
    },
    "parameters": {
      "target_environment": "prod",
      "auto_deploy": true
    }
  },
  "tags": [
    ["recipe", "recipe:hf-vllm-import-deploy:1"],
    ["source", "huggingface"],
    ["task", "chat_completions"],
    ["runtime", "vllm"]
  ]
}
```

#### `38391` inference deploy request

```json
{
  "kind": 38391,
  "content": {
    "endpoint": "endpoint:qwen-coder:prod",
    "model_version": "model-version:qwen2.5-coder-32b:v1",
    "runtime_preference": "vllm",
    "placement": {
      "accelerator": "gpu_nvidia_cuda",
      "min_vram_gb": 48
    }
  },
  "tags": [
    ["endpoint", "endpoint:qwen-coder:prod"],
    ["model_version", "model-version:qwen2.5-coder-32b:v1"],
    ["environment", "prod"],
    ["runtime", "vllm"],
    ["accelerator", "gpu_nvidia_cuda"]
  ]
}
```

## Default Vertical Slices

### Slice 1: Hugging Face → GPU → vLLM

1. Search/import a Hugging Face model card and pin a commit revision.
2. Register `MLModel` and `MLModelVersion` with source, license, task, format, and runtime requirements.
3. Resolve artifacts and hashes; optionally mirror to Blossom and/or OCI.
4. Choose the default `hf-vllm-import-deploy` recipe.
5. Select a worker advertising `vllm`, `safetensors` or HF snapshot support, NVIDIA CUDA, and enough VRAM.
6. Provision vLLM and expose the endpoint through the existing gateway path where OpenAI-compatible routing applies.
7. Publish deployment status/result and `31986` endpoint state.
8. Run smoke evaluation and record evaluation/provenance edges.

Acceptance: a user can choose a Hugging Face model and deploy it to a GPU worker through vLLM without leaving the Nostr-native flow.

### Slice 2: GitHub/ONNX → x64 conversion → RK3588/RKNN edge

1. Register a GitHub repo/release asset containing an ONNX vision model at a pinned revision.
2. Validate ONNX metadata, opset, declared inputs/outputs, and license.
3. Run a recipe on an x64 worker advertising `rknn_toolkit2` to convert ONNX → RKNN.
4. Publish `.rknn` artifact, pre/postprocess metadata, calibration details, hashes, and provenance edges.
5. Deploy to a worker advertising `rknn_server`, `npu_rk3588`, and `rknn` artifact support.
6. Expose as raw HTTP first; do not force OpenAI compatibility for vision/NPU inference.
7. Run a sample-image smoke test and publish endpoint state.

Acceptance: a user can pull a model from GitHub, convert/build it in a container, and deploy the resulting RKNN artifact to an RK3588 NPU target through recipe steps.

## Work Items

### Phase 0 — Contract and PSTF gate

- Update `docs/control-planes.md` with phase-1 `38390-38399` command/result kinds and `31980-31989` read models.
- Update `docs/protocol-compatibility.md` to state that REST/MCP are compatibility/tooling surfaces and Nostr remains completion truth.
- Document why new AI/ML events avoid NIP-90's `5000-7000` range.
- Create PSTF artifacts for signer-first protocol, recovery/serialization, fail-closed provenance, HF→vLLM, and ONNX→RKNN.

Gate: event namespace, acceptance criteria, and test matrix are approved before code changes.

### Phase 1 — Generic core plus Hugging Face → vLLM slice

- Add `internal/domain/ml.go` and additive ML migrations/repositories.
- Implement `MLRegistryService`, `MLProvenanceService`, and `MLPlacementService` for model/version/artifact/endpoint/deployment state.
- Backfill LLM tables into ML tables; switch `LLMRegistryService` and `LLMPlacementService` to ML-backed compatibility façades.
- Implement `MLInferenceProvisioningCoordinator` for stale-run recovery, per-target serialization, placement, provision, gate, observe, and command/result publication.
- Extend worker capability ingestion enough for GPU/vLLM placement.
- Add Hugging Face, Blossom, OCI, and SeaweedFS/S3 artifact resolvers, CUE recipe validation, vLLM provisioner wiring, `31980/31981/31985/31986/31989` projection, and `/ml` catalog/deployment basics.

Gate: a Hugging Face model can be registered, provenanced, deployed to vLLM on a GPU worker, observed through Nostr read models, and tested from the UI while existing `/llm` flows still work.

### Phase 2 — Recipes plus GitHub/ONNX → RK3588/RKNN slice

- Implement `MLRecipeCoordinator` with checkpointed linear execution, explicit retry policy, manual resume, and terminal result publication.
- Add job-dispatch/container execution for conversion steps by reusing Loom where possible; do not hide it inside the RKNN adapter.
- Extend worker capability ingestion for `rknn_toolkit2`, `rknn_server`, `npu_rk3588`, ONNX, and RKNN artifact compatibility.
- Add GitHub resolver, ONNX validation, RKNN conversion packaging, RKNN deployment adapter, raw HTTP endpoint state, and sample-image smoke evaluation.
- Add recipe detail/editor basics after the YAML format is stable.

Gate: a GitHub/ONNX model can be converted on an x64 worker, published with provenance, deployed to an RK3588 target, and observed as healthy.

### Phase 3 — Broader fabric

- Add evaluation, benchmark, fine-tuning, dataset, experiment, and promotion flows only after the first two slices validate the namespace and coordinator model.
- Expand UI with inference playground, richer recipe editor, experiment tracking, and comparison views.
- Prepare the public NIP/BUD candidate proposal only after implementation proves stable field names, replay/idempotency behavior, and compatibility guidance.

## Acceptance Criteria

- Existing LLM flows still work through current `/llm` UI, REST routes, and Nostr kinds.
- New AI/ML resources are represented by generic domain records and Nostr read models.
- Long-running model import, recipe, deployment, evaluation, and rollback flows complete through Nostr status/result events, not HTTP polling.
- Recipe runs are checkpointed and recoverable.
- Same endpoint/environment deployments serialize; different targets can run concurrently.
- Deployments fail closed when provenance, digest, runtime, toolchain, or hardware compatibility is incomplete.
- A Hugging Face model can be registered, provenanced, deployed to vLLM on a GPU worker, and tested from the UI.
- A GitHub-hosted ONNX model can be converted to RKNN, published, deployed to an RK3588 worker, and tested from the UI.
- Worker fleet UI exposes enough runtime/hardware/format/toolchain state for an operator to understand placement decisions.
- PSTF feature artifacts map each acceptance criterion to tests and verification evidence.

## Risks and Mitigations

- **Protocol sprawl** — keep LLM kinds stable and isolate generic AI/ML in one documented namespace.
- **Schema cutover risk** — use additive tables and backfill before making ML canonical; rollback requires DB snapshot restore after cutover.
- **Workflow overreach** — begin with linear recipes; add DAGs only after vertical slices prove the model.
- **Worker capability drift** — fail closed unless workers explicitly advertise required runtime, format, hardware, and toolchain capabilities.
- **RKNN complexity** — model conversion and deployment may require separate workers; represent that explicitly in recipes.
- **UI overload** — keep `/llm` focused as compatibility; put the richer fabric in `/ml`.

## Open Questions

- SeaweedFS currently appears in related fleet docs as a planned/re-baseline-needed S3-capable storage backend (`fleet-planning/docs/fleet/network.md:375-387`); before implementation, confirm whether Bahia should target an existing SeaweedFS deployment, TrueNAS/S3-compatible storage, or a future storage service endpoint.

## References

- `docs/architecture.md`
- `docs/control-planes.md`
- `docs/protocol-compatibility.md`
- `docs/designs/nostr-native-system-discovery.md`
- `docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md`
- `docs/soulfactory-runtime-control.md`
- MLflow Model Registry: https://mlflow.org/docs/latest/model-registry/
- Hugging Face model cards: https://huggingface.co/docs/hub/model-cards
- OCI Image/Distribution 1.1 artifacts: https://opencontainers.org/posts/blog/2024-03-13-image-and-distribution-1-1/
- Fleet SeaweedFS planning context: `fleet-planning/docs/fleet/network.md:375-387`
- ONNX IR: https://github.com/onnx/onnx/blob/main/docs/IR.md
- GGUF spec: https://github.com/ggml-org/ggml/blob/master/docs/gguf.md
- vLLM quickstart: https://docs.vllm.ai/en/latest/getting_started/quickstart.html
- llama.cpp server docs: https://www.mintlify.com/ggml-org/llama.cpp/inference/server
- RKNN Toolkit2: https://github.com/airockchip/rknn-toolkit2
