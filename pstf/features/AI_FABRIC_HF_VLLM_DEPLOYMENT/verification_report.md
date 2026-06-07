# Verification Report — AI_FABRIC_HF_VLLM_DEPLOYMENT

## Summary

D4 verification exists for the Hugging Face → artifact provenance → GPU/vLLM placement/deploy → Nostr read-model observation → UI smoke path, but the evidence is **non-production harness evidence**. It uses local fakes/mocks only and does not prove production readiness for real Hugging Face access, vLLM/GPU runtime behavior, OCI/Blossom artifact storage, OpenAI-compatible gateway routing, or live relay behavior.

`bahia-jicv` added an opt-in production integration gate, `test/integration/ml_hf_vllm_production_verification_test.go::TestAIHFVLLMProductionIntegrations`, that refuses to manufacture evidence. In this workspace, production verification is **blocked**, not verified, because the required external endpoints/credentials/hardware/relay access are absent and the existing integration package has unrelated compile blockers.

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

### Production verification gate

- `test/integration/ml_hf_vllm_production_verification_test.go::TestAIHFVLLMProductionIntegrations`
  - guarded by `BAHIA_HF_VLLM_PROD_VERIFY=1`;
  - skips by default with the exact prerequisite list;
  - fails closed when enabled but required production inputs are missing;
  - verifies only operator-supplied real endpoints: Hugging Face artifact, OCI mirror/provenance, and Blossom mirror response bodies are streamed and locally hashed with SHA-256 before production PASS; digest response headers are supplemental consistency evidence only, ETag is not treated as SHA-256 proof, and the gate also verifies vLLM `/v1/models` expected model id, gateway `/v1/models` expected model id, and live relay NIP-11/publish/subscribe behavior;
  - relay verification signs a narrow NIP-78 verification event with an expiration tag, checks per-relay OK/duplicate acceptance, surfaces CLOSED/AUTH failures, subscribes to each accepted relay with scoped kind/author/#d/#t filters, treats EOSE as backfill completion, and validates NIP-01 ID/signature before trusting the returned event.

### Production evidence

No production PASS evidence is recorded. The gate currently records blocked prerequisites rather than a production readiness claim.

## Commands Run — 2026-06-07

- `bd prime`
- `bd show bahia-jicv`
- `bd update bahia-jicv --claim`
- `gofmt -w test/integration/ml_hf_vllm_production_verification_test.go`
- `go test -tags=integration ./test/integration/ml_hf_vllm_production_verification_test.go -run TestAIHFVLLMProductionIntegrations -count=1`
  - Result: PASS compile/skip path (`ok command-line-arguments 0.268s`). This proves the byte-hash gate compiles standalone and does not require fake credentials by default.
- `go test -tags=integration ./test/integration -run TestAIHFVLLMProductionIntegrations -count=1`
  - Result: BLOCKED before executing the new gate by pre-existing compile errors in `test/integration/e2e_ci_registry_test.go`: unused `fmt`, unused `net/http`, and unused `ciSystem`. Tracked by `bahia-iz8j`.
- `bd create ...` for `bahia-wiw9`
- `bd create ...` for `bahia-iz8j`
- `bd dep add bahia-jicv bahia-wiw9`
- `bd dep add bahia-jicv bahia-iz8j`

### Review follow-up — 2026-06-07

- Updated `test/integration/ml_hf_vllm_production_verification_test.go` so Hugging Face, OCI mirror/provenance, and Blossom mirror artifact checks use `GET`, stream the response body, compute local SHA-256, and compare that value to the operator-supplied expected digest before production PASS. `Digest`, `X-Checksum-Sha256`, and `X-Content-Sha256` response headers remain supplemental consistency evidence; `ETag` is not treated as SHA-256 proof.
- `python3 -m json.tool pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/feature_spec.json >/tmp/bahia-feature-spec.json.check && python3 -m json.tool pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/acceptance_criteria.json >/tmp/bahia-acceptance.json.check && python3 -m json.tool pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/test_matrix.json >/tmp/bahia-test-matrix.json.check && python3 -m json.tool pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/defects.json >/tmp/bahia-defects.json.check`
  - Result: PASS JSON validation for scoped PSTF JSON artifacts.
- `go test -tags=integration ./test/integration/ml_hf_vllm_production_verification_test.go -run TestAIHFVLLMProductionIntegrations -count=1`
  - Result: PASS compile/skip path (`ok command-line-arguments 0.268s`). No production PASS evidence is claimed because `BAHIA_HF_VLLM_PROD_VERIFY` was unset and real external prerequisites remain blocked.
- `bd update bahia-wiw9 --description ...`
  - Result: PASS; blocker now requires expected SHA-256 byte hashes for configured artifact/mirror response bodies rather than header-only digest evidence.

## Acceptance Criteria Status

`AC-HFV-001` through `AC-HFV-005` have executable D4 harness evidence mapped in `test_matrix.json`. That evidence verifies local fake/no-network behavior only.

Production acceptance remains blocked until:

1. `bahia-wiw9` supplies real production prerequisites:
   - `BAHIA_HF_ARTIFACT_URL`
   - `BAHIA_HF_ARTIFACT_SHA256`
   - `BAHIA_VLLM_BASE_URL`
   - `BAHIA_ML_EXPECTED_MODEL_ID`
   - `BAHIA_ML_GATEWAY_MODELS_URL`
   - `BAHIA_ML_OCI_MANIFEST_URL`
   - `BAHIA_ML_OCI_ARTIFACT_SHA256`
   - `BAHIA_ML_BLOSSOM_ARTIFACT_URL`
   - `BAHIA_ML_BLOSSOM_ARTIFACT_SHA256`
   - `BAHIA_ML_RELAY_URLS`
   - `BAHIA_ML_RELAY_PRIVATE_KEY`
   - optional auth tokens where the production services require them.

The artifact SHA-256 prerequisites are expected byte hashes for the downloaded response bodies. HEAD-only checks, ETag-only checks, or digest-header-only checks are not sufficient production PASS evidence for `T-HFV-PROD-001`.
2. `bahia-iz8j` restores normal integration package compilation.
3. `BAHIA_HF_VLLM_PROD_VERIFY=1 go test -tags=integration ./test/integration -run TestAIHFVLLMProductionIntegrations -count=1` passes against real configured services.

## Defects / Remaining Gaps

- `D-HFV-PROD-001` / `bahia-wiw9`: production Hugging Face, artifact provenance/storage, GPU/vLLM, gateway, live relay, and Nostr protocol verification prerequisites are absent.
- `D-HFV-PROD-002` / `bahia-iz8j`: unrelated integration package compile blockers prevent normal package-path execution.

## Ambiguities / Human Decisions Needed

- `HITL-HFV-001`: operator must provide production endpoints, credentials, expected byte SHA-256 values for the configured artifact/mirror response bodies, expected model id, vLLM/GPU runtime, and relay access before production verification can be claimed.
- `HITL-HFV-002`: unrelated integration stubs must be fixed or isolated before using the normal integration package command.

## Confidence Assessment

High for classification of existing D4 evidence as non-production and for the standalone compile/skip behavior of the new production gate. Production readiness remains unverified.

## Recommendation

Do not treat this PSTF slice as production verified. Use `bahia-wiw9` and `bahia-iz8j` to unblock a real production verification run, then record PASS evidence here.
