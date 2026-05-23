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
    getSupportedWorkloadOptions,
    filterWorkers,
    workerRuntimesLabel,
    workerAcceleratorsLabel,
    workerFormatsLabel,
    workerToolchainsLabel,
    workerTasksLabel,
    workerVRAMLabel,
    workerPriceLabel,
    workerLastAdvertisementLabel
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
  const taskOptions = $derived(getSupportedWorkloadOptions(workers));

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
    { key: 'workloads', label: 'Supported Workloads', render: (r) => workerTasksLabel(r) },
    { key: 'runtimes', label: 'Runtimes', render: (r) => workerRuntimesLabel(r) },
    { key: 'accelerators', label: 'Accelerators', render: (r) => workerAcceleratorsLabel(r) },
    { key: 'formats', label: 'Formats', render: (r) => workerFormatsLabel(r) },
    { key: 'toolchains', label: 'Toolchains', render: (r) => workerToolchainsLabel(r) },
    { key: 'vram', label: 'VRAM', render: (r) => workerVRAMLabel(r) },
    { key: 'pricing', label: 'Pricing', render: (r) => workerPriceLabel(r) },
    { key: 'last_advertisement_at', label: 'Last Advertisement', render: (r) => workerLastAdvertisementLabel(r) }
  ]);
</script>

<div class="page">
  <div class="header">
    <h1>
      <StandardIcon size={28} strokeWidth={1.75} ariaHidden="true" />
      Workers
    </h1>
    <span class="count">{filteredWorkers.length} of {workers.length} workers</span>
  </div>
  <p class="subtitle">Shared execution pool for CI/CD, inference, and scheduled compute workloads.</p>

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
      <span>Task Type</span>
      <select bind:value={taskFilter}>
        <option value="">All task types</option>
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
  .subtitle { color: var(--text-muted); margin: -0.75rem 0 1.25rem; }
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
  :global(.status-stale .status-dot) { background: #f59e0b; }
  :global(.status-offline .status-dot) { background: #ef4444; }
</style>
