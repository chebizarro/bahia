<script>
  import Table from '$lib/components/Table.svelte';
  import { events, controlplaneConnection } from '$lib/stores';

  const PAGE_SIZE = 50;
  let currentPage = $state(1);

  let totalPages = $derived(Math.max(1, Math.ceil(events.length / PAGE_SIZE)));
  let pagedEvents = $derived(events.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE));

  // Reset to page 1 when events list grows
  $effect(() => {
    void events.length;
    if (currentPage > totalPages) currentPage = 1;
  });

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

  <div class="table-wrap">
    <Table columns={columns} data={pagedEvents} />
  </div>

  {#if totalPages > 1}
    <div class="pagination">
      <button
        class="page-btn"
        disabled={currentPage === 1}
        onclick={() => currentPage--}
        aria-label="Previous page"
      >‹ Prev</button>
      <span class="page-info">Page {currentPage} of {totalPages}  ·  {events.length} events</span>
      <button
        class="page-btn"
        disabled={currentPage === totalPages}
        onclick={() => currentPage++}
        aria-label="Next page"
      >Next ›</button>
    </div>
  {:else}
    <p class="event-count">{events.length} event{events.length === 1 ? '' : 's'}</p>
  {/if}
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    min-height: 0;
  }

  .header {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }

  h1 { margin: 0; }

  .status { color: var(--success); font-size: 0.875rem; }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-bottom: 1rem; }
  .error { color: var(--error); font-size: 0.875rem; margin-bottom: 1rem; }

  .table-wrap {
    width: 100%;
    overflow-x: auto;
    flex: 1 1 auto;
  }

  .pagination {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-top: 1rem;
    flex-wrap: wrap;
  }

  .page-btn {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 0.375rem 0.875rem;
    color: var(--text-primary);
    cursor: pointer;
    font-size: 0.875rem;
    transition: background 0.15s;
  }

  .page-btn:hover:not(:disabled) {
    background: var(--hover-bg);
  }

  .page-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .page-info {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .event-count {
    color: var(--text-muted);
    font-size: 0.75rem;
    margin-top: 0.5rem;
    text-align: right;
  }
</style>
