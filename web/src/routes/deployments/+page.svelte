<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Select from '$lib/components/Select.svelte';
  import { api } from '$lib/api/client.js';

  const PAGE_SIZE = 25;

  let intents = $state([]);
  let services = $state([]);
  let environments = $state([]);
  let loading = $state(true);
  let error = $state(null);

  let statusFilter = $state('all');
  let serviceFilter = $state('all');
  let environmentFilter = $state('all');
  let startDate = $state('');
  let endDate = $state('');
  let currentPage = $state(1);

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
      [services, environments] = await Promise.all([api.listServices(), api.listEnvironments()]);

      const intentPromises = [];

      for (const service of services) {
        for (const env of environments) {
          const promise = api
            .listIntents(service.id, env.id)
            .then((result) => {
              const intentList = Array.isArray(result) ? result : [];
              return intentList.map((intent) => ({
                ...intent,
                service_name: service.name,
                environment_name: env.name,
                intent_status: getIntentStatus(intent)
              }));
            })
            .catch((err) => {
              console.warn(`Failed to load intents for ${service.name}/${env.name}:`, err);
              return [];
            });

          intentPromises.push(promise);
        }
      }

      const intentArrays = await Promise.all(intentPromises);
      const allIntents = intentArrays.flat();
      const intentMap = new Map();
      allIntents.forEach((intent) => {
        if (intent.id) intentMap.set(intent.id, intent);
      });

      intents = Array.from(intentMap.values()).sort((a, b) => {
        const dateA = a.created_at ? new Date(a.created_at) : new Date(0);
        const dateB = b.created_at ? new Date(b.created_at) : new Date(0);
        return dateB - dateA;
      });
    } catch (err) {
      error = err.message || 'Failed to load deployment history';
      console.error('Error loading deployment history:', err);
    } finally {
      loading = false;
    }
  }

  function goToPreviousPage() {
    if (currentPage > 1) currentPage -= 1;
  }

  function goToNextPage() {
    if (currentPage < totalPages) currentPage += 1;
  }
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
    <Table {columns} data={pagedIntents} onRowClick={(row) => goto(`/deployments/${row.id}`)} />

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
</style>
