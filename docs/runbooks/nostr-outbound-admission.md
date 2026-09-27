# Nostr outbound admission

Bahia's `RelayPool` rejects outbound events before relay I/O when its bounded
per-purpose budget is exhausted or a relay rate-limit response has opened the
process-wide circuit breaker. Server pools share one controller. Standalone
Bahia-derived agents get a bounded controller by default.

Default logical-event budgets are partitioned so state-repair traffic cannot
consume capacity reserved for operator results and tombstones:

- priority (`kind 5` and encrypted `kind 1059`): 10/minute, burst 4;
- replaceable/addressable state (`kind >= 10000`): 10/minute, burst 3;
- general events: 10/minute, burst 3.

Fan-out to multiple relays consumes one logical-event token. A relay
rate-limit rejection opens a global exponential circuit breaker. Successfully
accepted signed event IDs are suppressed for ten minutes, with a bounded 4096
ID cache.

## Emergency kill switch

Set `BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE` to a root-controlled local path.
The file is checked on every publication, so no process restart is required.
Content `1`, `true`, `stop`, `stopped`, `disable`, or `disabled` rejects every
new publication before relay I/O. Missing files and any other content permit
the configured bounded traffic. An unreadable configured file fails closed.

Example:

```sh
install -m 0600 /dev/null /run/bahia/nostr-publish.stop
printf 'stop\n' > /run/bahia/nostr-publish.stop
# Resume only after diagnosing the sender and relay feedback.
printf 'resume\n' > /run/bahia/nostr-publish.stop
```

Do not use the kill switch as ordinary flow control. Readiness/metrics must
surface sustained rejection before production rollout.

## Known migration boundary

CI freezes the remaining direct publishers in Signet and SoulFactory. Those
paths must be migrated behind the same admission interface before Bahia can be
restarted in production; adding another bypass fails the static guard.
