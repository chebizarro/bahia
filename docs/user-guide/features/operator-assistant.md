# Operator Assistant

The floating **Assistant** is an in-product agent that proposes operations, asks for approval, and runs them through the same MCP tools and signed intents an operator would use. It needs `assistant.enabled: true` and a model endpoint.

## Workflows

- **Iterative** — the assistant converses, proposes one tool call at a time, and asks for approval when policy requires it.
- **Batch** — the assistant proposes a complete plan you can edit and reorder before approving it as a whole.

A prompt may request `workflow: batch` or `workflow: iterative`; otherwise the session keeps its workflow, and a new session uses `assistant.default_workflow`. Both workflows run through one executor that writes an encrypted checkpoint before every side effect and resumes an approved run after a daemon restart.

## Approvals and permissions

`assistant.permissions.mode` sets the posture:

| Mode | Behaviour |
|------|-----------|
| `audited` (default) | reads and low/medium-risk scoped mutations run autonomously with audit events; high-risk and destructive actions ask |
| `review` | reads run; every mutation asks |
| `readonly` | mutations are denied |
| `emergency` | all tool execution is denied |

An approval is scoped to one call with exact arguments, never to a tool name. Editing a batch plan creates a new revision and hash; an approval for an older revision cannot execute. Per-tool rules (`assistant.permissions`) and external MCP servers (`assistant.mcp.external_servers`, each with its own permission rules) extend the registry described in [MCP Tools](../mcp-tools.md).

## Documentation context

On a route with a documentation topic the composer shows a dismissible reference such as `docs:features-services`; it is sent as `selected_refs`, and the daemon resolves `docs:<topic>` and `bahia://docs/<topic>` from the user-guide catalog into the prompt context. An unknown topic is reported as unresolved, not treated as a failure.

## Stopping and reconciling

**Stop** prevents new work; operations already submitted may still finish and nothing is rolled back. When a run ends with an uncertain submitted effect, the **Uncertain downstream work** panel asks for the downstream request event id (**Submit evidence**) or an explicit **Abandon with attestation** with a reason. Missing relay events or an interrupted request are never treated as proof that an action failed; durable truth is the canonical state and the assistant's `30315` status events (`#domain=assistant`, with a `phase` such as `tool_call_requested`, `approval_required`, or `loop_completed`).

## Configuration

```yaml
assistant:
  enabled: true
  default_workflow: iterative        # batch | iterative
  llm_base_url: "https://api.openai.com"
  llm_model: "<batch-proposer-model>" # required for the batch workflow
  llm_api_key: "<key>"
  agentic:
    model: "<iterative-model>"       # falls back to llm_model
    tool_mode: native                # prompted for models without native tool calls
    max_iterations: 20
  permissions:
    mode: audited
```

Without `llm_model` the batch workflow is unavailable: a turn that resolves to batch is refused with `workflow_unavailable`, while approving, rejecting, stopping, and reconciling existing batch drafts still work. The assistant signs with the daemon's `nostr.private_key`; transcripts (`30316`) and execution checkpoints (`4903`) are encrypted.
