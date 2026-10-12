# Assistant transcript keys

Assistant transcripts (`30316`) and execution checkpoints (`4903`) are
sealed with the `assistant-transcript` [system credential](../architecture/secret-store.md#system-credentials)
held in Signet. Design: [`assistant-execution.md`](../architecture/assistant-execution.md#transcript-and-checkpoint-keys).

## Provision

Import a 32-byte key labelled `bahia-system/assistant-transcript/<key-id>`
and list it first under `secret_store.system_keys.assistant_transcript`
([steps](signet-secret-store.md#system-credentials)). `assistant.enabled`
refuses to start without it.

## Rotate

1. Import a new key under a new key id, for example
   `bahia-system/assistant-transcript/2027-04`. Do not use
   `signetctl rotate-credential` on a transcript key: Signet delivers only a
   credential's active version, so events sealed under the old version could
   no longer be opened.
2. List the new key first and keep the old one after it, then restart:

   ```yaml
   secret_store:
     system_keys:
       assistant_transcript:
         - {key_id: "2027-04", credential_id: cred_<new>, version: 1}   # seals new events
         - {key_id: "2026-10", credential_id: cred_<old>, version: 1}   # opens older events
   ```

New events name `bahia-system/assistant-transcript/2027-04` in `key_ref`;
older ones keep naming the key that sealed them. Checkpoints are immutable,
so the old key stays configured as long as the history it sealed is needed.
Removing it makes that history unreadable: loads fail closed naming the
missing key.

If a transcript key leaks, rotate it as above and treat the history it
sealed as exposed.

## Re-establish history sealed under the retired service key

Before system credentials, the daemon derived the transcript key from
`nostr.private_key` (`key_ref` `assistant-transcript/service-nostr-key`,
`key_version` `v1`). The daemon no longer derives it, so that history is
unreadable until it is re-established. It is also signed by the retired
service identity, which the daemon no longer trusts after the service key
rotation. `bahia-assistant-reencrypt` re-establishes it under the current
identity and the system key; it is never run by the daemon.

What it does: reads the retired identity's checkpoints, transcripts and
assistant session projections from the relays (REQ through EOSE on every
relay; any incomplete read aborts), opens them with the key derived from the
retired private key, and re-seals:

- each checkpoint chain root to head, re-pointing every predecessor link at
  the re-sealed predecessor (event ids change; `created_at` is kept);
- each transcript on its `d` coordinate;
- each v2 session projection with `checkpoint_event_id` re-pointed; a v1
  projection only for a session without a v2 projection.

With `--execute` it signs them as the current service identity and publishes
them in that order to the relays you name, which must be in
`nostr.controlled_relays`. It refuses to run while `nostr.signer` is still
the retired identity.

Preconditions:

- the service key is rotated: `nostr.signer` is the new identity;
- the `assistant-transcript` system key is configured and Signet answers;
- the daemon is stopped (one writer per service identity; sessions are
  recovered at startup).

Steps:

1. Put the retired key in a file exactly as the daemon was configured with it
   (`nostr.private_key`, 64 hex; the derived key depends on the exact text),
   owner-only:

   ```bash
   install -m 0600 /dev/null /run/operator/retired-service.key
   $EDITOR /run/operator/retired-service.key
   ```

2. Dry run (signs and publishes nothing):

   ```bash
   bahia-assistant-reencrypt --config /etc/bahia/config.yaml \
     --old-key-file /run/operator/retired-service.key \
     --relay wss://relay-a.example --relay wss://relay-b.example
   ```

   It prints how many checkpoints, transcripts and session projections it
   will publish, how many are already re-established, and every item it
   cannot open (`cannot re-establish …`, event ids only, never content).

3. Run it with `--execute`. Events are paced by the outbound `bulk` lane (5
   per minute by default); for a large history raise
   `nostr.outbound.lanes.bulk` in the configuration the tool reads. Exit
   status 2 means something was incomplete (a relay did not answer, an event
   was refused, an item could not be opened); the run is idempotent, so run
   it again: it reuses what the current identity already holds and resumes
   a checkpoint chain where it stopped without forking it.
4. `shred -u /run/operator/retired-service.key`, start the daemon, and check
   that `system_keys` passes and a migrated session opens.

Re-encryption restores the daemon's access; it does not make the old
ciphertext confidential again. Anyone holding the retired key can still read
the retired identity's events wherever relays retain them.

To abandon that history instead, do nothing: the daemon never reads the
retired identity's events after the rotation, and nothing derives the
retired key.
