# HITL Decisions — bahia-jxm3

Date: 2026-06-02
Superseded: 2026-06-03 by `bahia-gkg7`

## Decision

This prior ML browser ingress classification is superseded. The current `/ml` web route publishes signed Nostr ContextVM commands directly and is classified in the route matrix as `ml-nostr-controlplane` / `nostr_native`.

## Current product impact

Operators should treat ML browser import/deploy submission as Nostr command publication. Terminal completion remains correlated result/read-model observation, not synchronous form completion.
