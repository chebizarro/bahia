# HITL Decisions — SOULFACTORY_OPENCLAW_MVP

No signer/Signet construction ambiguity was found for Item 1. The existing `internal/adapters/signet.Client` implements the SoulFactory signer interface and fails closed unless its own explicit mock mode is enabled; app-level SoulFactory startup does not expose or enable that mock mode.

No REST provisioning or lifecycle route decision is needed because the plan explicitly keeps SoulFactory Nostr-first.
