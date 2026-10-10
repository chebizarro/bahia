# Nostr outbound admission

Every EVENT a Bahia process publishes is admitted by one process-wide
controller (`internal/nostrout`) before any relay I/O. There is no unlimited
mode: a nil controller rejects, an unreadable kill-switch file rejects, and a
relay rate-limit response opens a circuit breaker shared by every gateway.

## Gateways

Only these code paths put an EVENT frame on the wire, and each one asks the
controller immediately before every frame:

| Gateway | Traffic |
|---|---|
| `RelayPool.PublishWithResults` / `PublishToRelaysWithResults` | projections and canonical-first publishes, control-plane results and intent statuses, DNS requests and the standalone DNS agent health publisher, outbox delivery and redelivery, migrations and imports, HiveCI/Gitea journals, release and telemetry adapters, Loom, SoulFactory relay clients (publications, Concord invites, rekeys), and the Signet management plane |
| RelayPool NIP-42 AUTH (`authSignerFor`) | every AUTH frame from every connection: the pool's AuthHandler, publish-path AUTH retries, `AuthenticateRelay`/`AuthenticateRelays`, inbound-sync re-AUTH, NIP-77 session connections, and the Signet management pool |
| NIP-77 upload (`uploadNegentropyItems`) | local-only events a negentropy reconcile pushes to a relay, paced through bounded bulk operations on the session connection |
| `nostrout.Bunker` | every NIP-46 signer RPC (the Signet client and its per-agent bunkers, SoulFactory enrollment verifiers) |

A NIP-42 AUTH frame takes its own permit immediately before the library
writes it: one priority-lane token and one of the relay's reserved priority
wire tokens (`AdmitAuth`). AUTH belongs to the reserved priority share
because it unlocks delivery of exactly the traffic that share protects —
inbox relays serve gift-wrapped operator results only to their authenticated
recipient — and its bounded burst caps AUTH storms from relays that
re-challenge every connection. A refused permit fails the AUTH attempt
closed; the publish or subscription surfaces a retryable authentication
failure. A rate-limited AUTH answer feeds the shared breaker like any other
rate-limit feedback. The EVENT frame an AUTH retry resends is charged a
second per-relay wire token. REQ, CLOSE and COUNT frames are reads and are
not publications.

Standalone Bahia-derived agents (for example `bahia-dns-agent`) publish
through the same relay-pool gateway and get the same bounded process default.

CI enforces this with a zero-bypass, frame-level type-aware ratchet
(`internal/archtest/relay_publish_test.go`, baseline
`internal/archtest/testdata/relay_publish.baseline`): any production use of a
`Publish*` function or method from `fiatjaf.com/nostr` or `cascadia-go`
(including through the `nostr.Publisher` interfaces), `(*Relay).Auth`, the
raw frame writers `(*Relay).Write`/`WriteWithError`, raw websocket
dials/writes, NIP-46 client symbols, or NIP-77 session helpers must be an
exact approved declaration site, and every `(*Relay).Auth` call must
provably pass the admission-wrapped signer. The baseline is empty and must
stay empty; stale allowlist entries fail. Do not widen the allowlist; route
new traffic through the gateway. Reads (REQ/CLOSE/COUNT) are covered by the
`relay_subscribe` ratchet instead.

## Budgets

Logical budgets count events, not relay fan-out. Defaults per process
(`nostr.outbound.*` overrides any of them; zero keeps the default):

| Lane | Carries | Rate/min | Burst |
|---|---|---:|---:|
| priority | kind 5 tombstones, kind 1059 gift wraps, kind 25910 ContextVM | 10 | 4 |
| state | replaceable (10000–19999) and addressable (30000–39999) | 10 | 3 |
| general | everything else | 10 | 3 |
| bulk | declared multi-event operations only | 5 | 1 |
| signer | NIP-46 requests (kind 24133) | 10 | 4 |
| aggregate | all lanes together | 45 | 15 |

Each relay also has a wire budget, charged immediately before every EVENT
frame (after any reconnect), including NIP-42 AUTH retries: 15 frames/minute
(burst 5) reserved for priority frames and 40 frames/minute (burst 13) for
everything else. A server plus one standalone agent against the same relay
therefore stays below the shared relay's historic 120 events/minute bucket.
Lanes and wire shares are fixed partitions: bulk operations, state repair, and
AUTH churn can never consume priority capacity.

Ordinary publications fail fast when a budget is exhausted
(`nostr outbound publication budget exhausted`). Callers retain the event
(the durable outbox does this) rather than retrying immediately.

Configuration keys live under `nostr.outbound` (environment:
`BAHIA_NOSTR_OUTBOUND_*`, with `__` as the nested separator, for example
`BAHIA_NOSTR_OUTBOUND_LANES__PRIORITY__BURST`):

| Key | Default |
|---|---|
| `kill_switch_file` | empty (see below; also `BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE`) |
| `aggregate.rate_per_minute`, `aggregate.burst` | `45`, `15` (unset: sum of the lanes) |
| `lanes.priority`, `lanes.state`, `lanes.general`, `lanes.bulk`, `lanes.signer` | table above |
| `relay_wire.rate_per_minute`, `relay_wire.burst` | `40`, `13` |
| `relay_wire_priority.rate_per_minute`, `relay_wire_priority.burst` | `15`, `5` |
| `breaker_min`, `breaker_max` | `2s`, `1m` |
| `duplicate_ttl`, `duplicate_limit` | `10m`, `4096` |

