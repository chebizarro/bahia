# Verification Report: bahia-mxyg

## Observed behavior

- The legacy assistant orchestrator selected `PlanFromPromptStreaming` whenever the chat client implemented `AssistantStreamingChatClient`.
- The `bahia-m6qs` fallback in `internal/adapters/llm/chat_client.go` remained necessary for providers that return no streamed `delta.content` for `response_format (json_schema)` outputs, but the orchestrator still attempted streaming first on every turn.

## Intended behavior

- `assistant.llm_streaming` defaults to `false`, so the legacy planner uses non-streaming `PlanFromPrompt`.
- Operators can opt in per provider with YAML key `assistant.llm_streaming: true` or environment variable `BAHIA_ASSISTANT_LLM_STREAMING=true`.
- When the toggle is true and the chat client supports streaming, the orchestrator uses `PlanFromPromptStreaming`.
- The `bahia-m6qs` empty-stream fallback remains unchanged.

## Verification evidence

- `go test ./internal/service -run TestAssistantOrchestrator` passed.
- `go test ./internal/config` passed.
- `go build ./...` passed.
- `go vet ./internal/service ./internal/config` passed.

## Known boundaries

- This change only affects the legacy assistant planner path. The agentic loop and `openai_agent_client.go` are intentionally out of scope.
