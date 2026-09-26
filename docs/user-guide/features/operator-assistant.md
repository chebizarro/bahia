# Operator Assistant

The operator assistant has two proposal workflows: **batch** presents an
editable, reorderable plan for human approval; **iterative** proposes tool
actions during a conversation and asks for approval when policy requires it.
The unified v2 contract makes both use one durable execution path. The contract
is frozen here, but production still runs the existing v1 paths until the
backend and browser activation items land together.

A new turn may request `workflow: batch` or `workflow: iterative`. Otherwise
the session keeps its persisted workflow; only a new session uses the configured
default. Review a batch plan's exact step order and arguments before approval.
An edit creates a new revision and hash; a stale approval cannot execute.
Action approval is scoped to one call and exact arguments, never just a tool
name. `AllowedTools: null` is unrestricted, whereas `[]` permits no tools.

Use **Stop run** to prevent new work. Submitted operations may still finish;
no rollback is attempted. A run with an uncertain submitted effect needs an
exact downstream request-event reference for reconciliation. Missing relay
events or an interrupted RPC are not proof of failure. Historical v1 sessions
remain viewable; active controls require eligible v2 state after migration.

The [protocol](../../operator-assistant-protocol.md) defines the signed
ContextVM methods and Nostr observables.
