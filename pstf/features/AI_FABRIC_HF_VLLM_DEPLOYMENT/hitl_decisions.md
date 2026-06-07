# HITL Decisions — AI_FABRIC_HF_VLLM_DEPLOYMENT

## Decision Log

- `HITL-HFV-001` (2026-06-07): Production readiness cannot be inferred from D4 fake/no-network evidence. A human/operator must provide real production verification prerequisites before this slice can be marked production verified: pinned Hugging Face artifact URL and sha256, vLLM backend URL, expected model id, ML gateway models URL, OCI/Blossom artifact mirror URLs and digests, production relay URLs, and an authorized Nostr private key for relay OK/EOSE/CLOSED/AUTH/signature/scoped-filter verification. Tracked by Bead `bahia-wiw9`.
- `HITL-HFV-002` (2026-06-07): Normal package-path execution of the new production gate is blocked by pre-existing compile errors in `test/integration/e2e_ci_registry_test.go`; fixing those unrelated integration stubs is tracked by Bead `bahia-iz8j`.
