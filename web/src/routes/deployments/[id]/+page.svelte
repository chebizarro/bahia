<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import {
    deploymentIntents,
    deploymentRuns,
    services,
    environments,
    loadDeploymentIntents,
    loadDeploymentRuns,
    loadServices,
    loadEnvironments
  } from '$lib/stores';
  import { approveDeploymentIntent, rejectDeploymentIntent } from '$lib/stores/public-controlplane.svelte.js';
  import {
    ArtifactIcon,
    DeploymentIcon,
    EnvironmentIcon,
    ErrorIcon,
    PendingIcon,
    ProgressIcon,
    ServiceIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';
  import { shortenPubkey } from '$lib/nostr/nostr-hex.js';

  let intent = $state(null);
  let runs = $state([]);
  let service = $state(null);
  let environment = $state(null);
  let loading = $state(true);
  let error = $state(null);
  let intentId = $derived(page.params.id);

  // Action state
  let approving = $state(false);
  let rejecting = $state(false);
  let actionError = $state(null);
  let approveOpen = $state(false);
  let rejectOpen = $state(false);

  // Reactive: check if intent is pending approval
  let isPending = $derived(intent && String(intent.approval_status || '').toLowerCase() === 'pending');

  // Columns for runs table
  let runsColumns = $derived([
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
  ]);

  $effect(() => {
    const id = intentId;
    if (!id) return;
    void loadIntent(id);
  });

  async function loadIntent(id = intentId) {
    loading = true;
    error = null;

    try {
      await Promise.all([loadDeploymentIntents(), loadDeploymentRuns(), loadServices(), loadEnvironments()]);
      intent = deploymentIntents.find((candidate) => candidate.id === id) || null;
      if (!intent) {
        throw new Error('Deployment intent not found');
      }

      runs = deploymentRuns.filter((run) => run.deployment_intent_id === id || run.intent_id === id);
      service = intent.service_id ? services.find((candidate) => candidate.id === intent.service_id) || null : null;
      environment = intent.environment_id ? environments.find((candidate) => candidate.id === intent.environment_id) || null : null;
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
      await approveDeploymentIntent(intent.id);
      await loadIntent();
      intent = { ...intent, approval_status: 'approved' };
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
      await rejectDeploymentIntent(intent.id);
      await loadIntent();
      intent = { ...intent, approval_status: 'rejected' };
      rejectOpen = false;
    } catch (err) {
      actionError = err.message || 'Failed to reject intent';
      console.error('Error rejecting intent:', err);
    } finally {
      rejecting = false;
    }
  }

  function approvalStatusCard(status) {
    const normalized = String(status || '').toLowerCase();
    if (normalized === 'approved') return 'success';
    if (normalized === 'rejected') return 'error';
    if (normalized === 'pending') return 'warning';
    return 'default';
  }

  function deploymentStatusCard(status) {
    const normalized = String(status || '').toLowerCase();
    if (normalized === 'succeeded') return 'success';
    if (normalized === 'failed') return 'error';
    if (normalized === 'cancelled') return 'warning';
    return 'default';
  }

  function approvalStatusIcon(status) {
    const normalized = String(status || '').toLowerCase();
    if (normalized === 'approved') return SuccessIcon;
    if (normalized === 'rejected') return ErrorIcon;
    if (normalized === 'pending') return PendingIcon;
    return UnknownIcon;
  }

  function deploymentStatusIcon(status) {
    const normalized = String(status || '').toLowerCase();
    if (normalized === 'succeeded') return SuccessIcon;
    if (normalized === 'failed') return ErrorIcon;
    if (normalized === 'cancelled' || normalized === 'timeout') return WarningIcon;
    if (normalized === 'running') return ProgressIcon;
    if (normalized === 'queued' || normalized === 'pending') return PendingIcon;
    // Never fall back to the question-mark icon for the deployment status hero.
    return DeploymentIcon;
  }
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading deployment intent...</p>
  {:else if error}
    <div class="error-state">
      <p class="error title-with-icon"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{error}</span></p>
      <LoadingButton variant="secondary" onclick={() => goto('/deployments')}>
        Back to Deployments
      </LoadingButton>
    </div>
  {:else if intent}
    <a href="/deployments" class="back-link">← Back to Deployments</a>
    <div class="header">
      <div>
        <h1 class="title-with-icon"><DeploymentIcon size={28} strokeWidth={1.75} ariaHidden="true" /> <span>Deployment Intent</span></h1>
        <p class="intent-id"><code>{intent.id}</code></p>
      </div>
      {#if isPending}
        <div class="actions">
          <LoadingButton 
            variant="primary" 
            onclick={() => { approveOpen = true; actionError = null; }}
          >
            Approve
          </LoadingButton>
          <LoadingButton 
            variant="danger" 
            onclick={() => { rejectOpen = true; actionError = null; }}
          >
            Reject
          </LoadingButton>
        </div>
      {/if}
    </div>

    <div class="cards-grid">
      <Card title="Service" titleIcon={ServiceIcon} value={service?.name || intent.service_id || 'Unknown'}>
        {#if intent.service_id && !service}
          <p class="detail-subtitle"><code>{intent.service_id}</code></p>
        {/if}
      </Card>

      <Card title="Environment" titleIcon={EnvironmentIcon} value={environment?.name || intent.environment_id || 'Unknown'}>
        {#if intent.environment_id && !environment}
          <p class="detail-subtitle"><code>{intent.environment_id}</code></p>
        {/if}
      </Card>

      <Card title="Approval Status" titleIcon={approvalStatusIcon(intent.approval_status)} status={approvalStatusCard(intent.approval_status)} value={intent.approval_status || 'Unknown'} />

      <Card title="Deployment Status" titleIcon={deploymentStatusIcon(intent.deployment_status)} status={deploymentStatusCard(intent.deployment_status)} value={intent.deployment_status || 'Unknown'} />
    </div>

    <div class="details-card">
      <Card title="Intent Details" titleIcon={DeploymentIcon}>
        <div class="details-card-body">
        <div class="details-grid">
          <div class="detail-item">
            <span class="detail-label">Artifact ID</span>
            <span class="detail-value cross-ref-value">
              <ArtifactIcon size={16} strokeWidth={1.75} ariaHidden="true" />
              {#if intent.artifact_id}
                <code>{intent.artifact_id}</code>
              {:else}
                <span>-</span>
              {/if}
            </span>
          </div>
          <div class="detail-item">
            <span class="detail-label">Requested By</span>
            <span class="detail-value" title={intent.requested_by || ''}>{intent.requested_by ? shortenPubkey(intent.requested_by) : '-'}</span>
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
        </div>
      </Card>
    </div>

    <div class="runs-section">
      <h2 class="section-title"><DeploymentIcon size={20} strokeWidth={1.75} ariaHidden="true" /> <span>Deployment Runs ({runs.length})</span></h2>
      {#if runs.length === 0}
        <EmptyState
          iconComponent={DeploymentIcon}
          title="No runs yet"
          message="Deployment runs will appear here once the intent is approved and executed"
        />
      {:else}
        <Table columns={runsColumns} data={runs} onRowClick={(row) => goto(`/deployments/runs/${row.id}`)} />
      {/if}
    </div>
  {:else}
    <EmptyState
      iconComponent={UnknownIcon}
      title="Intent not found"
      message="The requested deployment intent does not exist"
    >
      <LoadingButton variant="secondary" onAction={() => goto('/deployments')}>
        Back to Deployments
      </LoadingButton>
    </EmptyState>
  {/if}
</div>

<!-- Approve confirmation dialog -->
<ConfirmDialog
  bind:open={approveOpen}
  title="Approve Deployment"
  titleIcon={SuccessIcon}
  message={intent && service && environment 
    ? `Approve deployment of ${service.name} to ${environment.name}?` 
    : 'Approve this deployment intent?'}
  confirmLabel="Approve"
  variant="default"
  loading={approving}
  onConfirm={handleApprove}
  onCancel={() => { approveOpen = false; actionError = null; }}
  onClose={() => { approveOpen = false; actionError = null; }}
>
  {#if actionError}
    <p class="dialog-error">{actionError}</p>
  {/if}
</ConfirmDialog>

<!-- Reject confirmation dialog -->
<ConfirmDialog
  bind:open={rejectOpen}
  title="Reject Deployment"
  titleIcon={WarningIcon}
  message={intent && service && environment 
    ? `Reject deployment of ${service.name} to ${environment.name}?` 
    : 'Reject this deployment intent?'}
  confirmLabel="Reject"
  variant="danger"
  loading={rejecting}
  onConfirm={handleReject}
  onCancel={() => { rejectOpen = false; actionError = null; }}
  onClose={() => { rejectOpen = false; actionError = null; }}
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
  .title-with-icon,
  .section-title,
  .cross-ref-value {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
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
  .details-card {
    margin-bottom: 1.5rem;
  }
  .details-card-body {
    margin-top: 1rem;
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
