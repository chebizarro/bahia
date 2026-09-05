# Ops Widgets

The **Operations → Ops Widgets** view displays live dashboard snapshots published as Nostr kind `30318` events using the `dashboard-widget/v1` envelope.

## Trust and relay policy

Bahia subscribes only to the fleet relays exported by Wheelhouse:

- `wss://relay.sharegap.net`
- `wss://nos.lol`

Set `PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS` to a comma-separated list of trusted 64-character hexadecimal publisher pubkeys. The view fails closed and renders no events when the allowlist is empty. Invalid Nostr signatures are rejected by Bahia's relay client before events reach Wheelhouse.

## Display behavior

Wheelhouse provides the event store, envelope validation, template registry, ECharts option builders, and fallback cards. The view therefore:

- deduplicates events by event ID;
- retains the latest event for each `(publisher, d-tag)` widget slot;
- renders `ops.timeseries.v1`, `ops.gauge.v1`, `ops.stat.v1`, and `ops.event_table.v1`;
- shows a safe metadata fallback card for invalid or unsupported envelopes that pass publisher and address checks;
- keeps subscriptions open after EOSE and reconnects after relay closure or connection failure.

External `data_ref` payloads remain represented by Wheelhouse's verified-sidecar placeholder unless a hash-verifying fetcher is added to the library boundary.

## Local package dependency

Wheelhouse is not published yet. The web package consumes it from `file:../../wheelhouse`, so the sibling Wheelhouse checkout must exist and its library artifact must be built before installing or building Bahia web.
