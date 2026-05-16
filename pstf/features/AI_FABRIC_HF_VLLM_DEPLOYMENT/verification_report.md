# Verification Report — AI_FABRIC_HF_VLLM_DEPLOYMENT

## Summary

D4 verification harness implemented for the Hugging Face → artifact provenance → GPU/vLLM placement/deploy → Nostr read-model observation → UI smoke acceptance path. The path uses local fakes/mocks only and does not require real Hugging Face, vLLM, GPU, OCI, or relay network access.

## Evidence Added

- `internal/service/ml_inference_provisioning_coordinator_test.go::TestHFVLLMInferenceFabricHarnessUsesFakesForProvenancePlacementDeployAndObservation`
  - registers a fake Hugging Face model/version with license, task, safetensors format, vLLM runtime, and GPU/VRAM requirements;
  - resolves a fake digest-addressed Hugging Face artifact;
  - records matching OCI mirror provenance and worker digest verification;
  - selects a fake GPU worker advertising `vllm`, `safetensors`, `chat_completions`, `gpu_nvidia_cuda`, and 48GB VRAM;
  - provisions through a fake vLLM provisioner, syncs an OpenAI-compatible gateway target, publishes lifecycle status/result hooks, and observes healthy endpoint state suitable for `31986` projection.
- `web/tests/e2e/ml-hf-vllm-fabric-smoke.spec.js`
  - seeds mocked Nostr read-model events for `31980`, `31981`, `31985`, `31986`, `31988`, and `31989`;
  - verifies `/ml` renders the model catalog, Hugging Face source, vLLM model version, OpenAI-compatible endpoint, and healthy deployed state.

## Commands Run

- `gofmt -w internal/service/ml_inference_provisioning_coordinator_test.go`
- `go test ./internal/service -run 'TestHFVLLMInferenceFabricHarnessUsesFakesForProvenancePlacementDeployAndObservation|TestMLInferenceProvisioningCoordinatorProcessOnceSuccessPublishesAndObserves|TestMLPlacementSelectsGPUVLLMWorker|TestMLArtifactResolverSet_HuggingFace|TestMLProvenanceService'`
  - Result: PASS (`ok github.com/openagentsinc/bahia/internal/service 0.223s`).
- `npm run test:e2e -- ml-hf-vllm-fabric-smoke.spec.js`
  - Initial sandboxed run: BLOCKED by macOS Chromium sandbox permission (`MachPortRendezvousServer ... Permission denied`).
  - Unsandboxed rerun request: rejected by user. UI smoke test file remains added but not executed successfully in this session.

## Acceptance Criteria Status

All `AC-HFV-001` through `AC-HFV-005` now have executable D4 harness evidence mapped in `test_matrix.json`. Service-level acceptance evidence passed. UI smoke acceptance is implemented but execution was blocked by local Playwright launch permissions.

## Defects

None recorded for service D4. UI execution environment is a verification blocker, not a product defect.

## Ambiguities / Human Decisions Needed

None for D4. Real Hugging Face/vLLM/GPU validation remains outside this fake-only harness.

## Confidence Assessment

Medium-high for the service D4 path because targeted tests pass with deterministic fakes. Medium for UI smoke until Playwright can be run in an environment that permits Chromium launch.

## Recommendation

Keep D4 scoped to fake/no-network acceptance. Do not treat this as RKNN recipe coverage.
