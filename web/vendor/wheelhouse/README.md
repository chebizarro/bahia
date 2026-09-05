# Wheelhouse

Wheelhouse is the standalone TypeScript/Svelte reference renderer for Cascadia Nostr
`dashboard-widget/v1` events. It validates untrusted kind `30318` events, normalizes them into
`WidgetModel`, and renders a small client-owned template catalog without accepting executable
chart specifications.

## Protocol profile

- Addressable kind `30318` with exactly one `d` tag:
  `<metric>:<host>:<service>:<window>`
- Envelope fields: `type`, `version`, `widget_kind`, `meta`, `scope`, `query`, exactly one of
  `data` or `data_ref`, and `presentation`
- `meta.title` and `meta.alt` are mandatory
- `scope.metric`, `scope.host`, and `scope.service` provide the first three slot components;
  `query.window` provides the fourth
- `query.from`, `query.to`, `query.step`, and `query.generated_at` are integer epoch seconds;
  `query.staleness_ttl` is a required nonnegative lifetime in seconds
- `renderer` and `spec` are forbidden
- Content is capped at 32 KiB; inline series points/table rows are capped at 2,000
- Unknown versions and widget kinds produce safe metadata fallback cards
- External data is represented by a placeholder in the MVP. The exported
  `VerifiedSidecarFetcher` boundary is reserved for a fetcher that verifies `data_ref.sha256`
  before Wheelhouse re-validates the returned template data.

Protocol constants live only in `src/lib/constants.ts` so generated `cascadia-ts` bindings can
replace them later.

## Templates

| Template | Widget kind | Rendering |
| --- | --- | --- |
| `ops.timeseries.v1` | `timeseries` | ECharts time axis + threshold mark lines |
| `ops.gauge.v1` | `gauge` | ECharts gauge + severity color |
| `ops.stat.v1` | `stat` | ECharts graphic + severity color |
| `ops.event_table.v1` | `event_table` | Native accessible Svelte table |

Builders accept optional semantic theme tokens. The consuming client owns all theming.

## Install and use

```bash
npm install
npm run test
npm run check
npm run build
```

```svelte
<script lang="ts">
  import { WidgetRenderer, type NostrWidgetEvent } from 'wheelhouse';
  import 'wheelhouse/style.css';
  export let event: NostrWidgetEvent;
</script>

<WidgetRenderer {event} theme={{ warn: '#f5a623', critical: '#e5484d' }} />
```

The lower-level validator and store are also exported:

```ts
import { createWidgetStore, validateWidgetEvent } from 'wheelhouse';

const store = createWidgetStore({ allowedPubkeys: ['<64-char hex pubkey>'] });
const result = validateWidgetEvent(event);
store.ingest(event);
```

An empty publisher allowlist denies every event.

## Development harness

```bash
cp .env.example .env
# Set VITE_ALLOWED_PUBKEYS for live rendering.
npm run dev
```

The harness starts in offline demo mode with fixtures for all four templates and a `data_ref`
placeholder. Live mode connects **only** to:

- `wss://relay.sharegap.net`
- `wss://nos.lol`

It subscribes to kind `30318`, verifies Nostr event signatures, applies the pubkey allowlist,
deduplicates event ids, and retains the latest event for each `(pubkey, d)` slot.
