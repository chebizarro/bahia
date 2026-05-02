<script>
  import Table from '$lib/components/Table.svelte';
  import { events, controlplaneConnection } from '$lib/stores';

  let columns = $derived([
    { key: 'time', label: 'Time', render: (r) => r.time || '-' },
    { key: 'type', label: 'Event Type' },
    { key: 'entity_id', label: 'Entity ID', render: (r) => r.entity_id ? `<code>${r.entity_id}</code>` : '-' },
    { key: 'data', label: 'Data', render: (r) => r.data ? `<code>${JSON.stringify(r.data).slice(0, 80)}...</code>` : '-' }
  ]);

  let statusDisplay = $derived({
    idle: '⚪ Not connected',
    discovering: '🟡 Discovering relay',
    connecting: '🟡 Connecting to relay',
    bootstrapping: '🟡 Waiting for EOSE',
    live: '🟢 Connected via Nostr relay',
    disconnected: '⚪ Relay disconnected',
    rollback_sse: '🟠 Rollback: legacy SSE',
    error: '🔴 Relay error'
  }[controlplaneConnection.status] || '⚪ Unknown');

</script>

<div class="page">
  <div class="header">
    <h1>Live Events</h1>
    <span class="status">{statusDisplay}</span>
  </div>

  <p class="hint">
    Events are rendered from relay-backed Bahia read-model/status/audit subscriptions.
    {#if controlplaneConnection.relays.length > 0}
      Relays: {controlplaneConnection.relays.join(', ')}
    {/if}
  </p>

  {#if controlplaneConnection.lastError}
    <p class="error">{controlplaneConnection.lastError}</p>
  {/if}

  <Table columns={columns} data={events} />
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1rem; }
  .status { color: var(--success); font-size: 0.875rem; }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-bottom: 1rem; }
  .error { color: var(--error); font-size: 0.875rem; margin-bottom: 1rem; }
</style>
