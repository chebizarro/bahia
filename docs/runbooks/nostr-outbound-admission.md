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
| `nostrout.Bunker` | every NIP-46 signer RPC (the Signet client and its per-agent bunkers, SoulFactory enrollment verifiers) |

NIP-42 AUTH frames are not EVENT publications and are not budgeted, but the
EVENT frame an AUTH retry resends is charged a second per-relay wire token.

Standalone Bahia-derived agents (for example `bahia-dns-agent`) publish
through the same relay-pool gateway and get the same bounded process default.

CI enforces this with a zero-bypass type-aware ratchet
(`internal/archtest/relay_publish_test.go`, baseline
`internal/archtest/testdata/relay_publish.baseline`): any use of a `Publish*`
function or method from `fiatjaf.com/nostr` or `cascadia-go` — call, method
value, or method expression, under any import alias — and any NIP-46 client
symbol outside `internal/nostrout/bunker.go` fails the build. The baseline is
empty and must stay empty. Do not widen the owners; route new traffic through
the gateway.

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

Concord rotations and Direct Invite batches are admitted as one bounded
operation before their first irreversible step (the custody write). Once
active, their events are paced through the bulk lane and wait for an open
circuit to close, instead of failing halfway because the operation is larger
than a burst.

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
in-flight/capacity/queue rejections, wire attempts and rejections, opaque
(NIP-46) admissions, operation state, active publications, and relay
rate-limit responses. It never reports event bodies, tags, keys, or relay
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
