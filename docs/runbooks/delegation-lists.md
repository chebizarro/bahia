# Signed delegation lists

Agent claims of a human requester are refused until Bahia has caught up its
NIP-51 delegation-list REQ. Check `/ready` for `delegation_lists=pass` before
using a delegated ML rollback or service-signed backup command. Direct
operator-signed requests continue to use `nostr.authorized_pubkeys`.

## Grant

1. Identify the exact agent signing pubkey. For service-signed backup commands,
   this is `nostr.public_key`; for an ML agent, use its authorized signer
   pubkey. The daemon subscribes to coordinates for the service key and the
   configured control-plane author keys.
2. As a pubkey listed in `nostr.authorized_pubkeys`, publish a signed NIP-51
   kind-`30000` event to the configured control-plane relays:

   ```json
   {"kind":30000,"tags":[["d","agent:<agent-pubkey>:delegation"],["p","<human-pubkey>"]]}
   ```

   Sign the full NIP-01 event, use an empty string for `content`, and verify
   every relay's `OK`. Include every human this list author grants in the
   replacement event; omitted `p` tags are revoked for that author.
3. Wait for `delegation_lists=pass`, then submit the agent-signed command. A
   relay `OK` on the list alone is not the daemon's EOSE catch-up proof.

## Revoke or rotate

Publish a newer event with the same author, kind and `d`, omitting the
revoked human. A list with no `p` tags revokes all of that author's grants.
If multiple trusted operators published grants for the same agent, remove
the human from every author's list. Use a strictly later `created_at` second:
when timestamps tie, NIP-01 selects the lower event ID, not publication
order. Confirm relay `OK` and the daemon's caught-up health before relying
on revocation.

## Deployment migration

Publish complete lists for every existing delegated workflow before using it
with a daemon that enforces signed grants. Static `nostr.authorized_pubkeys`
continues to identify trusted list authors and direct request signers but
never substitutes for a missing `p` grant. A missing, malformed, stale, or
uncaught-up list denies the claim rather than preserving the old static grant.
