<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { api } from '$lib/api/client.js';

  let pendingIntents = $state([]);
  let loading = $state(true);
  let error = $state(null);

  // Action state
  let actionIntent = $state(null);
  let approving = $state(false);
  let rejecting = $state(false);
  let actionError = $state(null);
  let approveOpen = $state(false);
  let rejectOpen = $state(false);

  // Columns for the pending intents table
  let columns = $derived([
    { key: 'service_name', label: 'Service' },
    { key: 'environment_name', label: 'Environment' },
    { 
      key: 'artifact_id', 
      label: 'Artifact', 
      render: (r) => r.artifact_id ? `<code>${r.artifact_id.slice(0, 12)}...</code>` : '-'
    },
    { key: 'requested_by', label: 'Requested By' },
    { 
      key: 'created_at', 
      label: 'Created',
      render: (r) => r.created_at ? new Date(r.created_at).toLocaleString() : '-'
    },
    {
      key: 'actions',
      label: 'Actions',
      render: (r) => `
        <div class="action-buttons">
          <button class="btn-approve" data-id="${r.id}">Approve</button>
          <button class="btn-reject" data-id="${r.id}">Reject</button>
        </div>
      `
    }
  ]);

  $effect(() => {
    void loadPendingIntents();
  });

  async function loadPendingIntents() {
    loading = true;
    error = null;

    try {
      // Load services and environments
      const [services, environments] = await Promise.all([
        api.listServices(),
        api.listEnvironments()
      ]);

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

      // Filter to only pending approvals and sort by created_at descending
      pendingIntents = Array.from(intentMap.values())
        .filter(intent => {
          const approvalStatus = String(intent.approval_status || '').toLowerCase();
          return approvalStatus === 'pending';
        })
        .sort((a, b) => {
          const dateA = a.created_at ? new Date(a.created_at) : new Date(0);
          const dateB = b.created_at ? new Date(b.created_at) : new Date(0);
          return dateB - dateA;
        });

    } catch (err) {
      error = err.message || 'Failed to load pending approvals';
      console.error('Error loading pending approvals:', err);
    } finally {
      loading = false;
    }
  }

  function handleTableClick(event) {
    const approveBtn = event.target.closest('.btn-approve');
    const rejectBtn = event.target.closest('.btn-reject');

    if (approveBtn) {
      const intentId = approveBtn.dataset.id;
      actionIntent = pendingIntents.find(i => i.id === intentId);
      if (actionIntent) {
        approveOpen = true;
        actionError = null;
      }
    } else if (rejectBtn) {
      const intentId = rejectBtn.dataset.id;
      actionIntent = pendingIntents.find(i => i.id === intentId);
      if (actionIntent) {
        rejectOpen = true;
        actionError = null;
      }
    }
  }

  async function handleApprove() {
    if (!actionIntent) return;

    approving = true;
    actionError = null;

    try {
      await api.approveIntent(actionIntent.id);
      
      // Remove from local list
      pendingIntents = pendingIntents.filter(i => i.id !== actionIntent.id);
      
      // Close dialog
      approveOpen = false;
      actionIntent = null;
    } catch (err) {
      actionError = err.message || 'Failed to approve intent';
      console.error('Error approving intent:', err);
    } finally {
      approving = false;
    }
  }

  async function handleReject() {
    if (!actionIntent) return;

    rejecting = true;
    actionError = null;

    try {
      await api.rejectIntent(actionIntent.id);
      
      // Remove from local list
      pendingIntents = pendingIntents.filter(i => i.id !== actionIntent.id);
      
      // Close dialog
      rejectOpen = false;
      actionIntent = null;
    } catch (err) {
      actionError = err.message || 'Failed to reject intent';
      console.error('Error rejecting intent:', err);
    } finally {
      rejecting = false;
    }
  }

  function handleRowClick(row, event) {
    // Don't navigate if clicking action buttons
    if (event.target.closest('.action-buttons')) {
      return;
    }
    goto(`/deployments/${row.id}`);
  }
</script>

<svelte:body onclick={handleTableClick} />

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Pending Approvals</h1>
      <span class="count">{pendingIntents.length} pending</span>
    </div>
  </div>

  {#if loading}
    <p class="loading">Loading pending approvals...</p>
  {:else if error}
    <div class="error-state">
      <p class="error">⚠️ {error}</p>
    </div>
  {:else if pendingIntents.length === 0}
    <EmptyState
      icon="✅"
      title="No pending approvals"
      message="All deployment intents have been reviewed"
    />
  {:else}
    <Table 
      {columns} 
      data={pendingIntents}
      onRowClick={handleRowClick}
    />
  {/if}
</div>

<!-- Approve confirmation dialog -->
<ConfirmDialog
  bind:open={approveOpen}
  title="Approve Deployment"
  message={actionIntent ? `Approve deployment of ${actionIntent.service_name} to ${actionIntent.environment_name}?` : ''}
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
  message={actionIntent ? `Reject deployment of ${actionIntent.service_name} to ${actionIntent.environment_name}?` : ''}
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
  .dialog-error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0.5rem 0 0;
    padding: 0.5rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
  }
  :global(.action-buttons) {
    display: flex;
    gap: 0.5rem;
  }
  :global(.btn-approve),
  :global(.btn-reject) {
    padding: 0.25rem 0.75rem;
    font-size: 0.75rem;
    font-weight: 500;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    transition: opacity 0.2s;
  }
  :global(.btn-approve) {
    background: #10b981;
    color: white;
  }
  :global(.btn-approve:hover) {
    opacity: 0.9;
  }
  :global(.btn-reject) {
    background: #ef4444;
    color: white;
  }
  :global(.btn-reject:hover) {
    opacity: 0.9;
  }
</style>
