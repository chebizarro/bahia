# Acceptance Criteria — LLM_ENABLED_UX_FOUNDATION

These are the 9 acceptance criteria from `docs/plans/llm-enabled-ux-foundation-2026-05-16.md`.

1. No downstream control-plane command exists before explicit operator approval.
2. Approved plans produce exactly one downstream Nostr command per step.
3. Duplicate approval does not republish downstream commands.
4. D1-disabled/excluded tools are rejected at planning time.
5. Sidebar reload reconstructs session entirely from Nostr events.
6. Relay CLOSED/auth interruption marks session `blocked`, not `failed`.
7. Final assistant result accurately reflects downstream terminal result.
8. All assistant actions visible as Nostr events — no hidden mutations.
9. Operator can cancel a stuck session via `38421 decision=cancel`.
