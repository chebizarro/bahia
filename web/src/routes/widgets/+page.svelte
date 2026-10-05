<script>
  import { onMount } from 'svelte';
  import { WidgetRenderer } from 'wheelhouse';
  import 'wheelhouse/style.css';
  import { opsWidgetWall } from '$lib/widgets/ops-widget-wall.js';
  import { controlplaneConnection } from '$lib/stores/controlplane.svelte.js';
  import { syncStatus } from '$lib/stores/sync-status.svelte.js';

  let events = $state([]);
  const allowlistConfigured = opsWidgetWall.allowedPubkeys.length > 0;
  let connectionLabel = $derived(`${syncStatus.eoseCount}/${syncStatus.relayCount} deployment relays caught up`);

  onMount(() => {
    return opsWidgetWall.subscribe((snapshot) => {
      events = [...snapshot];
    });
  });
</script>

<svelte:head>
  <title>Ops Widgets · Bahia</title>
  <meta
    name="description"
    content="Trusted live Nostr operations widgets rendered with Wheelhouse"
  />
</svelte:head>

<div class="page">
  <header class="hero">
    <div>
      <p class="eyebrow">Nostr operations telemetry</p>
      <h1>Ops Widgets</h1>
      <p class="subtitle">
        Live kind-30318 snapshots rendered by Wheelhouse with publisher trust, addressable slot
        replacement, schema validation, and safe fallback cards.
      </p>
    </div>
    <div class="relay-status" aria-label="Widget relay status">
      <strong>{connectionLabel}</strong>
      <span>{events.length} current widget{events.length === 1 ? '' : 's'}</span>
      {#if controlplaneConnection.reconnects > 0}<span>{controlplaneConnection.reconnects} reconnect attempt{controlplaneConnection.reconnects === 1 ? '' : 's'}</span>{/if}
    </div>
  </header>

  <section class="relay-panel" aria-label="Deployment widget relays">
    {#each controlplaneConnection.relays as relay}
      <span>{relay}</span>
    {/each}
  </section>

  {#if !allowlistConfigured}
    <section class="notice warning" role="status">
      <strong>Publisher allowlist required</strong>
      <p>
        Configure <code>widget_pubkeys</code> in the deployment bootstrap seed or set
        <code>VITE_WHEELHOUSE_ALLOWED_PUBKEYS</code> at build time to trusted 64-character hexadecimal pubkeys.
        The wall rejects every publisher while the list is empty.
      </p>
    </section>
  {/if}

  {#if controlplaneConnection.lastError}
    <section class="notice error" role="status">
      <strong>Relay subscription degraded</strong>
      <p>{controlplaneConnection.lastError}</p>
    </section>
  {/if}

  {#if events.length > 0}
    <section class="widget-grid" aria-label="Current operations widgets">
      {#each events as event (event.id)}
        <WidgetRenderer {event} />
      {/each}
    </section>
  {:else}
    <section class="empty-state">
      <h2>No trusted widget snapshots</h2>
      <p>
        {allowlistConfigured
          ? syncStatus.eoseCount > 0
            ? 'The deployment relays have no current kind-30318 events from allowed publishers.'
            : 'Waiting for deployment relays; cached widgets appear without a connection.'
          : 'Publisher trust is fail-closed until an allowlist is configured.'}
      </p>
    </section>
  {/if}
</div>

<style>
  .page {
    display: grid;
    gap: 1.25rem;
    padding: 2rem;
  }

  .hero {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1.5rem;
  }

  .eyebrow {
    margin: 0 0 0.25rem;
    color: var(--primary, #818cf8);
    font-size: 0.78rem;
    font-weight: 900;
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }

  h1,
  h2,
  p {
    margin-top: 0;
  }

  h1 {
    margin-bottom: 0.35rem;
  }

  .subtitle,
  .empty-state p,
  .notice p {
    color: var(--text-muted);
  }

  .relay-status {
    display: grid;
    gap: 0.2rem;
    min-width: 12rem;
    padding: 0.85rem 1rem;
    border: 1px solid var(--border-color);
    border-radius: 14px;
    background: var(--card-bg, rgba(15, 23, 42, 0.7));
    text-align: right;
  }

  .relay-status span {
    color: var(--text-muted);
    font-size: 0.78rem;
  }

  .relay-panel {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }

  .relay-panel span {
    padding: 0.45rem 0.7rem;
    border: 1px solid var(--border-color);
    border-radius: 999px;
    color: var(--text-muted);
    font-size: 0.8rem;
  }

  .notice,
  .empty-state {
    padding: 1.1rem 1.25rem;
    border: 1px solid var(--border-color);
    border-radius: 16px;
    background: var(--card-bg, rgba(15, 23, 42, 0.7));
  }

  .notice p,
  .empty-state p {
    margin: 0.4rem 0 0;
  }

  .warning {
    border-color: color-mix(in srgb, #f59e0b 55%, var(--border-color));
  }

  .error {
    border-color: color-mix(in srgb, var(--danger, #ef4444) 55%, var(--border-color));
  }

  .widget-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
    gap: 1rem;
    align-items: stretch;
  }

  :global(.widget-grid .widget-card),
  :global(.widget-grid .fallback) {
    height: 100%;
    color: var(--text-primary);
    background: var(--card-bg, rgba(15, 23, 42, 0.7));
    border-color: var(--border-color);
  }

  code {
    overflow-wrap: anywhere;
  }

  @media (max-width: 720px) {
    .page {
      padding: 1.25rem;
    }

    .hero {
      display: grid;
    }

    .relay-status {
      width: 100%;
      text-align: left;
    }
  }
</style>
