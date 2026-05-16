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
- `npm run test:e2e -- ml-hf-vllm-fabric-smoke.spec.js` (from `web/`, 2026-05-16)
  - Initial sandboxed run: BLOCKED by macOS Chromium sandbox permission (`MachPortRendezvousServer ... Permission denied`).
  - Unsandboxed rerun exposed the remaining D4 gap: mocked Nostr read-model events used non-NIP-01 event IDs and the app shell overwrote injected E2E bootstrap relay metadata.
  - Repaired `web/tests/e2e/ml-hf-vllm-fabric-smoke.spec.js` to hash mocked events using the NIP-01 serialization and `web/src/app.html` to preserve a pre-injected `window.__BAHIA_BOOTSTRAP__` for E2E bootstrapping.
  - Result after repair: PASS (`1 passed (10.8s)`).
- `npm run build` (from `web/`, 2026-05-16)
  - Result: PASS. Existing warnings observed: `src/routes/policies/+page.svelte` a11y label warning, unused `qrcode` default import warning, and Vite dynamic/static import chunking warning for `src/lib/nostr/client.js`.

## Acceptance Criteria Status

All `AC-HFV-001` through `AC-HFV-005` now have executable D4 harness evidence mapped in `test_matrix.json`. Service-level acceptance evidence passed. UI smoke acceptance is implemented and passed in this environment when Playwright was allowed to launch Chromium outside the sandbox.

## Defects

None recorded for D4 after repairing the UI smoke test fixture/bootstrap gap.

## Ambiguities / Human Decisions Needed

None for D4. Real Hugging Face/vLLM/GPU validation remains outside this fake-only harness.

## Confidence Assessment

High for the fake/no-network D4 acceptance path because targeted service evidence exists, the repaired Playwright UI smoke passes, and the web production build succeeds.

## Recommendation

Keep D4 scoped to fake/no-network acceptance. Do not treat this as RKNN recipe coverage.
