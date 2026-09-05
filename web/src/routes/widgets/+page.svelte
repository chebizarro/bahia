<script>
  import { onMount } from 'svelte';
  import { WidgetRenderer } from 'wheelhouse';
  import 'wheelhouse/style.css';
  import {
    OPS_WIDGET_ALLOWED_PUBKEYS,
    OPS_WIDGET_RELAYS,
    createOpsWidgetWall
  } from '$lib/widgets/ops-widget-wall.js';

  let events = $state([]);
  let caughtUpRelays = $state([]);
  let failedRelays = $state([]);
  let lastError = $state('');
  let resubscribeAttempts = $state(0);

  const allowlistConfigured = OPS_WIDGET_ALLOWED_PUBKEYS.length > 0;
  let connectionLabel = $derived(`${caughtUpRelays.length}/${OPS_WIDGET_RELAYS.length} relays caught up`);

  onMount(() => {
    const wall = createOpsWidgetWall();
    const unsubscribeStore = wall.store.subscribe((snapshot) => {
      events = [...snapshot];
    });
    wall.start({
      onEose: (relay) => {
        caughtUpRelays = Array.from(new Set([...caughtUpRelays, relay]));
        failedRelays = failedRelays.filter((item) => item !== relay);
      },
      onClosed: (reason, relay) => {
        failedRelays = Array.from(new Set([...failedRelays, relay]));
        caughtUpRelays = caughtUpRelays.filter((item) => item !== relay);
        lastError = reason || `Subscription closed by ${relay}`;
      },
      onHealth: (health) => {
        resubscribeAttempts = health.resubscribeAttempts || 0;
      }
    });

    return () => {
      unsubscribeStore();
      wall.destroy();
    };
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
      {#if resubscribeAttempts > 0}<span>{resubscribeAttempts} reconnect attempt{resubscribeAttempts === 1 ? '' : 's'}</span>{/if}
    </div>
  </header>

  <section class="relay-panel" aria-label="Fleet widget relays">
    {#each OPS_WIDGET_RELAYS as relay}
      <span class:caught-up={caughtUpRelays.includes(relay)} class:failed={failedRelays.includes(relay)}>
        {relay}
      </span>
    {/each}
  </section>

  {#if !allowlistConfigured}
    <section class="notice warning" role="status">
      <strong>Publisher allowlist required</strong>
      <p>
        Set <code>PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS</code> to a comma-separated list of trusted
        64-character hexadecimal pubkeys. The wall rejects every publisher while the list is empty.
      </p>
    </section>
  {/if}

  {#if lastError}
    <section class="notice error" role="status">
      <strong>Relay subscription degraded</strong>
      <p>{lastError}</p>
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
          ? caughtUpRelays.length > 0
            ? 'The fleet relays have no current kind-30318 events from allowed publishers.'
            : 'Connecting to the fleet relays…'
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

  .relay-panel .caught-up {
    border-color: color-mix(in srgb, var(--success, #22c55e) 55%, var(--border-color));
    color: var(--success, #22c55e);
  }

  .relay-panel .failed {
    border-color: color-mix(in srgb, var(--danger, #ef4444) 55%, var(--border-color));
    color: var(--danger, #ef4444);
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
