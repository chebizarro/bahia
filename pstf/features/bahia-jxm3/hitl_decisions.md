# HITL Decisions — bahia-jxm3

Date: 2026-06-02

## Decision

Classify ML browser import/deploy as `rest_to_nostr_bridge` ingress.

## Rationale

The completed route matrix already classifies `/ml` import/deploy browser submissions as REST-to-Nostr bridge traffic. Repository evidence shows the browser calls REST client methods, the backend signs/publishes ML request events, and the HTTP response returns Nostr correlation metadata. Moving these forms to browser signer-first publishing would require a larger product and UX change outside this item.

## Product impact

The UI and docs now state that HTTP `202` is publish acceptance only. Operators follow the returned request/result/read-model metadata for terminal completion. Existing endpoint worker pin commands remain signer-first browser Nostr commands.

## Blockers

None recorded for this slice.
