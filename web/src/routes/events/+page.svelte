<script>
  import Table from '$lib/components/Table.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import { events, controlplaneConnection } from '$lib/stores';
  import { KINDS } from '$lib/nostr/client.js';

  const PAGE_SIZE_OPTIONS = [25, 50, 100];
  let pageSize = $state(50);
  let currentPage = $state(1);
  let eventTypeFilter = $state('all');

  // Event type categories for filtering
  const eventCategories = [
    { value: 'all', label: 'All Events' },
    { value: 'deployment', label: 'Deployments' },
    { value: 'service', label: 'Services' },
    { value: 'llm', label: 'LLM Routes' },
    { value: 'policy', label: 'Policies' },
    { value: 'sbom', label: 'SBOM' },
    { value: 'artifact', label: 'Artifacts' }
  ];

  // Map event types to categories
  function getEventCategory(event) {
    const type = event.type?.toLowerCase() || '';
    const kind = event.kind;
    
    // SBOM events
    if (kind === KINDS.SBOM_REFERENCE || kind === KINDS.SBOM_AVAILABILITY_LIST) {
      return 'sbom';
    }
    if (type.includes('sbom')) {
      return 'sbom';
    }
    
    // Deployment events
    if (type.includes('deployment') || type.includes('deploy')) {
      return 'deployment';
    }
    
    // Service events
    if (type.includes('service')) {
      return 'service';
    }
    
    // LLM events
    if (type.includes('llm') || type.includes('route')) {
      return 'llm';
    }
    
    // Policy events
    if (type.includes('policy')) {
      return 'policy';
    }
    
    // Artifact events
    if (type.includes('artifact')) {
      return 'artifact';
    }
    
    return 'other';
  }

  // Get human-readable event type label
  function getEventTypeLabel(event) {
    if (event.kind === KINDS.SBOM_REFERENCE) {
      return 'SBOM Reference';
    }
    if (event.kind === KINDS.SBOM_AVAILABILITY_LIST) {
      return 'SBOM Availability List';
    }
    return event.type || 'Unknown';
  }

  // Get badge variant for event category
  function getEventBadge(event) {
    const cat = getEventCategory(event);
    const variants = {
      sbom: 'info',
      deployment: 'primary',
      service: 'success',
      llm: 'warning',
      policy: 'default',
      artifact: 'primary'
    };
    return variants[cat] || 'default';
  }

  let filteredEvents = $derived(
    eventTypeFilter === 'all'
      ? events
      : events.filter(e => getEventCategory(e) === eventTypeFilter)
  );

  let totalPages = $derived(Math.max(1, Math.ceil(filteredEvents.length / pageSize)));
  let pagedEvents = $derived(filteredEvents.slice((currentPage - 1) * pageSize, currentPage * pageSize));
  let pageStart = $derived(filteredEvents.length === 0 ? 0 : (currentPage - 1) * pageSize + 1);
  let pageEnd = $derived(Math.min(filteredEvents.length, currentPage * pageSize));

  // Reset to page 1 when filter changes or events list grows
  $effect(() => {
    void filteredEvents.length;
    void pageSize;
    if (currentPage > totalPages) currentPage = totalPages;
  });

  let columns = $derived([
    { key: 'time', label: 'Time', render: (r) => r.time || '-' },
    {
      key: 'type',
      label: 'Event Type',
      render: (r) => {
        const label = getEventTypeLabel(r);
        const variant = getEventBadge(r);
        return `<span class="event-type-cell"><span class="badge-inline ${variant}">${label}</span></span>`;
      }
    },
    { key: 'entity_id', label: 'Entity ID', render: (r) => r.entity_id ? `<code>${r.entity_id}</code>` : '-' },
    {
      key: 'data',
      label: 'Data',
      render: (r) => {
        if (!r.data) return '-';
        const str = JSON.stringify(r.data);
        return `<code>${str.slice(0, 80)}${str.length > 80 ? '...' : ''}</code>`;
      }
    }
  ]);

  let statusDisplay = $derived({
    idle: '⚪ Not connected',
    discovering: '🟡 Discovering relay',
    connecting: '🟡 Connecting to relay',
    bootstrapping: '🟡 Waiting for initial sync',
    live: '🟢 Connected via Nostr relay',
    disconnected: '⚪ Relay disconnected',
    error: '🔴 Relay error'
  }[controlplaneConnection.status] || '⚪ Unknown');

  let relayProvenance = $derived(
    controlplaneConnection.relays.length > 0
      ? `Relays: ${controlplaneConnection.relays.join(', ')}`
      : 'No relays advertised/configured/connected.'
  );

  // Count SBOM events for badge
  let sbomEventCount = $derived(
    events.filter(e =>
      e.kind === KINDS.SBOM_REFERENCE ||
      e.kind === KINDS.SBOM_AVAILABILITY_LIST ||
      e.type?.toLowerCase().includes('sbom')
    ).length
  );
