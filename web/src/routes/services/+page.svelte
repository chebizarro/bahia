<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import { services, loading, loadServices } from '$lib/stores';
  import { onMount } from 'svelte';

  onMount(() => loadServices());

  $: columns = [
    { key: 'name', label: 'Name' },
    { key: 'artifact_repo', label: 'Artifact Repo' },
    { key: 'runtime_type', label: 'Runtime' },
    { key: 'default_branch', label: 'Branch' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ];
</script>

<div class="page">
  <div class="header">
    <h1>Services</h1>
    <span class="count">{$services.length} services</span>
  </div>

  {#if $loading.services}
    <p class="loading">Loading...</p>
  {:else}
    <Table {columns} data={$services} onRowClick={(row) => goto(`/services/${row.id}`)} />
  {/if}
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }
</style>
