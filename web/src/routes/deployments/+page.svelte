<script>
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Select from '$lib/components/Select.svelte';
  import { api } from '$lib/api/client.js';

  let intents = [];
  let services = [];
  let environments = [];
  let loading = true;
  let error = null;
  let statusFilter = 'all';
  let approvalFilter = 'all';

  const statusOptions = [
    { value: 'all', label: 'All Statuses' },
    { value: 'pending', label: 'Pending' },
    { value: 'queued', label: 'Queued' },
    { value: 'running', label: 'Running' },
    { value: 'succeeded', label: 'Succeeded' },
    { value: 'failed', label: 'Failed' },
    { value: 'cancelled', label: 'Cancelled' }
  ];

  const approvalOptions = [
    { value: 'all', label: 'All Approvals' },
    { value: 'pending', label: 'Pending' },
    { value: 'approved', label: 'Approved' },
    { value: 'rejected', label: 'Rejected' }
  ];

  // Columns for the intents table
  $: columns = [
    { key: 'service_name', label: 'Service' },
    { key: 'environment_name', label: 'Environment' },
    { 
      key: 'artifact_id', 
      label: 'Artifact', 
      render: (r) => r.artifact_id ? `<code>${r.artifact_id.slice(0, 12)}...</code>` : '-'
    },
    { 
      key: 'approval_status', 
      label: 'Approval',
      render: (r) => {
        const status = String(r.approval_status || '').toLowerCase();
        const colors = { pending: '#f59e0b', approved: '#10b981', rejected: '#ef4444' };
        const color = colors[status] || '#888';
        return `<span style="color: ${color}; font-weight: 500;">${status}</span>`;
      }
    },
    { 
      key: 'deployment_status', 
      label: 'Status',
      render: (r) => {
        const status = String(r.deployment_status || '').toLowerCase();
        const colors = { 
          pending: '#888',
          queued: '#888', 
          running: '#3b82f6', 
          succeeded: '#10b981', 
          failed: '#ef4444',
          cancelled: '#6b7280'
        };
        const color = colors[status] || '#888';
        return `<span style="color: ${color}; font-weight: 500;">${status}</span>`;
      }
    },
    { key: 'requested_by', label: 'Requested By' },
    { 
      key: 'created_at', 
      label: 'Created',
      render: (r) => r.created_at ? new Date(r.created_at).toLocaleString() : '-'
    }
  ];

  // Filter intents based on current filters
  $: filteredIntents = intents.filter(intent => {
    // Status filter
    if (statusFilter !== 'all') {
      const deployStatus = String(intent.deployment_status || '').toLowerCase();
      if (deployStatus !== statusFilter) return false;
    }

    // Approval filter
    if (approvalFilter !== 'all') {
      const approvalStatus = String(intent.approval_status || '').toLowerCase();
      if (approvalStatus !== approvalFilter) return false;
    }

    return true;
  });

  onMount(() => {
    loadAllIntents();
  });

  async function loadAllIntents() {
    loading = true;
    error = null;

    try {
      // Load services and environments
      [services, environments] = await Promise.all([
        api.listServices(),
        api.listEnvironments()
      ]);

      // Build service and environment lookup maps
      const serviceMap = {};
      const envMap = {};
      services.forEach(s => { serviceMap[s.id] = s.name; });
      environments.forEach(e => { envMap[e.id] = e.name; });

      // Load intents for every service/environment pair
      const intentPromises = [];

      for (const service of services) {
        for (const env of environments) {
          const promise = api.listIntents(service.id, env.id)
            .then(result => {
              const intentList = Array.isArray(result) ? result : [];
              return intentList.map(intent => ({
                ...intent,
                service_name: service.name,
                environment_name: env.name
              }));
            })
            .catch(err => {
              // Log but don't fail the whole page if one pair fails
              console.warn(`Failed to load intents for ${service.name}/${env.name}:`, err);
              return [];
            });
          
          intentPromises.push(promise);
        }
      }

      // Wait for all intent requests
      const intentArrays = await Promise.all(intentPromises);
      
      // Flatten and deduplicate by intent ID
      const allIntents = intentArrays.flat();
      const intentMap = new Map();
      allIntents.forEach(intent => {
        if (intent.id) {
          intentMap.set(intent.id, intent);
        }
      });

      // Convert to array and sort by created_at descending
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
      <Select
        id="status-filter"
        bind:value={statusFilter}
        options={statusOptions}
        disabled={loading}
      />
    </div>
    <div class="filter-field">
      <label for="approval-filter">Approval</label>
      <Select
        id="approval-filter"
        bind:value={approvalFilter}
        options={approvalOptions}
        disabled={loading}
      />
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
    <Table 
      {columns} 
      data={filteredIntents} 
      onRowClick={(row) => goto(`/deployments/${row.id}`)}
    />
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
    display: flex;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }
  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-width: 200px;
  }
  .filter-field label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
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
</style>
