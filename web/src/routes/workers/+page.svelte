<script>
  import Table from '$lib/components/Table.svelte';
  import { workers, loading, loadWorkers } from '$lib/stores';
  import { goto } from '$app/navigation';
  import { StandardIcon, AcceleratorIcon } from '$lib/icons/domain-icons.js';
  import {
    inferWorkerStatus,
    getCapabilityOptions,
    getRuntimeOptions,
    getArtifactFormatOptions,
    getAcceleratorOptions,
    getToolchainOptions,
    getMLTaskOptions,
    filterWorkers,
    workerRuntimesLabel,
    workerAcceleratorsLabel,
    workerFormatsLabel,
    workerToolchainsLabel,
    workerVRAMLabel
  } from './list-utils.js';

  let capabilityFilter = $state('');
  let capabilitySearch = $state('');
  let runtimeFilter = $state('');
  let formatFilter = $state('');
  let acceleratorFilter = $state('');
  let toolchainFilter = $state('');
  let taskFilter = $state('');

  $effect(() => {
    void loadWorkers();
  });

  const capabilityOptions = $derived(getCapabilityOptions(workers));
  const runtimeOptions = $derived(getRuntimeOptions(workers));
  const formatOptions = $derived(getArtifactFormatOptions(workers));
  const acceleratorOptions = $derived(getAcceleratorOptions(workers));
  const toolchainOptions = $derived(getToolchainOptions(workers));
  const taskOptions = $derived(getMLTaskOptions(workers));

  const mlFilters = $derived({ runtimeFilter, formatFilter, acceleratorFilter, toolchainFilter, taskFilter });
  const filteredWorkers = $derived(filterWorkers(workers, capabilityFilter, capabilitySearch, mlFilters));

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
    { key: 'runtimes', label: 'Runtimes', render: (r) => workerRuntimesLabel(r) },
    { key: 'accelerators', label: 'Accelerators', render: (r) => workerAcceleratorsLabel(r) },
    { key: 'formats', label: 'Formats', render: (r) => workerFormatsLabel(r) },
    { key: 'toolchains', label: 'Toolchains', render: (r) => workerToolchainsLabel(r) },
    { key: 'vram', label: 'VRAM', render: (r) => workerVRAMLabel(r) },
    { key: 'price_per_sec', label: 'Price', render: (r) => `${r.price_per_sec || 0} sats/sec` },
    { key: 'last_seen', label: 'Last Seen', render: (r) => r.last_seen?.slice(0, 19) || '-' }
  ]);
</script>

<div class="page">
  <div class="header">
    <h1>
      <StandardIcon size={28} strokeWidth={1.75} aria-hidden="true" />
      Workers
    </h1>
    <span class="count">{filteredWorkers.length} of {workers.length} workers</span>
  </div>

  <div class="filters">
    <label>
      <span>Search</span>
      <input bind:value={capabilitySearch} type="search" placeholder="Search workers…" />
    </label>

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
      <span>Runtime</span>
      <select bind:value={runtimeFilter}>
        <option value="">All runtimes</option>
        {#each runtimeOptions as runtime}
          <option value={runtime}>{runtime}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Artifact Format</span>
      <select bind:value={formatFilter}>
        <option value="">All formats</option>
        {#each formatOptions as format}
          <option value={format}>{format}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Accelerator</span>
      <select bind:value={acceleratorFilter}>
        <option value="">All accelerators</option>
        {#each acceleratorOptions as accelerator}
          <option value={accelerator}>{accelerator}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Toolchain</span>
      <select bind:value={toolchainFilter}>
        <option value="">All toolchains</option>
        {#each toolchainOptions as toolchain}
          <option value={toolchain}>{toolchain}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>ML Task</span>
      <select bind:value={taskFilter}>
        <option value="">All tasks</option>
        {#each taskOptions as task}
          <option value={task}>{task}</option>
        {/each}
      </select>
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
  h1 { display: inline-flex; align-items: center; gap: 0.5rem; }
  h1 :global(svg) { display: block; flex-shrink: 0; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }

  .filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
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
