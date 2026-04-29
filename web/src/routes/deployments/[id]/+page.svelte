<script>
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { api } from '$lib/api/client.js';

  let intent = null;
  let runs = [];
  let service = null;
  let environment = null;
  let loading = true;
  let error = null;

  // Action state
  let approving = false;
  let rejecting = false;
  let actionError = null;
  let approveOpen = false;
  let rejectOpen = false;

  // Reactive: check if intent is pending approval
  $: isPending = intent && String(intent.approval_status || '').toLowerCase() === 'pending';

  // Columns for runs table
  $: runsColumns = [
    { 
      key: 'id', 
      label: 'Run ID', 
      render: (r) => r.id ? `<code>${r.id.slice(0, 12)}...</code>` : '-'
    },
    { 
      key: 'status', 
      label: 'Status',
      render: (r) => {
        const status = String(r.status || '').toLowerCase();
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
    { 
      key: 'worker_pubkey', 
      label: 'Worker', 
      render: (r) => r.worker_pubkey ? `<code>${r.worker_pubkey.slice(0, 12)}...</code>` : '-'
    },
    { key: 'exit_code', label: 'Exit Code' },
    { 
      key: 'created_at', 
      label: 'Created',
      render: (r) => r.created_at ? new Date(r.created_at).toLocaleString() : '-'
    },
    { 
      key: 'finished_at', 
      label: 'Finished',
      render: (r) => r.finished_at ? new Date(r.finished_at).toLocaleString() : '-'
    }
  ];

  onMount(() => {
    loadIntent();
  });

  async function loadIntent() {
    loading = true;
    error = null;

    try {
      const intentId = $page.params.id;
      
      // Load intent
      intent = await api.getIntent(intentId);

      // Load runs for this intent
      try {
        runs = await api.listRuns(intentId);
        if (!Array.isArray(runs)) {
          runs = [];
        }
      } catch (err) {
        console.warn('Failed to load runs:', err);
        runs = [];
      }

      // Load service name if service_id exists
      if (intent.service_id) {
        try {
          service = await api.getService(intent.service_id);
        } catch (err) {
          console.warn('Failed to load service:', err);
        }
      }

      // Load environment name if environment_id exists
      if (intent.environment_id) {
        try {
          environment = await api.getEnvironment(intent.environment_id);
        } catch (err) {
          console.warn('Failed to load environment:', err);
        }
      }

    } catch (err) {
      error = err.message || 'Failed to load deployment intent';
      console.error('Error loading intent:', err);
    } finally {
      loading = false;
    }
  }

  async function handleApprove() {
    if (!intent) return;

    approving = true;
    actionError = null;

    try {
      await api.approveIntent(intent.id);
      
      // Reload intent and runs to get updated state
      await loadIntent();
      
      // Close dialog
      approveOpen = false;
    } catch (err) {
      actionError = err.message || 'Failed to approve intent';
      console.error('Error approving intent:', err);
    } finally {
      approving = false;
    }
  }

  async function handleReject() {
    if (!intent) return;

    rejecting = true;
    actionError = null;

    try {
      await api.rejectIntent(intent.id);
      
      // Reload intent and runs to get updated state
      await loadIntent();
      
      // Close dialog
      rejectOpen = false;
    } catch (err) {
      actionError = err.message || 'Failed to reject intent';
      console.error('Error rejecting intent:', err);
    } finally {
      rejecting = false;
    }
  }

  function getApprovalStatusColor(status) {
    const normalized = String(status || '').toLowerCase();
    const colors = { pending: '#f59e0b', approved: '#10b981', rejected: '#ef4444' };
    return colors[normalized] || '#888';
  }

  function getDeploymentStatusColor(status) {
    const normalized = String(status || '').toLowerCase();
    const colors = { 
      pending: '#888',
      queued: '#888', 
      running: '#3b82f6', 
      succeeded: '#10b981', 
      failed: '#ef4444',
      cancelled: '#6b7280'
    };
    return colors[normalized] || '#888';
  }
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading deployment intent...</p>
  {:else if error}
    <div class="error-state">
      <p class="error">⚠️ {error}</p>
      <LoadingButton variant="secondary" on:click={() => goto('/deployments')}>
        Back to Deployments
      </LoadingButton>
    </div>
  {:else if intent}
    <div class="header">
      <div>
        <a href="/deployments" class="back-link">← Back to Deployments</a>
        <h1>Deployment Intent</h1>
        <p class="intent-id"><code>{intent.id}</code></p>
      </div>
      {#if isPending}
        <div class="actions">
          <LoadingButton 
            variant="primary" 
            on:click={() => { approveOpen = true; actionError = null; }}
          >
            Approve
          </LoadingButton>
          <LoadingButton 
            variant="danger" 
            on:click={() => { rejectOpen = true; actionError = null; }}
          >
            Reject
          </LoadingButton>
        </div>
      {/if}
    </div>

    <div class="cards-grid">
      <Card>
        <div class="detail-section">
          <h3>Service</h3>
          <p class="detail-value">{service?.name || intent.service_id || 'Unknown'}</p>
          {#if intent.service_id && !service}
            <p class="detail-subtitle"><code>{intent.service_id}</code></p>
          {/if}
        </div>
      </Card>

      <Card>
        <div class="detail-section">
          <h3>Environment</h3>
          <p class="detail-value">{environment?.name || intent.environment_id || 'Unknown'}</p>
          {#if intent.environment_id && !environment}
            <p class="detail-subtitle"><code>{intent.environment_id}</code></p>
          {/if}
        </div>
      </Card>

      <Card>
        <div class="detail-section">
          <h3>Approval Status</h3>
          <p class="detail-value" style="color: {getApprovalStatusColor(intent.approval_status)}">
            {intent.approval_status || 'Unknown'}
          </p>
        </div>
      </Card>

      <Card>
        <div class="detail-section">
          <h3>Deployment Status</h3>
          <p class="detail-value" style="color: {getDeploymentStatusColor(intent.deployment_status)}">
            {intent.deployment_status || 'Unknown'}
          </p>
        </div>
      </Card>
    </div>

    <div class="details-card">
      <Card>
        <h2>Intent Details</h2>
        <div class="details-grid">
          <div class="detail-item">
            <span class="detail-label">Artifact ID</span>
            <span class="detail-value">
              {#if intent.artifact_id}
                <code>{intent.artifact_id}</code>
              {:else}
                -
              {/if}
            </span>
          </div>
          <div class="detail-item">
            <span class="detail-label">Requested By</span>
            <span class="detail-value">{intent.requested_by || '-'}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">Created At</span>
            <span class="detail-value">
              {intent.created_at ? new Date(intent.created_at).toLocaleString() : '-'}
            </span>
          </div>
          <div class="detail-item">
            <span class="detail-label">Updated At</span>
            <span class="detail-value">
              {intent.updated_at ? new Date(intent.updated_at).toLocaleString() : '-'}
            </span>
          </div>
        </div>
      </Card>
    </div>

    <div class="runs-section">
      <h2>Deployment Runs ({runs.length})</h2>
      {#if runs.length === 0}
        <EmptyState
          icon="🏃"
          title="No runs yet"
          message="Deployment runs will appear here once the intent is approved and executed"
        />
      {:else}
        <Table columns={runsColumns} data={runs} />
      {/if}
    </div>
  {:else}
    <EmptyState
      icon="❓"
      title="Intent not found"
      message="The requested deployment intent does not exist"
    >
      <LoadingButton variant="secondary" on:click={() => goto('/deployments')}>
        Back to Deployments
      </LoadingButton>
    </EmptyState>
  {/if}
</div>

<!-- Approve confirmation dialog -->
<ConfirmDialog
  bind:open={approveOpen}
  title="Approve Deployment"
  message={intent && service && environment 
    ? `Approve deployment of ${service.name} to ${environment.name}?` 
    : 'Approve this deployment intent?'}
  confirmLabel="Approve"
  variant="default"
  loading={approving}
  on:confirm={handleApprove}
  on:cancel={() => { approveOpen = false; actionError = null; }}
  on:close={() => { approveOpen = false; actionError = null; }}
>
  {#if actionError}
    <p class="dialog-error">{actionError}</p>
  {/if}
</ConfirmDialog>

<!-- Reject confirmation dialog -->
<ConfirmDialog
  bind:open={rejectOpen}
  title="Reject Deployment"
  message={intent && service && environment 
    ? `Reject deployment of ${service.name} to ${environment.name}?` 
    : 'Reject this deployment intent?'}
  confirmLabel="Reject"
  variant="danger"
  loading={rejecting}
  on:confirm={handleReject}
  on:cancel={() => { rejectOpen = false; actionError = null; }}
  on:close={() => { rejectOpen = false; actionError = null; }}
>
  {#if actionError}
    <p class="dialog-error">{actionError}</p>
  {/if}
</ConfirmDialog>

<style>
  .page {
    padding: 0;
  }
  .header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .back-link {
    display: inline-block;
    color: var(--primary);
    text-decoration: none;
    font-size: 0.875rem;
    margin-bottom: 0.5rem;
  }
  .back-link:hover {
    text-decoration: underline;
  }
  .intent-id {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }
  .actions {
    display: flex;
    gap: 0.75rem;
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
    margin: 0 0 1rem;
  }
  .cards-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
    gap: 1rem;
    margin-bottom: 1.5rem;
  }
  .detail-section h3 {
    font-size: 0.75rem;
    text-transform: uppercase;
    color: var(--text-muted);
    margin: 0 0 0.5rem;
    font-weight: 600;
  }
  .detail-section .detail-value {
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }
  .detail-section .detail-subtitle {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin: 0.25rem 0 0;
  }
  .details-card {
    margin-bottom: 1.5rem;
  }
  .details-card h2 {
    margin: 0 0 1rem;
    font-size: 1.125rem;
    color: var(--text-primary);
  }
  .details-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
    gap: 1rem;
  }
  .detail-item {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .detail-label {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    font-weight: 600;
  }
  .detail-item .detail-value {
    font-size: 0.875rem;
    color: var(--text-primary);
  }
  .runs-section {
    margin-top: 2rem;
  }
  .runs-section h2 {
    margin: 0 0 1rem;
    font-size: 1.125rem;
    color: var(--text-primary);
  }
  .dialog-error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0.5rem 0 0;
    padding: 0.5rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
  }
</style>
