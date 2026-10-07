# Ops Widgets

**Operations → Ops Widgets** renders live dashboard snapshots that trusted services publish to the fleet relays as kind `30318` events in the `dashboard-widget/v1` envelope.

## Trust

The view subscribes to the Bahia control-plane relays for kind `30318` events whose author is in the widget allowlist. Set `PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS` on the web container (at runtime, not build time) to a comma-separated list of 64-character hex publisher pubkeys; the container entrypoint writes it into the deployment seed as `widget_pubkeys`. With an empty allowlist the page subscribes to nothing and shows **No trusted widget snapshots**. Signatures are verified before an event reaches the renderer.

## Rendering

The page keeps the latest event per `(publisher, d-tag)` slot, deduplicates by event id, keeps the subscription open after EOSE, and reconnects after a relay closes. Supported templates are `ops.timeseries.v1`, `ops.gauge.v1`, `ops.stat.v1`, and `ops.event_table.v1`; an envelope that passes publisher and address checks but is invalid or unsupported is shown as a metadata fallback card. External `data_ref` payloads are shown as a placeholder rather than fetched.

The relay panel lists the deployment relays and how many have caught up.
