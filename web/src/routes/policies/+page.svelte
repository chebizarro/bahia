<script>
  import Table from '$lib/components/Table.svelte';
  import { api } from '$lib/api/client.js';
  import { onMount } from 'svelte';

  let policies = [];
  let loading = true;

  onMount(async () => {
    try {
      policies = await api.listPolicies() || [];
    } catch (err) {
      console.error('Failed to load policies:', err);
    } finally {
      loading = false;
    }
  });

  $: columns = [
    { key: 'name', label: 'Name' },
    { key: 'enforcement', label: 'Enforcement' },
    { key: 'enabled', label: 'Status', render: (r) => r.enabled ? '✅ Enabled' : '⏸️ Disabled' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ];
</script>

<div class="page">
  <div class="header">
    <h1>Policies</h1>
    <span class="count">{policies.length} policies</span>
  </div>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else}
    <Table columns={columns} data={policies} />
  {/if}
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }
</style>
