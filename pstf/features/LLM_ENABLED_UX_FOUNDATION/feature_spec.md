# Feature Spec — LLM_ENABLED_UX_FOUNDATION

## Feature

Define the protocol contracts for Bahia's LLM-enabled operator assistant foundation.

## Scope

This Milestone 1 Foundation slice establishes shared contracts only:

- canonical operator assistant Nostr protocol reference
- assistant event kind constants
- Go domain types for sessions, plans, prompt requests, and async tool receipts
- deterministic assistant plan hashing contract
- frontend assistant kind constants and parse helpers
- PSTF acceptance criteria and verification scaffold

## Out of scope

- assistant orchestration logic
- LLM calls or provider adapters
- MCP dispatch implementation
- session recovery runner
- Svelte stores or UI components
- reactor registration for prompt/approval handling
- downstream command execution

## Intended behavior

The repository should contain stable protocol definitions that later backend and frontend milestones can implement against without redefining event kinds, tags, plan shape, approval semantics, or assistant-safe downstream command scope.

## Contract references

- `docs/plans/llm-enabled-ux-foundation-2026-05-16.md`
- `docs/operator-assistant-protocol.md`
- `internal/domain/assistant.go`
- `internal/adapters/nostr/publisher.go`
- `web/src/lib/nostr/client.js`