</script>

<div class="page">
  <div class="header">
    <div class="header-left">
      <h1>Live Events</h1>
      <span class="status">{statusDisplay}</span>
    </div>
    <div class="header-right">
      {#if sbomEventCount > 0}
        <Badge variant="info">
          {sbomEventCount} SBOM event{sbomEventCount !== 1 ? 's' : ''}
        </Badge>
      {/if}
    </div>
  </div>

  <p class="hint">
    Events are rendered from relay-backed Bahia read-model/status/audit subscriptions.
    {relayProvenance}
  </p>

  {#if controlplaneConnection.lastError}
    <p class="error">{controlplaneConnection.lastError}</p>
  {/if}

  <!-- Event Type Filter -->
  <div class="filters">
    <div class="filter-field">
      <label for="event-type-filter">Event Type</label>
      <Select
        id="event-type-filter"
        bind:value={eventTypeFilter}
        options={eventCategories}
      />
    </div>
    <div class="filter-stats">
      Showing {filteredEvents.length} of {events.length} events
    </div>
  </div>

  <div class="table-card">
    <div class="table-toolbar">
      <span>{pageStart}-{pageEnd} of {filteredEvents.length}</span>
      <label>
        Rows
        <select bind:value={pageSize} onchange={() => currentPage = 1}>
          {#each PAGE_SIZE_OPTIONS as size}
            <option value={size}>{size}</option>
          {/each}
        </select>
      </label>
    </div>
    <div class="table-wrap">
      <Table columns={columns} data={pagedEvents} />
    </div>
  </div>

  {#if totalPages > 1}
    <div class="pagination">
      <button
        class="page-btn"
        disabled={currentPage === 1}
        onclick={() => currentPage--}
        aria-label="Previous page"
      >‹ Prev</button>
      <span class="page-info">Page {currentPage} of {totalPages} · showing {pageStart}-{pageEnd}</span>
      <button
        class="page-btn"
        disabled={currentPage === totalPages}
        onclick={() => currentPage++}
        aria-label="Next page"
      >Next ›</button>
    </div>
  {:else}
    <p class="event-count">{filteredEvents.length} event{filteredEvents.length === 1 ? '' : 's'}</p>
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
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }

  .header-left {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  .header-right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  h1 { margin: 0; }

  .status { color: var(--success); font-size: 0.875rem; }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-bottom: 1rem; }
  .error { color: var(--error); font-size: 0.875rem; margin-bottom: 1rem; }

  /* Filters */
  .filters {
    display: flex;
    align-items: flex-end;
    gap: 1rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }

  .filter-field {
    min-width: 200px;
  }

  .filter-field label {
    display: block;
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-bottom: 0.25rem;
  }

  .filter-stats {
    color: var(--text-muted);
    font-size: 0.875rem;
    padding-bottom: 0.5rem;
  }

  .table-card {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    overflow: hidden;
  }

  .table-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--border-color);
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .table-toolbar label {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .table-toolbar select {
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    color: var(--text-primary);
    padding: 0.25rem 0.5rem;
  }

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

  /* Inline badge styles for table cells */
  :global(.event-type-cell) {
    display: inline-flex;
    align-items: center;
  }

  :global(.badge-inline) {
    display: inline-flex;
    align-items: center;
    padding: 0.125rem 0.375rem;
    border-radius: 3px;
    font-weight: 500;
    font-size: 0.75rem;
  }

  :global(.badge-inline.default) { background: #374151; color: #d1d5db; }
  :global(.badge-inline.primary) { background: #1e3a8a; color: #bfdbfe; }
  :global(.badge-inline.success) { background: #065f46; color: #6ee7b7; }
  :global(.badge-inline.warning) { background: #78350f; color: #fcd34d; }
  :global(.badge-inline.error) { background: #7f1d1d; color: #fca5a5; }
  :global(.badge-inline.info) { background: #1e3a5f; color: #93c5fd; }
</style>
