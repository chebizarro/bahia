# Operator Assistant

The operator assistant has two proposal workflows: **batch** presents an
editable, reorderable plan for human approval; **iterative** proposes tool
actions during a conversation and asks for approval when policy requires it.
Both workflows only propose; every action either one produces runs through the
same durable unified executor under the v2 contract, which checkpoints before
every side effect and resumes runs after a restart.

A new turn may request `workflow: batch` or `workflow: iterative`. Otherwise
the session keeps its persisted workflow; only a new session uses the configured
default. Review a batch plan's exact step order and arguments before approval.
An edit creates a new revision and hash; a stale approval cannot execute.
Action approval is scoped to one call and exact arguments, never just a tool
name. `AllowedTools: null` is unrestricted, whereas `[]` permits no tools.

The batch workflow needs `assistant.llm_model` (the batch proposer's model);
the iterative workflow needs `assistant.agentic.model` or `llm_model`. A
deployment whose default is iterative may omit `llm_model`; new batch
proposals are then unavailable. A new turn that resolves to batch (an explicit
`workflow: batch` or a session whose persisted workflow is batch) is refused
with `workflow_unavailable` rather than run as iterative. Work that needs no
proposer is unaffected: approving (including an edited revision) or rejecting
an existing batch draft, **Stop run**, reconciliation and finishing an approved
run. See
[Getting Started](../getting-started.md#assistant-workflow-configuration-migration).

Use **Stop run** to prevent new work. Submitted operations may still finish;
no rollback is attempted. A run with an uncertain submitted effect needs an
exact downstream request-event reference for reconciliation. Missing relay
events or an interrupted RPC are not proof of failure. Historical v1 sessions
remain viewable; active controls require eligible v2 state after migration.

The [protocol](../../operator-assistant-protocol.md) defines the signed
ContextVM methods and Nostr observables.
