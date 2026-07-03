# Verification Report — bahia-6hic.12

Date: 2026-07-03

## Scope verified

- Implemented `internal/adapters/llm/anthropic_agent_client.go` against Anthropic `/v1/messages`, including `anthropic-version`, `x-api-key`, provider-neutral system/message serialization, native `tool_use` response parsing, and canonical `tool_result` threading from `AssistantToolObservation`.
- Updated permission posture: `audited` is the default; `review` remains selectable; `readonly` and `emergency` are accepted fail-closed modes; arg risk upgrade covers production/prod/rollback/delete/remove/revoke.
- Implemented production hook evaluators in `internal/service/assistant_hooks.go`: prompt hooks call the configured `AgentModelClient` with hook prompt + event payload; mcp-tool hooks call only registered read-only synchronous MCP tools.
- Wired provider selection and hook evaluators at app startup while preserving item-11 external MCP configuration/wiring from commit `46c0ddc8`.
- Enhanced assistant UI rendering for tool calls, downstream async waits, subagent runs, tool observations, and phase timeline while preserving action approval. Parser/store fields now mirror run/turn/iteration/observation/subagent metadata for relay and local ContextVM result items.

## Commands

| Command | Result | Notes |
|---|---:|---|
| `go test ./internal/adapters/llm ./internal/service ./internal/config` | PARTIAL | `llm` and `config` pass; broad `internal/service` hits known `bahia-x3km` SecurityScanner failures. |
| `go test ./internal/adapters/llm ./internal/config` | PASS | Rerun after Oracle P0 Anthropic base-url default fix. |
| `go test ./internal/service -run 'TestAssistant(Permission\|Hook)'` | PASS | Focused assistant permission and hook regressions requested for final closeout. |
| `cd web && npm test -- --run tests/unit` | PASS | 73 files / 576 tests. |
| `go build ./...` | PASS | Verified after item-11 external MCP commit `46c0ddc8` was present. |

## Acceptance mapping

| Acceptance | Evidence |
|---|---|
| AC1 Anthropic native tool-use | `TestAnthropicAgentClientToolUseAndToolResultThreading`, malformed/context tests |
| AC2 Provider config | `TestAssistantAgenticDefaults`, `TestAssistantAgenticValidation/anthropic_agentic_provider_accepted` |
| AC3 Audited default + risk scoring | `TestAssistantPermissionEngineDefaultAuditedAllowsLowRiskMutation`, `TestAssistantPermissionEngineAuditedAsksWhenArgsUpgradeRisk` |
| AC4 readonly/emergency | `TestAssistantPermissionEngineReadonlyAndEmergencyDenyMutations`, config validation test |
| AC5 hook evaluators | `TestAssistantHookModelPromptEvaluatorCallsModelWithPayload`, `TestAssistantReadOnlyMCPHookCallerAllowsOnlyReadOnlySyncTools` |
| AC6 richer UX | assistant component tests for tool calls/waits/subagents/observations/action approval; full web unit suite |

## Notes

- Oracle review found and item 12 fixed an Anthropic base-url default issue: provider `anthropic` now defaults away from the OpenAI URL even when `Defaults()` pre-populated it.
- Oracle review found and item 12 fixed local ContextVM result metadata parity for the assistant store.
- `assistant.agentic.enabled` remains false by default.
- No fake or stubbed behavior remains in the touched item-12 adapter, permission engine, hook evaluator, or UI paths.
