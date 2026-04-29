<script>
  import Table from '$lib/components/Table.svelte';
  import { workers, loading, loadWorkers } from '$lib/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  onMount(() => loadWorkers());

  $: columns = [
    { key: 'name', label: 'Name' },
    { key: 'pubkey', label: 'Pubkey', render: (r) => `<code>${r.pubkey?.slice(0, 12)}...</code>` },
    { key: 'capabilities', label: 'Capabilities', render: (r) => (r.capabilities || []).join(', ') },
    { key: 'price_per_sec', label: 'Price', render: (r) => `${r.price_per_sec || 0} sats/sec` },
    { key: 'last_seen', label: 'Last Seen', render: (r) => r.last_seen?.slice(0, 19) || '-' }
  ];
</script>

<div class="page">
  <div class="header">
    <h1>Workers</h1>
    <span class="count">{$workers.length} workers available</span>
  </div>

  {#if $loading.workers}
    <p class="loading">Loading...</p>
  {:else}
    <Table columns={columns} data={$workers} onRowClick={(row) => goto(`/workers/${encodeURIComponent(row.pubkey)}`)} />
  {/if}
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }
</style>
