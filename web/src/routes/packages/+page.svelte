<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { ArtifactIcon, WarningIcon } from '$lib/icons/domain-icons.js';
  import {
    packageRepositories,
    packageArtifacts,
    operations,
    loadPackageRepositories,
    loadPackageArtifacts
  } from '$lib/stores';
  import { latestPackageOperation, packageOperationLabel } from './page-model.js';

  let loading = $state(true);
  let error = $state(null);

  $effect(() => {
    void loadPackages();
  });

  async function loadPackages() {
    loading = true;
    error = null;
    try {
      await Promise.all([loadPackageRepositories(), loadPackageArtifacts()]);
    } catch (err) {
      error = err.message || 'Failed to load package repositories';
    } finally {
      loading = false;
    }
  }

  function artifactCount(repository) {
    return packageArtifacts.filter((artifact) => artifact.repository_id === repository.id && !artifact.deleted).length;
  }

  let rows = $derived(packageRepositories
    .filter((repository) => !repository.deleted)
    .map((repository) => ({
      ...repository,
      artifact_count: artifactCount(repository),
      live_operation: latestPackageOperation(operations, repository.id)
    })));

  let columns = $derived([
    { key: 'name', label: 'Name', icon: ArtifactIcon, text: (row) => row.name || row.id },
    { key: 'backend_type', label: 'Backend Type', text: (row) => row.backend_type || '-' },
    { key: 'format', label: 'Format', text: (row) => row.format || '-' },
    { key: 'status', label: 'Status', text: (row) => row.status || '-' },
    { key: 'live_operation', label: 'Live outcome', text: (row) => row.live_operation ? `${packageOperationLabel(row.live_operation)} · ${row.live_operation.status || 'processing'}` : '-' },
    { key: 'artifact_count', label: 'Artifacts' }
  ]);
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>
        <ArtifactIcon size={28} strokeWidth={1.75} ariaHidden="true" />
        Packages
      </h1>
      <span class="count">{rows.length} repositories</span>
    </div>
  </div>

  {#if loading}
    <p class="loading">Loading package repositories...</p>
  {:else if error}
    <EmptyState iconComponent={WarningIcon} title="Error loading packages" message={error} />
  {:else if rows.length === 0}
    <EmptyState
      iconComponent={ArtifactIcon}
      title="No package repositories yet"
      message="Package repositories will appear here after they are registered by the control plane."
    />
  {:else}
    <Table {columns} data={rows} onRowClick={(row) => goto(`/packages/${row.id}`)} />
  {/if}
</div>

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }
</style>
