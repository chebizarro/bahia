<script>
  import Modal from '$lib/components/Modal.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfigFabricDriftTable from '$lib/config-fabric/ConfigFabricDriftTable.svelte';
  import ConfigPublishForm from '$lib/config-fabric/ConfigPublishForm.svelte';
  import { initialConfigPublishForm } from '$lib/config-fabric/model.js';
  import api from '$lib/api/client.js';
  import { ConfiguredIcon, WarningIcon } from '$lib/icons/domain-icons.js';

  let rows = $state([]);
  let loading = $state(true);
  let error = $state('');
  let publishOpen = $state(false);

  let withdrawnCount = $derived(rows.filter((row) => row.withdrawn).length);
  let driftCount = $derived(rows.filter((row) => row.drift && !row.withdrawn).length);

  $effect(() => {
    void loadDrift();
  });

  async function loadDrift() {
    loading = true;
    error = '';
    try {
      rows = await api.listConfigFabricDrift();
    } catch (err) {
      error = err?.message || 'Failed to load Config Fabric drift';
    } finally {
      loading = false;
    }
  }

  async function handlePublished() {
    publishOpen = false;
    await loadDrift();
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <h1>Config Fabric</h1>
      <p>Signed desired configuration, effective versions, drift, and rejection status.</p>
    </div>
    <LoadingButton variant="primary" onclick={() => { publishOpen = true; }}>Publish Config</LoadingButton>
  </div>

  {#if loading}
    <p class="loading">Loading Config Fabric status...</p>
  {:else if error}
    <section class="error-state" role="alert">
      <p>Error: {error}</p>
      <LoadingButton variant="secondary" onclick={loadDrift}>Retry</LoadingButton>
    </section>
  {:else if rows.length === 0}
    <EmptyState
      iconComponent={ConfiguredIcon}
      title="No managed configuration"
      message="Publish a signed desired configuration to begin tracking effective state and drift."
      actionLabel="Publish Config"
      onAction={() => { publishOpen = true; }}
    />
  {:else}
    <div class="summary" aria-label="Config Fabric summary">
      <div><strong>{rows.length}</strong><span>service policies</span></div>
      <div class:warning={driftCount > 0}><strong>{driftCount}</strong><span>drifted</span></div>
      {#if withdrawnCount > 0}
        <div class="warning"><strong>{withdrawnCount}</strong><span>withdrawn</span></div>
      {/if}
    </div>
    {#if driftCount > 0}
      <p class="drift-warning"><WarningIcon size={18} /> {driftCount} coordinate{driftCount === 1 ? '' : 's'} differ from effective state.</p>
    {/if}
    {#if withdrawnCount > 0}
      <p class="drift-warning" role="status">
        <WarningIcon size={18} />
        {withdrawnCount} desired config{withdrawnCount === 1 ? ' was' : 's were'} deleted or expired. The relay keeps
        the last applied config live (the allowlist is intentionally not reverted); open the policy to publish a replacement.
      </p>
    {/if}
    <ConfigFabricDriftTable {rows} />
  {/if}
</div>

<Modal bind:open={publishOpen} title="Publish Config Fabric Change" titleIcon={ConfiguredIcon} size="lg">
  <ConfigPublishForm
    initial={initialConfigPublishForm()}
    driftRows={rows}
    onPublished={handlePublished}
    onCancel={() => { publishOpen = false; }}
  />
</Modal>

<style>
  .header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; margin-bottom: 1.5rem; }
  h1 { margin-bottom: 0.25rem; }
  .header p { color: var(--text-muted); margin: 0; }
  .loading, .error-state { color: var(--text-muted); padding: 2rem; text-align: center; }
  .error-state p { color: var(--error); }
  .summary { display: flex; gap: 1rem; margin-bottom: 1rem; }
  .summary div { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 8px; display: flex; flex-direction: column; min-width: 150px; padding: 1rem; }
  .summary div.warning { border-left: 4px solid #f59e0b; }
  .summary strong { font-size: 1.5rem; }
  .summary span { color: var(--text-muted); font-size: 0.75rem; }
  .drift-warning { align-items: center; background: rgba(245, 158, 11, 0.1); border-radius: 4px; color: #fcd34d; display: flex; gap: 0.5rem; padding: 0.75rem; }
  @media (max-width: 640px) {
    .header { flex-direction: column; }
  }
</style>