Negative values are rejected by config validation. No value disables
admission.

`bahia-server` and `bahia-relay` each build their process controller from
these settings at startup (`config.NostrOutboundConfig.Admission`), so the
relay sidecar's NIP-46 signer requests honour the same lanes and kill switch
file as the daemon's. The settings are fixed for the life of the process: a
`SIGHUP` whose config changes anything under `nostr.outbound` is rejected and
logged (`nostr outbound admission settings are fixed at process start;
restart to apply ...`), and the process keeps running with its current
config. Restart the process to change budgets or the kill switch path. The
kill switch file's *content* is read on every admission and needs neither.

## Duplicate suppression

The controller keeps a bounded receipt cache (4096 entries, 10 minutes) keyed
by event ID *and* relay. A destination that already accepted an exact signed
event is answered locally with a `duplicate:` result and no frame; a failed or
newly added destination remains eligible. Concurrent attempts to send the same
event to the same relay are refused with `already in flight`.

## Circuit breaker

Any `rate-limited:` relay feedback opens a process-wide breaker with
exponential backoff (2s doubling to 60s, plus up to 20% jitter): a publish
OK=false reason, a subscription CLOSED classified by the pool's CLOSED
policy, or a NOTICE frame. While open, ordinary publications fail with
`circuit breaker open` before relay I/O. A late success from a publication
admitted before a newer rate limit cannot close that newer circuit.

## Multi-event operations

Concord rotations, Direct Invite batches, and NIP-77 reconcile uploads are
admitted as one bounded operation before their first irreversible step (the
custody write for a rotation; the first uploaded frame for a reconcile).
Once active, their events are paced through the bulk lane and wait for an
open circuit to close, instead of failing halfway because the operation is
larger than a burst. A negentropy upload larger than one operation's 2048
event ceiling continues in consecutive operations, and a session whose
timeout ends first simply resumes at the next session — NIP-77
reconciliation is convergent.

- One operation is active per process; up to 8 wait in FIFO order for at most
  30 seconds, then fail with `queue full` or `wait expired`.
- An operation declares at most 2048 publications and is bounded by
  `declared / bulk rate + 60s`. At defaults a 13-event rotation takes about
  2.5 minutes, and a 2048-event operation is bounded at roughly 6h51m.
- Exceeding the declared count, the kill switch, cancellation, or the deadline
  interrupts the operation. Nostr cannot publish several events atomically
  across relays: an interrupted rotation keeps CORD-06's resumable semantics
  and must be re-run; nothing is rolled back. Refounding is refused before
  planning (CORD-04 containment), so its compaction and snapshot paths never
  publish.

A caller whose context deadline is shorter than the paced duration (for
example a short request timeout) will see a partial, resumable rotation. Give
rotation commands a context that covers the paced duration.

## Interaction with the publish outbox

The local outbox (`docs/architecture/outbox-delivery.md`) owns delivery
durability; admission owns the wire. An admission refusal (budget, circuit,
kill switch, capacity, in-flight duplicate) **skips** the delivery round: it
never counts against the bounded attempt budget, never abandons the entry,
and never marks its coordinate undelivered — admission is back-pressure, not
abandonment. The entry stays pending and is retried about a second later
(jittered, so a restart's hydration cannot resynchronize into a burst). A
process-wide gate (kill switch, open circuit, exhausted controller capacity)
stops the whole redelivery pass at the first refusal; a lane budget refusal
leaves other lanes' entries in the pass. `PublishBeforeCommit` producers
(relay-first registries) fail closed instead: a refused round queues nothing
and the producer commits nothing.

## Emergency kill switch

Set `nostr.outbound.kill_switch_file` (or
`BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE`, which standalone agents read
directly) to a root-controlled local path. The file is checked on every
admission, so no process restart is required. Content `1`, `true`, `stop`,
`stopped`, `disable`, or `disabled` rejects every new publication — including
NIP-46 signer requests — before relay I/O. Missing files and any other content
permit the configured bounded traffic. An unreadable configured file fails
closed.

```sh
install -m 0600 /dev/null /run/bahia/nostr-publish.stop
printf 'stop\n' > /run/bahia/nostr-publish.stop
# Resume only after diagnosing the sender and relay feedback.
printf 'resume\n' > /run/bahia/nostr-publish.stop
```

Do not use the kill switch as ordinary flow control.

## Readiness and metrics

The `nostr_outbound_admission` health check fails while the kill switch is
active and warns while the breaker is open or after budget rejections. Its
content-free details include attempted, admitted, budget/circuit/kill-switch/
in-flight/capacity/queue rejections, wire attempts and rejections, AUTH
admissions, opaque (NIP-46) admissions, operation state, active
publications, and relay rate-limit responses. It never reports event bodies, tags, keys, or relay
credentials. The standalone DNS agent reports the same counters in its
`/healthz` payload under `outbound_admission`.

## Known limits

- The pinned NIP-46 library sends exactly one request frame per bunker relay
  per RPC, with no internal retries, and exposes no publish hook: requests are
  admitted (signer lane plus every bunker relay's wire share) before the
  library runs, but relay OK/rate-limit feedback on those frames is swallowed
  by the library and cannot open the breaker.
- Pending replaceable-state events are not coalesced by coordinate in the
  outbox; the receipt cache suppresses exact replays instead.
