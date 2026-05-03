# Payments and orgs encrypted Nostr request/result migration

Date: 2026-05-03
Issue: `bahia-xfke`

## Summary

`/payments` and `/orgs` no longer require REST compatibility auth. Both route families use encrypted signer-first Nostr request/result events (`kind:5980` request, `kind:7980` result) and keep payment history, organization membership, and invite data out of public sidecar projections.

## Operations

| Domain | Operation | Payload | Result |
| --- | --- | --- | --- |
| payments | `payments.history` | `{worker, limit}` | payment records for the worker |
| orgs | `orgs.list` | `{}` | organizations for the requester, with role |
| orgs | `orgs.my_invites` | `{}` | pending invites for the requester, enriched with org name |
| orgs | `orgs.detail` | `{id}` | `{org, members, invites, my_role}`; invites are included only for admin/owner requesters |
| orgs | `orgs.create` | `{name, display_name}` | created organization |
| orgs | `orgs.delete` | `{id}` | terminal status |
| orgs | `orgs.accept_invite` | `{invite_id}` | created membership |
| orgs | `orgs.create_invite` | `{org_id, pubkey, role, expires_in}` | created invite |
| orgs | `orgs.revoke_invite` | `{org_id, invite_id}` | terminal status |
| orgs | `orgs.update_member_role` | `{org_id, pubkey, role}` | terminal status |
| orgs | `orgs.remove_member` | `{org_id, pubkey}` | terminal status |

Filename note: this document filename retains `private-transport` for historical continuity; terminology in-body uses the canonical encrypted request/result names.

## Guardrails

- No org membership, invite, or payment data is published as public read-model sidecar events.
- Browser route code uses `web/src/lib/stores/payments.svelte.js` and `web/src/lib/stores/orgs.svelte.js`, which call encrypted request/result helpers.
- Backend operation handlers live in `internal/controlplane/private_domain_handlers.go` and reuse existing repository/RBAC/payment service logic.
- The `/payments` and `/orgs` compatibility gates were removed only after all route files under those prefixes stopped importing the REST API client.
- Canonical operator-facing names are `nostr.encrypted_request_relays`, `nostr.browser_encrypted_request_relays`, and `features.encrypted_nostr_requests`.
- Wire marker is `encrypted=bahia-encrypted-v1`.
