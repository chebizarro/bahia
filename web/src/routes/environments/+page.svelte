<script>
  import Table from '$lib/components/Table.svelte';
  import { environments, loading, loadEnvironments } from '$lib/stores';
  import { onMount } from 'svelte';

  onMount(() => loadEnvironments());

  $: columns = [
    { key: 'name', label: 'Name' },
    { key: 'deploy_strategy', label: 'Strategy' },
    { key: 'protected', label: 'Protected', render: (r) => r.protected ? '🔒' : '-' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ];
</script>

<div class="page">
  <div class="header">
    <h1>Environments</h1>
    <span class="count">{$environments.length} environments</span>
  </div>

  {#if $loading.environments}
    <p class="loading">Loading...</p>
  {:else}
    <Table columns={columns} data={$environments} />
  {/if}
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }
</style>
