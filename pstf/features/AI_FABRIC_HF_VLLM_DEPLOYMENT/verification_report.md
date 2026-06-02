# Verification Report — AI_FABRIC_HF_VLLM_DEPLOYMENT

## Summary

D4 verification exists for the Hugging Face → artifact provenance → GPU/vLLM placement/deploy → Nostr read-model observation → UI smoke path, but the evidence is **non-production harness evidence**. It uses local fakes/mocks only and does not prove production readiness for real Hugging Face access, vLLM/GPU runtime behavior, OCI/Blossom artifact storage, OpenAI-compatible gateway routing, or live relay behavior.

Production integration verification is tracked by Bead `bahia-jicv`.

## Evidence Classification

### Non-production D4 harness evidence

- `internal/service/ml_inference_provisioning_coordinator_test.go::TestHFVLLMInferenceFabricHarnessUsesFakesForProvenancePlacementDeployAndObservation`
  - registers a fake Hugging Face model/version with license, task, safetensors format, vLLM runtime, and GPU/VRAM requirements;
  - resolves a fake digest-addressed Hugging Face artifact;
  - records fake OCI mirror provenance and worker digest verification;
  - selects a fake GPU worker advertising `vllm`, `safetensors`, `chat_completions`, `gpu_nvidia_cuda`, and 48GB VRAM;
  - provisions through a fake vLLM provisioner, syncs a fake OpenAI-compatible gateway target, publishes lifecycle status/result hooks, and observes mocked endpoint state suitable for `31986` projection.
- `web/tests/e2e/ml-hf-vllm-fabric-smoke.spec.js`
  - seeds mocked Nostr read-model events for `31980`, `31981`, `31985`, `31986`, `31988`, and `31989`;
  - verifies `/ml` renders the model catalog, Hugging Face source, vLLM model version, OpenAI-compatible endpoint, and healthy deployed state from mocked read-model inputs.

### Production evidence

No production evidence is recorded in this PSTF slice. The D4 harness must not be used to claim real external integration readiness or live Nostr relay correctness.

## Commands Run

Historical D4 commands recorded by the original verification slice:

- `gofmt -w internal/service/ml_inference_provisioning_coordinator_test.go`
- `go test ./internal/service -run 'TestHFVLLMInferenceFabricHarnessUsesFakesForProvenancePlacementDeployAndObservation|TestMLInferenceProvisioningCoordinatorProcessOnceSuccessPublishesAndObserves|TestMLPlacementSelectsGPUVLLMWorker|TestMLArtifactResolverSet_HuggingFace|TestMLProvenanceService'`
  - Result: PASS (`ok github.com/openagentsinc/bahia/internal/service 0.223s`).
- `npm run test:e2e -- ml-hf-vllm-fabric-smoke.spec.js` (from `web/`, 2026-05-16)
  - Initial sandboxed run: BLOCKED by macOS Chromium sandbox permission (`MachPortRendezvousServer ... Permission denied`).
  - Unsandboxed rerun exposed mocked-event/bootstrap fixture gaps, which were repaired.
  - Result after repair: PASS (`1 passed (10.8s)`).
- `npm run build` (from `web/`, 2026-05-16)
  - Result: PASS. Existing warnings observed: `src/routes/policies/+page.svelte` a11y label warning, unused `qrcode` default import warning, and Vite dynamic/static import chunking warning for `src/lib/nostr/client.js`.

Item 6 stale-verification cleanup commands:

- `bd prime`
- `bd update bahia-dg3t --claim`
- `bd search "AI fabric production"`, `bd search "Hugging Face vLLM real"`, `bd search "signer-first AI ML"`, `bd search "HF vLLM"`, `bd search "AI_FABRIC_SIGNER_FIRST_PROTOCOL"`, `bd search "AI_FABRIC_HF_VLLM_DEPLOYMENT"`
- `bd create ...` for `bahia-jicv`

## Acceptance Criteria Status

`AC-HFV-001` through `AC-HFV-005` have executable D4 harness evidence mapped in `test_matrix.json`. That evidence verifies local fake/no-network behavior only. Production acceptance remains unverified until `bahia-jicv` records real configured integration evidence or exact external blockers.

## Defects / Remaining Gaps

- `D-HFV-PROD-001` / `bahia-jicv`: production Hugging Face, artifact provenance/storage, GPU/vLLM, gateway, live relay, and Nostr protocol verification is not yet recorded.

## Ambiguities / Human Decisions Needed

None for labeling the D4 harness. If `bahia-jicv` needs to decide ML product ingress policy, coordinate with sibling Bead `bahia-jxm3` rather than changing policy in this PSTF cleanup.

## Confidence Assessment

High for the fake/no-network D4 harness path. Not established for production readiness.

## Recommendation

Keep D4 scoped to fake/no-network acceptance. Do not treat it as RKNN recipe coverage, production Hugging Face/vLLM/GPU validation, live relay validation, or signer-first ML ingress policy resolution.
