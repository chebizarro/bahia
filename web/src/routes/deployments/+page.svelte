<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Select from '$lib/components/Select.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { services, environments, deploymentIntents, artifacts as allArtifacts, loadServices, loadEnvironments, loadDeploymentIntents, loadArtifacts } from '$lib/stores';
  import { createDeploymentIntent, rollbackDeployment } from '$lib/stores/public-controlplane.svelte.js';

  const PAGE_SIZE = 25;

  let loading = $state(true);
  let error = $state(null);

  let statusFilter = $state('all');
  let serviceFilter = $state('all');
  let environmentFilter = $state('all');
  let startDate = $state('');
  let endDate = $state('');
  let currentPage = $state(1);

  let rollbackOpen = $state(false);
  let rollbackSubmitting = $state(false);
  let rollbackError = $state(null);
  let rollbackIntent = $state(null);
  let rollbackArtifacts = $state([]);
  let rollbackTargetMode = $state('previous');
  let rollbackArtifactId = $state('');

  const statusOptions = [
    { value: 'all', label: 'All Statuses' },
    { value: 'pending', label: 'Pending' },
    { value: 'approved', label: 'Approved' },
    { value: 'rejected', label: 'Rejected' },
    { value: 'running', label: 'Running' },
    { value: 'completed', label: 'Completed' },
    { value: 'failed', label: 'Failed' }
  ];

  let serviceOptions = $derived([
    { value: 'all', label: 'All Services' },
    ...services.map((service) => ({ value: service.id, label: service.name }))
  ]);

  let environmentOptions = $derived([
    { value: 'all', label: 'All Environments' },
    ...environments.map((environment) => ({ value: environment.id, label: environment.name }))
  ]);

  function normalizeDeploymentStatus(status) {
    const value = String(status || '').toLowerCase();
    if (!value || value === 'pending' || value === 'queued') return 'pending';
    if (value === 'running') return 'running';
    if (value === 'succeeded' || value === 'completed') return 'completed';
    if (value === 'failed' || value === 'cancelled') return 'failed';
    return value;
  }

  function getIntentStatus(intent) {
    const approvalStatus = String(intent.approval_status || '').toLowerCase();

    if (approvalStatus === 'rejected') return 'rejected';
    if (approvalStatus === 'pending') return 'pending';

    if (approvalStatus === 'approved') {
      const deploymentStatus = normalizeDeploymentStatus(intent.deployment_status);
      if (deploymentStatus === 'pending') return 'approved';
      if (deploymentStatus === 'running') return 'running';
      if (deploymentStatus === 'completed') return 'completed';
      if (deploymentStatus === 'failed') return 'failed';
      return 'approved';
    }

    return normalizeDeploymentStatus(intent.deployment_status);
  }

  function getStatusColor(status) {
    const colors = {
      pending: '#f59e0b',
      approved: '#10b981',
      rejected: '#ef4444',
      running: '#3b82f6',
      completed: '#10b981',
      failed: '#ef4444'
    };
    return colors[String(status || '').toLowerCase()] || '#888';
  }

  function toDateStart(value) {
    if (!value) return null;
    return new Date(`${value}T00:00:00`);
  }

  function toDateEnd(value) {
    if (!value) return null;
    return new Date(`${value}T23:59:59.999`);
  }

  let columns = $derived([
    {
      key: 'rollback_action',
      label: 'Rollback',
      render: (r) => {
        if (!r?.service_id || !r?.environment_id) return '-';
        return `<button type="button" class="rollback-btn" data-rollback-id="${r.id}">Rollback</button>`;
      }
    },
    { key: 'service_name', label: 'Service' },
    { key: 'environment_name', label: 'Environment' },
    { 
      key: 'artifact_id', 
      label: 'Artifact', 
      render: (r) => (r.artifact_id ? `<code>${r.artifact_id.slice(0, 12)}...</code>` : '-')
    },
    { 
      key: 'intent_status',
      label: 'Status',
      render: (r) => {
        const status = String(r.intent_status || '').toLowerCase();
        const color = getStatusColor(status);
        return `<span style="color: ${color}; font-weight: 500;">${status}</span>`;
      }
    },
    { key: 'requested_by', label: 'Requested By' },
    { 
      key: 'created_at', 
      label: 'Created',
      render: (r) => (r.created_at ? new Date(r.created_at).toLocaleString() : '-')
    }
  ]);

  let intents = $derived.by(() => {
    const serviceById = new Map(services.map((service) => [service.id, service]));
    const environmentById = new Map(environments.map((environment) => [environment.id, environment]));
    const allIntents = deploymentIntents.map((intent) => ({
      ...intent,
      service_name: serviceById.get(intent.service_id)?.name || intent.service_id,
      environment_name: environmentById.get(intent.environment_id)?.name || intent.environment_id,
      intent_status: getIntentStatus(intent)
    }));
    const intentMap = new Map();
    allIntents.forEach((intent) => {
      if (intent.id) intentMap.set(intent.id, intent);
    });

    return Array.from(intentMap.values()).sort((a, b) => {
      const dateA = a.created_at ? new Date(a.created_at) : new Date(0);
      const dateB = b.created_at ? new Date(b.created_at) : new Date(0);
      return dateB - dateA;
    });
  });

  let filteredIntents = $derived(intents.filter((intent) => {
    if (statusFilter !== 'all' && intent.intent_status !== statusFilter) return false;
    if (serviceFilter !== 'all' && intent.service_id !== serviceFilter) return false;
    if (environmentFilter !== 'all' && intent.environment_id !== environmentFilter) return false;

    const createdAt = intent.created_at ? new Date(intent.created_at) : null;

    if (!createdAt && (startDate || endDate)) return false;

    if (createdAt && startDate) {
      const start = toDateStart(startDate);
      if (start && createdAt < start) return false;
    }

    if (createdAt && endDate) {
      const end = toDateEnd(endDate);
      if (end && createdAt > end) return false;
    }

    return true;
  }));

  let totalPages = $derived(Math.max(1, Math.ceil(filteredIntents.length / PAGE_SIZE)));

  let pagedIntents = $derived(
    filteredIntents.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE)
  );

  $effect(() => {
    statusFilter;
    serviceFilter;
    environmentFilter;
    startDate;
    endDate;
    currentPage = 1;
  });

  $effect(() => {
    if (currentPage > totalPages) {
      currentPage = totalPages;
    }
  });

  $effect(() => {
    void loadAllIntents();
  });

  async function loadAllIntents() {
    loading = true;
    error = null;

    try {
      await Promise.all([loadServices(), loadEnvironments(), loadDeploymentIntents()]);
    } catch (err) {
      error = err.message || 'Failed to load deployment history';
      console.error('Error loading deployment history:', err);
    } finally {
      loading = false;
    }
  }

  async function openRollbackModal(intent) {
    rollbackIntent = intent;
    rollbackTargetMode = 'previous';
    rollbackArtifactId = '';
    rollbackError = null;
    rollbackArtifacts = [];

    try {
      await loadArtifacts();
      rollbackArtifacts = allArtifacts.filter((artifact) => artifact.service_id === intent.service_id);
    } catch (err) {
      rollbackError = err.message || 'Failed to load artifacts';
    }

    rollbackOpen = true;
  }

  async function handleRollbackConfirm() {
    if (!rollbackIntent) return;
    if (rollbackTargetMode === 'artifact' && !rollbackArtifactId) {
      rollbackError = 'Select an artifact target';
      return;
    }

    rollbackSubmitting = true;
    rollbackError = null;

    try {
      if (rollbackTargetMode === 'previous') {
        await rollbackDeployment(rollbackIntent.service_id, rollbackIntent.environment_id);
      } else {
        await createDeploymentIntent(
          rollbackIntent.service_id,
          rollbackIntent.environment_id,
          rollbackArtifactId
        );
      }
      rollbackOpen = false;
      await loadAllIntents();
    } catch (err) {
      rollbackError = err.message || 'Failed to create rollback intent';
    } finally {
      rollbackSubmitting = false;
    }
  }

  function handleTableRowClick(row, event) {
    const rollbackButton = event?.target?.closest('.rollback-btn');
    if (rollbackButton) {
      event.preventDefault();
      event.stopPropagation();
      void openRollbackModal(row);
      return;
    }
    goto(`/deployments/${row.id}`);
  }

  function goToPreviousPage() {
    if (currentPage > 1) currentPage -= 1;
  }

  function goToNextPage() {
    if (currentPage < totalPages) currentPage += 1;
  }

  let rollbackArtifactOptions = $derived(
    rollbackArtifacts.map((artifact) => ({
      value: artifact.id,
      label: artifact.image_tag || artifact.version || artifact.name || artifact.id
    }))
  );
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Deployment History</h1>
      <span class="count">{filteredIntents.length} of {intents.length} deployments</span>
    </div>
  </div>

  <div class="filters">
    <div class="filter-field">
      <label for="status-filter">Status</label>
      <Select id="status-filter" bind:value={statusFilter} options={statusOptions} disabled={loading} />
    </div>

    <div class="filter-field">
      <label for="service-filter">Service</label>
      <Select id="service-filter" bind:value={serviceFilter} options={serviceOptions} disabled={loading} />
    </div>

    <div class="filter-field">
      <label for="environment-filter">Environment</label>
      <Select
        id="environment-filter"
        bind:value={environmentFilter}
        options={environmentOptions}
        disabled={loading}
      />
    </div>

    <div class="filter-field date-field">
      <label for="start-date-filter">Start Date</label>
      <input id="start-date-filter" type="date" bind:value={startDate} disabled={loading} />
    </div>

    <div class="filter-field date-field">
      <label for="end-date-filter">End Date</label>
      <input id="end-date-filter" type="date" bind:value={endDate} disabled={loading} />
    </div>
  </div>

  {#if loading}
    <p class="loading">Loading deployment history...</p>
  {:else if error}
    <div class="error-state">
      <p class="error">⚠️ {error}</p>
    </div>
  {:else if intents.length === 0}
    <EmptyState
      icon="🚀"
      title="No deployments yet"
      message="Deployment intents will appear here once services are deployed to environments"
    />
  {:else if filteredIntents.length === 0}
    <EmptyState
      icon="🔍"
      title="No deployments match current filters"
      message="Try adjusting your filter criteria"
    />
  {:else}
    <Table {columns} data={pagedIntents} onRowClick={handleTableRowClick} />

    {#if filteredIntents.length > PAGE_SIZE}
      <div class="pagination" aria-label="Deployment history pagination">
        <button type="button" class="page-btn" onclick={goToPreviousPage} disabled={currentPage === 1}>
          Previous
        </button>
        <span class="page-status">Page {currentPage} of {totalPages}</span>
        <button type="button" class="page-btn" onclick={goToNextPage} disabled={currentPage === totalPages}>
          Next
        </button>
      </div>
    {/if}
  {/if}
</div>

<style>
  .page {
    padding: 0;
  }

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

  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .filters {
    display: grid;
    gap: 1rem;
    margin-bottom: 1.5rem;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .filter-field label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }

  .date-field input {
    border: 1px solid var(--border-color, #2a2a4a);
    background: var(--card-bg, #1a1a2e);
    color: var(--text-primary, #e5e7eb);
    border-radius: 0.375rem;
    padding: 0.5rem 0.75rem;
    min-height: 2.25rem;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .error-state {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0;
  }

  .pagination {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 0.75rem;
    margin-top: 1rem;
  }

  .page-status {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .page-btn {
    border: 1px solid var(--border-color, #2a2a4a);
    background: var(--card-bg, #1a1a2e);
    color: var(--text-primary, #e5e7eb);
    border-radius: 0.375rem;
    padding: 0.4rem 0.75rem;
    cursor: pointer;
  }

  .page-btn:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  :global(.rollback-btn) {
    border: 1px solid var(--border-color, #2a2a4a);
    background: var(--card-bg, #1a1a2e);
    color: var(--text-primary, #e5e7eb);
    border-radius: 0.375rem;
    padding: 0.3rem 0.6rem;
    cursor: pointer;
  }

  :global(.rollback-btn:hover) {
    border-color: var(--primary, #6366f1);
  }

  .rollback-body {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .rollback-option {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .rollback-error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0;
  }
</style>

<ConfirmDialog
  bind:open={rollbackOpen}
  title="Confirm Rollback"
  confirmLabel="Create Rollback Intent"
  loading={rollbackSubmitting}
  onConfirm={handleRollbackConfirm}
  onCancel={() => { rollbackOpen = false; rollbackError = null; }}
  onClose={() => { rollbackOpen = false; rollbackError = null; }}
>
  <div class="rollback-body">
    <p>
      {#if rollbackIntent}
        Roll back <strong>{rollbackIntent.service_name}</strong> in <strong>{rollbackIntent.environment_name}</strong>.
      {/if}
    </p>

    <label class="rollback-option">
      <input type="radio" name="rollback-target-history" bind:group={rollbackTargetMode} value="previous" />
      Previous successful version (automatic)
    </label>

    <label class="rollback-option">
      <input type="radio" name="rollback-target-history" bind:group={rollbackTargetMode} value="artifact" />
      Specific artifact
    </label>

    {#if rollbackTargetMode === 'artifact'}
      <Select
        id="rollback-artifact-history"
        bind:value={rollbackArtifactId}
        options={rollbackArtifactOptions}
        placeholder={rollbackArtifactOptions.length > 0 ? 'Select artifact' : 'No artifacts available'}
        disabled={rollbackSubmitting || rollbackArtifactOptions.length === 0}
      />
    {/if}

    {#if rollbackError}
      <p class="rollback-error">{rollbackError}</p>
    {/if}
  </div>
</ConfirmDialog>
