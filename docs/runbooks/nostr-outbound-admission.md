# Nostr outbound admission

Every EVENT a Bahia process publishes is admitted by one process-wide
controller (`internal/nostrout`) before any relay I/O. There is no unlimited
mode: a nil controller rejects, an unreadable kill-switch file rejects, and a
relay rate-limit response opens a circuit breaker shared by every gateway.

## Gateways

Only these code paths may put an EVENT frame on the wire, and each one asks
the controller immediately before every frame:

| Gateway | Traffic |
|---|---|
| `RelayPool.PublishWithResults` | projections, control-plane results, DNS, outbox redelivery, migrations, release and telemetry adapters |
| SoulFactory relay bus (`sendAdmitted`) | SoulFactory publications, Concord invites, rekeys, compaction, snapshots |
| Signet `callManagement` | Signet management gift wraps |
| `nostrout.Bunker` | every NIP-46 signer request (Signet client, agent bunkers, SoulFactory enrollment verifiers) |

Standalone Bahia-derived agents (for example `bahia-dns-agent`) use the same
gateways and get the same bounded process default.

CI enforces this with a type-aware guard
(`internal/nostrout/gateway_guard_test.go`): any use of a `Publish*` function or
method from `fiatjaf.com/nostr` or `cascadia-go` — call, method value, or method
expression, under any import alias — and any NIP-46 client symbol must be an
exact approved gateway. Stale allowlist entries fail, and production code may
not construct an isolated controller. Do not widen the allowlist; route new
traffic through a gateway.

## Budgets

Logical budgets count events, not relay fan-out. Defaults per process:

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

## Duplicate suppression

The controller keeps a bounded receipt cache (4096 entries, 10 minutes) keyed
by event ID *and* relay. A destination that already accepted an exact signed
event is answered locally with a `duplicate:` result and no frame; a failed or
newly added destination remains eligible. Concurrent attempts to send the same
event to the same relay are refused with `already in flight`.

## Circuit breaker

Any `rate-limited:` response opens a process-wide breaker with exponential
backoff (2s doubling to 60s, plus up to 20% jitter). While open, ordinary
publications fail with `circuit breaker open` before relay I/O. A late success
from a publication admitted before a newer rate limit cannot close that newer
circuit.

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
  and must be re-run; nothing is rolled back.

A caller whose context deadline is shorter than the paced duration (for example
a short request timeout) will see a partial, resumable rotation. Give rotation
commands a context that covers the paced duration.

## Durable outbox retry lifetime

Events the server persists before publishing are retried from the outbox for at
most one hour from durable enqueue (`received_at`, not a possibly backdated
`created_at`). After that they move to the terminal `expired` publish state
and are never retried. A redelivery batch stops at the first admission
refusal instead of recording one identical failure per pending row. Terminal
ContextVM results keep their single 60-second deadline across the first
attempt and every retry, and stop immediately when the kill switch is active.

Each redelivery sweep also coalesces replaceable (kinds 0, 3, 10000–19999)
and addressable (30000–39999) events: a pending revision moves to terminal
`superseded` when **any recorded revision** of its NIP-01 coordinate wins,
including a published or inbound revision. The coordinate is `(kind, pubkey)`
plus the first `d` tag for addressable kinds (missing and empty are equal).
The winner has the newer `created_at`, or the lower event ID on a tie. An
ordered coordinate index lets each pending row fetch one winner without
scanning unrelated event history; sweep cost scales with pending depth and
indexed lookups. Regular and ephemeral kinds are never coalesced. The sweep
holds the publish lock, so it never changes an event that is mid-send.

Migration `000071_nostr_publish_expired` widens the publish-state constraint;
`000072_nostr_publish_superseded` adds `superseded`. Migration
`000073_nostr_coordinate_winner` adds the first-`d` coordinate function and
ordered index across all recorded states, including existing rows. Building
that index blocks writes during the migration; plan its rollout while Bahia
is stopped and verify build time on a production-sized copy. Rolling back
`000073` drops only the index and function; rolling back `000072` retires
superseded rows to `not_applicable`, never to `pending`. Older binaries ignore
expired rows, whose down migration also retires them to `not_applicable`.

## Emergency kill switch

Set `BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE` to a root-controlled local path.
The file is checked on every admission, so no process restart is required.
Content `1`, `true`, `stop`, `stopped`, `disable`, or `disabled` rejects every
new publication — including NIP-46 signer requests — before relay I/O. Missing
files and any other content permit the configured bounded traffic. An
unreadable configured file fails closed.

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
in-flight/capacity/queue rejections, wire attempts and rejections,
operation state, active publications, and relay
rate-limit responses. It never reports event bodies, tags, keys, or relay
credentials.

## NIP-46 signer requests

Bahia uses its own NIP-46 client (`nostrout.Bunker`), wire-compatible with
`fiatjaf.com/nostr/nip46`, because the upstream client publishes kind 24133
requests internally, discards relay OK/rate-limit results, and occasionally
issues unsolicited `switch_relays` requests. Each request is admitted in the
signer lane with a bounded wait (at most 30 seconds, 32 concurrent waiters)
because its caller is already blocked on the reply; each relay frame is
admitted after the relay connection is established; and every relay outcome
is observed, so a signer relay's rate limit opens the shared breaker for all
gateways. Relays come only from the bunker URI.
