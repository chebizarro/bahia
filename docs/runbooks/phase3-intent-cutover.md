# Phase 3 R1 intent cutover

Operator mutations use client-signed kind `30900` intents. Check the daemon's
intent-domain registration and use a stable `intent_id` on retries; a relay
`OK` acknowledges publication only. Follow requester-scoped kind `30315`
status and the daemon-authored canonical state for the outcome. Do not retry a
mutation through ContextVM or REST if its status is pending.

The operator-facing ContextVM methods retained by R1 are
`assistant/prompt`, `assistant/approval`, `assistant/cancel`,
`assistant/reconcile`, `services/secrets-reveal`, and
`deployments/run-logs-get`. The daemon-to-DNS-agent RPC is an internal fallback
and is not an operator mutation route. The browser's assistant, secret-reveal,
and log-fetch e2e mocks must keep using their ContextVM responses.

All registered intent domains are enabled by default. Set
`nostr.intent_domains_disabled: [domain]` only to reject that domain's intents
temporarily; it does **not** re-enable a legacy handler. The deprecated
`nostr.intent_domains` allowlist is removed. After a cutover, confirm exactly
one canonical record per mutation, then verify warm-start and gift-wrap
ingress independently; neither should replay a second write.
