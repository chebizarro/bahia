<script>
  import Table from '$lib/components/Table.svelte';
  import { workers, loading, loadWorkers } from '$lib/stores';
  import { goto } from '$app/navigation';
  import { inferWorkerStatus, getCapabilityOptions, filterWorkers } from './list-utils.js';

  let capabilityFilter = $state('');
  let capabilitySearch = $state('');

  $effect(() => {
    void loadWorkers();
  });

  const capabilityOptions = $derived(getCapabilityOptions(workers));
  const filteredWorkers = $derived(filterWorkers(workers, capabilityFilter, capabilitySearch));

  let columns = $derived([
    { key: 'name', label: 'Name' },
    { key: 'pubkey', label: 'Pubkey', render: (r) => `<code>${r.pubkey?.slice(0, 12)}...</code>` },
    {
      key: 'status',
      label: 'Status',
      render: (r) => {
        const status = inferWorkerStatus(r);
        return `<span class="worker-status status-${status}"><span class="status-dot" aria-hidden="true"></span>${status}</span>`;
      }
    },
    { key: 'capabilities', label: 'Capabilities', render: (r) => (r.capabilities || []).join(', ') || '-' },
    { key: 'price_per_sec', label: 'Price', render: (r) => `${r.price_per_sec || 0} sats/sec` },
    { key: 'last_seen', label: 'Last Seen', render: (r) => r.last_seen?.slice(0, 19) || '-' }
  ]);
</script>

<div class="page">
  <div class="header">
    <h1>Workers</h1>
    <span class="count">{filteredWorkers.length} of {workers.length} workers</span>
  </div>

  <div class="filters">
    <label>
      <span>Capability</span>
      <select bind:value={capabilityFilter}>
        <option value="">All capabilities</option>
        {#each capabilityOptions as capability}
          <option value={capability}>{capability}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Capability search</span>
      <input bind:value={capabilitySearch} type="search" placeholder="Search capabilities" />
    </label>
  </div>

  {#if loading.workers}
    <p class="loading">Loading...</p>
  {:else}
    <Table columns={columns} data={filteredWorkers} onRowClick={(row) => goto(`/workers/${encodeURIComponent(row.pubkey)}`)} />
  {/if}
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }

  .filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.75rem;
    margin-bottom: 1rem;
  }

  .filters label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .filters select,
  .filters input {
    padding: 0.5rem 0.625rem;
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 0.375rem;
    background: var(--surface-bg, #141426);
    color: var(--text-color, #fff);
  }

  :global(.worker-status) {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    text-transform: capitalize;
  }

  :global(.status-dot) {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 999px;
    display: inline-block;
  }

  :global(.status-online .status-dot) { background: #22c55e; }
  :global(.status-offline .status-dot) { background: #ef4444; }
</style>
