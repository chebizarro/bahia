# Verification Report — bahia-6hic.10

Date: 2026-07-03

## Scope verified

Item 10 server-side extensibility surface for the agentic assistant loop:

- **Subagents** (`internal/service/assistant_subagents.go`): markdown+frontmatter (`name`/`description`/`model`/`tools`) loader; internal `bahia_assistant_delegate_subagent` tool runs a bounded, synchronous child loop restricted to an allowed-tool subset intersected with the global permission engine, returns the child result as a sync observation, and gates acceptance with `SubagentStop` hooks. Child loops run through a session-effect-free runtime clone so they never publish phantom session/status events or corrupt parent `agent_loop` metadata.
- **Skills** (`internal/service/assistant_skills.go`): `SKILL.md` loader with progressive disclosure — only names/descriptions are injected at turn start; the read-only `bahia_assistant_skill_load` tool returns bodies and confines supporting-file reads to each skill root (traversal/absolute paths rejected).
- **Commands** (`internal/service/assistant_commands.go`): `description`/`allowed-tools`/`model`/`argument-hint` markdown templates; a leading slash-command is expanded (with `$ARGUMENTS`/`$N` substitution) into the initial user message before `UserPromptSubmit` hooks, and command `allowed-tools` scope the exposed schema list.
- **Hooks** (`internal/service/assistant_hooks.go`): JSON loader + runner for `UserPromptSubmit`/`SessionStart`/`SessionEnd`/`PreToolUse`/`PostToolUse`/`Stop`/`SubagentStop`, with `prompt` and `mcp-tool` handler types only (shell rejected at load time). The loop enforces hard-deny → hooks → re-evaluate on hook-modified input → execute; hooks fold to the most-restrictive decision and can never upgrade a base deny to an allow.
- **Config/wiring**: `internal/config/config.go` adds `subagents`/`skills`/`commands`/`hooks` path blocks with parent-traversal containment and required-when-enabled validation; `internal/app/app.go` loads the libraries behind `assistant.agentic.enabled` and fails closed on malformed definitions. The internal delegate/skill_load tools use a distinct service-owned path; `internal/mcp/registry.go` was not touched (item 11 owns that merge path).

## Commands

| Command | Result | Notes |
|---|---:|---|
| `go test ./internal/service/ -run 'ParseAssistantSubagent\|ParseAssistantSkill\|ParseAssistantCommand\|ParseAssistantHookDocument'` | PASS | 9 tests. Valid + invalid frontmatter/JSON for all four loaders (missing fields, malformed yaml/json, unsupported event, shell/empty handlers, empty body). |
| `go test ./internal/service/ -run 'AssistantAgentLoopDelegatesSubagentReturningSyncObservation\|AssistantAgentLoopSubagentToolRestrictionIntersection'` | PASS | 2 tests. Sync delegation observation + allowed-tool intersection enforcement. |
| `go test ./internal/service/ -run 'AssistantAgentLoopPreToolUseHook\|AssistantAgentLoopStopHookBlocksThenCompletes'` | PASS | 3 tests. PreToolUse deny/ask gating + Stop-block-then-complete. |
| `go test ./internal/service/ -run 'AssistantSkillCatalogProgressiveDisclosure\|AssistantResolveContainedPathRejectsTraversal\|AssistantAgentLoopSkillLoadReturnsBody'` | PASS | 3 tests. Progressive disclosure, path containment, skill_load body. |
| `go test ./internal/service/ -run 'AssistantCommandExpand\|AssistantAgentLoopExpandsCommandIntoPrompt'` | PASS | 3 tests. Slash-command expansion + turn-start injection. |
| `go test ./internal/config/ -run AssistantExtension` | PASS | 3 tests. Path containment rejection, required-when-enabled, normalization. |
| `go build ./...` | PASS | Full module builds. |
| `go vet ./internal/service/ ./internal/config/` | PASS | Clean. |
| `go test ./internal/service/ -run Assistant` | PASS | 65 tests. Full agentic assistant regression (loaders, loop, runtime, permissions, recovery). |

## Acceptance mapping

| Acceptance | Evidence |
|---|---|
| AC1 loaders parse + reject invalid | `TestParseAssistantSubagent{Valid,Invalid}Frontmatter`, `TestParseAssistantSkill{Valid,Invalid}`, `TestParseAssistantCommand{WithFrontmatter,NoFrontmatter,Invalid}`, `TestParseAssistantHookDocument{Valid,RejectsUnsupported}` |
| AC2 subagent sync observation | `TestAssistantAgentLoopDelegatesSubagentReturningSyncObservation` |
| AC3 tool-restriction intersection | `TestAssistantAgentLoopSubagentToolRestrictionIntersection` |
| AC4 PreToolUse deny/ask | `TestAssistantAgentLoopPreToolUseHookDeniesTool`, `TestAssistantAgentLoopPreToolUseHookAsksForApproval` |
| AC5 Stop block | `TestAssistantAgentLoopStopHookBlocksThenCompletes` |
| AC6 skills progressive disclosure + confinement | `TestAssistantSkillCatalogProgressiveDisclosure`, `TestAssistantResolveContainedPathRejectsTraversal`, `TestAssistantAgentLoopSkillLoadReturnsBody` |
| AC7 command expansion | `TestAssistantCommandExpand{,Arguments}`, `TestAssistantAgentLoopExpandsCommandIntoPrompt` |
| AC8 config containment + build | `TestAssistantExtensionPaths{RejectTraversal,RequiredWhenEnabled,NormalizeAndAllowClean}`, `go build ./...` |

## Notes

- The broader `go test ./internal/service` suite has pre-existing SecurityScanner fixture failures unrelated to this work (tracked as `bahia-x3km`); scoped `-run Assistant` is green.
- Production prompt/mcp-tool hook evaluators are intentionally not injected yet (item 12). Until then an unevaluatable hook is skipped, which is safe: hooks are additive-and-restrictive only and can never upgrade a deny to an allow.
- Subagent child loops are synchronous by construction: async or approval-requiring tool calls inside a subagent are denied, so delegation always returns a single synchronous observation to the parent.
