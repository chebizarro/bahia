# Verification Report: bahia-yu6g

## Observed behavior

- `internal/config/config.go` defaulted `Assistant.Agentic.Enabled` to `false`, so the legacy plan/approve planner was the default when the assistant was enabled.
- `validateAssistant` already inherited `agentic.model` and `agentic.api_key` from legacy `llm_*` fields when empty, but an omitted agentic block left `agentic.base_url` at the default OpenAI URL instead of inheriting a configured legacy `llm_base_url`.
- Validation reported only `assistant.agentic.model` when agentic was enabled without an effective model.

## Intended behavior

- The assistant defaults to the multi-step agentic loop with `assistant.permissions.mode=audited` unchanged.
- Deployments that only configure `assistant.llm_model`, `assistant.llm_base_url`, and `assistant.llm_api_key` keep working: the effective agentic model, base URL, and API key are populated before validation and before `app.go` builds the agentic model client.
- Setting `assistant.agentic.enabled: false` remains the explicit legacy plan/approve planner escape hatch.
- Validation tells operators to configure either `assistant.agentic.model` or `assistant.llm_model` when no effective agentic model exists.

## Verification evidence

- `go test ./internal/config` passed; it covers default-on agentic config, legacy `llm_*` inheritance into effective agentic provider fields, explicit legacy planner opt-out, and the clear effective-model validation error.
- `go build ./...` passed; the config changes compile through application wiring, including the agentic construction path that consumes `cfg.Assistant.Agentic`.
- `go test ./internal/service -run TestAssistantOrchestrator` passed; existing orchestrator coverage confirms the agentic loop and legacy planner fallback paths remain intact.
- `go vet ./internal/config` passed for the touched config package.

## Known boundaries

- This feature only changes configuration defaults, normalization, validation, and getting-started documentation. It does not alter Nostr event kinds, ContextVM semantics, relay subscription behavior, the legacy planner implementation, the `bahia-m6qs` fallback, or the `bahia-mxyg` `llm_streaming` toggle.
