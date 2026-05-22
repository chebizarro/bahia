<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Select from '$lib/components/Select.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import {
    environments,
    states as allStates,
    services,
    workers,
    deploymentIntents,
    loadEnvironments,
    loadStates,
    loadDeploymentIntents,
    loadWorkers
  } from '$lib/stores';
  import { updateEnvironment, deleteEnvironment } from '$lib/stores/public-controlplane.svelte.js';
  import { environmentFormSchema, parseRuntimeConfig, validateForm } from '$lib/validation/forms.js';
  import {
    ArtifactIcon,
    DeploymentIcon,
    EnvironmentIcon,
    ProtectedIcon,
    ServiceIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';

  let environment = $state(null);
  let states = $state([]);
  let deploymentHistory = $state([]);
  let loading = $state(true);
  let error = $state(null);

  // Service detail dialog
  let selectedService = $state(null);
  let serviceDialogOpen = $state(false);

  function openServiceDialog(service) {
    selectedService = service;
    serviceDialogOpen = true;
  }
  function closeServiceDialog() {
    serviceDialogOpen = false;
    selectedService = null;
  }

  // Lookup maps derived from stores
  let serviceById = $derived(Object.fromEntries(services.map(s => [s.id, s])));
  let workerOptions = $derived([
    { value: '', label: 'Any worker' },
    ...workers.map(w => ({ value: w.pubkey, label: w.name || w.pubkey?.slice(0, 16) + '...' }))
  ]);

  let environmentId = $derived(page.params.id);

  // Edit modal state
  let editOpen = $state(false);
  let editing = $state(false);
  let editError = $state(null);
  let editForm = $state({
    name: '',
    loom_worker_selector: '',
    runtime_config: '{}',
    deploy_strategy: 'replace',
    protected: false
  });

  // Delete modal state
  let deleteOpen = $state(false);
  let deleting = $state(false);
  let deleteError = $state(null);

  const deployStrategyOptions = [
    { value: 'replace', label: 'Replace' },
    { value: 'blue_green', label: 'Blue/Green' },
    { value: 'canary', label: 'Canary' }
  ];

  $effect(() => {
    const id = environmentId;
    if (!id) return;
    void loadEnvironment(id);
  });

  async function loadEnvironment(id) {
    loading = true;
    error = null;
    environment = null;
    states = [];
    deploymentHistory = [];

    try {
      await Promise.all([loadEnvironments(), loadStates(), loadDeploymentIntents(), loadWorkers()]);
      environment = environments.find((candidate) => candidate.id === id) || null;
      if (!environment) {
        throw new Error('Environment not found');
      }

      states = allStates.filter(state => state.environment_id === id);
      deploymentHistory = deploymentIntents
        .filter((intent) => intent.environment_id === id)
        .sort((a, b) => {
          const dateA = a.created_at ? new Date(a.created_at) : new Date(0);
          const dateB = b.created_at ? new Date(b.created_at) : new Date(0);
          return dateB - dateA;
        });
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  function getIntentStatus(intent) {
    const approvalStatus = String(intent.approval_status || '').toLowerCase();
    const deploymentStatus = String(intent.deployment_status || '').toLowerCase();

    if (approvalStatus === 'rejected') return 'rejected';
    if (approvalStatus === 'pending') return 'pending';
    if (deploymentStatus) return deploymentStatus;
    if (approvalStatus === 'approved') return 'approved';
    return 'pending';
  }

  let driftedStates = $derived(states.filter((state) => String(state.drift_status || '').toLowerCase() === 'drifted'));
  let inSyncStates = $derived(states.filter((state) => String(state.drift_status || '').toLowerCase() === 'in_sync'));
  let environmentDriftStatus = $derived(
    states.length === 0 ? 'unknown' : driftedStates.length > 0 ? 'drifted' : 'in_sync'
  );
  let driftStatusIcon = $derived(
    environmentDriftStatus === 'drifted' ? WarningIcon : environmentDriftStatus === 'in_sync' ? SuccessIcon : UnknownIcon
  );

  function serviceDisplayName(serviceId) {
    const svc = serviceById[serviceId];
    return svc?.name || svc?.display_name || (serviceId ? `${String(serviceId).slice(0, 12)}...` : '-');
  }

  function truncateId(value) {
    return value ? `${String(value).slice(0, 12)}...` : '-';
  }

  let stateColumns = $derived([
    {
      key: 'service_name',
      label: 'Name',
      icon: ServiceIcon,
      text: (r) => serviceDisplayName(r.service_id)
    },
    { key: 'artifact_id', label: 'Artifact', icon: ArtifactIcon, text: (r) => truncateId(r.artifact_id) },
    { key: 'status', label: 'Status' },
    { key: 'drift_status', label: 'Drift' },
    { key: 'deployed_at', label: 'Deployed', render: (r) => r.deployed_at ? new Date(r.deployed_at).toLocaleString() : '-' }
  ]);

  let historyColumns = $derived([
    { key: 'service_id', label: 'Service', icon: ServiceIcon, text: (r) => serviceDisplayName(r.service_id) },
    { key: 'artifact_id', label: 'Artifact', icon: ArtifactIcon, text: (r) => truncateId(r.artifact_id) },
    { key: 'intent_status', label: 'Status', render: (r) => getIntentStatus(r) },
    { key: 'created_at', label: 'Requested', render: (r) => r.created_at ? new Date(r.created_at).toLocaleString() : '-' }
  ]);

  function openEditModal() {
    if (!environment) return;
    
    // Format runtime_config as pretty JSON
    let runtimeConfigStr = '{}';
    if (environment.runtime_config && typeof environment.runtime_config === 'object') {
      try {
        runtimeConfigStr = JSON.stringify(environment.runtime_config, null, 2);
      } catch (err) {
        runtimeConfigStr = '{}';
      }
    }
    
    editForm = {
      name: environment.name,
      loom_worker_selector: environment.loom_worker_selector || '',
      runtime_config: runtimeConfigStr,
      deploy_strategy: environment.deploy_strategy || 'replace',
      protected: environment.protected || false
    };
    editError = null;
    editOpen = true;
  }

  function closeEditModal() {
    editOpen = false;
    editError = null;
  }

  async function handleEdit() {
    const validationResult = validateForm(environmentFormSchema, editForm);
    if (!validationResult.success) {
      editError = validationResult.error;
      return;
    }

    const parsedRuntimeConfig = parseRuntimeConfig(editForm.runtime_config);

    editing = true;
    editError = null;

    try {
      await updateEnvironment(environmentId, {
        name: editForm.name.trim(),
        loom_worker_selector: editForm.loom_worker_selector.trim(),
        runtime_config: parsedRuntimeConfig,
        deploy_strategy: editForm.deploy_strategy,
        protected: editForm.protected
      });
      environment = {
        ...environment,
        name: editForm.name.trim(),
        loom_worker_selector: editForm.loom_worker_selector.trim(),
        runtime_config: parsedRuntimeConfig,
        deploy_strategy: editForm.deploy_strategy,
        protected: editForm.protected
      };
      closeEditModal();
    } catch (err) {
      editError = err.message || 'Failed to update environment';
    } finally {
      editing = false;
    }
  }

  function openDeleteModal() {
    deleteError = null;
    deleteOpen = true;
  }

  function closeDeleteModal() {
    deleteOpen = false;
    deleteError = null;
  }

  async function handleDelete() {
    deleting = true;
    deleteError = null;

    try {
      await deleteEnvironment(environmentId);
      goto('/environments');
    } catch (err) {
      deleteError = err.message || 'Failed to delete environment';
      deleting = false;
      // Keep modal open to allow user to see error
    }
  }
</script>

<div class="page">
  <a href="/environments" class="back">← Environments</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <p class="error">Error: {error}</p>
  {:else if environment}
    <div class="header">
      <h1 class="title-with-icon"><EnvironmentIcon size={28} strokeWidth={1.75} ariaHidden="true" /> <span>{environment.name}</span></h1>
      <div class="actions">
        <LoadingButton variant="secondary" onclick={openEditModal}>
          Edit
        </LoadingButton>
        <LoadingButton variant="danger" onclick={openDeleteModal}>
          Delete
        </LoadingButton>
      </div>
    </div>
    
    <div class="info-grid">
      <Card title="Deploy Strategy" titleIcon={DeploymentIcon} value={environment.deploy_strategy || 'replace'} />
      <Card title="Protected" titleIcon={environment.protected ? ProtectedIcon : UnknownIcon} value={environment.protected ? 'Yes' : 'No'} />
      <Card title="Worker Selector" titleIcon={ServiceIcon} value={environment.loom_worker_selector || 'Any worker'} />
      <Card title="Current State" titleIcon={driftStatusIcon} value={environmentDriftStatus === 'drifted' ? 'Drifted' : environmentDriftStatus === 'in_sync' ? 'In Sync' : 'Unknown'} />
      <Card title="Drifted Services" titleIcon={WarningIcon} value={String(driftedStates.length)} />
      <Card title="In-Sync Services" titleIcon={SuccessIcon} value={String(inSyncStates.length)} />
      <Card title="ID" titleIcon={EnvironmentIcon} value={environment.id ? `${environment.id.slice(0, 16)}...` : '-'} />
    </div>

    {#if environment.runtime_config && Object.keys(environment.runtime_config).length > 0}
      <section>
        <h2 class="section-title"><EnvironmentIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>Runtime Configuration</span></h2>
        <pre class="config-json">{JSON.stringify(environment.runtime_config, null, 2)}</pre>
      </section>
    {/if}

    <section>
      <h2 class="section-title"><ServiceIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>Deployed Services ({states.length})</span></h2>
      {#if states.length > 0}
        <Table
          columns={stateColumns}
          data={states}
          onRowClick={(row) => {
            const svc = serviceById[row.service_id];
            if (svc) openServiceDialog(svc);
          }}
        />
      {:else}
        <EmptyState
          iconComponent={ServiceIcon}
          title="No services deployed"
          message="No services are currently deployed to this environment"
        />
      {/if}
    </section>

    <section>
      <h2 class="section-title"><DeploymentIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>Deployment History ({deploymentHistory.length})</span></h2>
      {#if deploymentHistory.length > 0}
        <Table columns={historyColumns} data={deploymentHistory} />
      {:else}
        <EmptyState
          iconComponent={DeploymentIcon}
          title="No deployment history"
          message="No deployment intents have been recorded for this environment yet"
        />
      {/if}
    </section>
  {/if}
</div>

<!-- Service Detail Dialog -->
<Modal bind:open={serviceDialogOpen} title="Service Detail" titleIcon={ServiceIcon} onClose={closeServiceDialog}>
  {#if selectedService}
    <div class="svc-detail">
      <div class="svc-row"><span class="svc-label">Name</span><span>{selectedService.name || selectedService.display_name || '-'}</span></div>
      <div class="svc-row"><span class="svc-label">ID</span><code class="svc-id">{selectedService.id}</code></div>
      {#if selectedService.owner_org_id}<div class="svc-row"><span class="svc-label">Org</span><code>{selectedService.owner_org_id}</code></div>{/if}
      {#if selectedService.description}<div class="svc-row"><span class="svc-label">Description</span><span>{selectedService.description}</span></div>{/if}
      {#if selectedService.status}<div class="svc-row"><span class="svc-label">Status</span><span>{selectedService.status}</span></div>{/if}
      {#if selectedService.image}<div class="svc-row"><span class="svc-label">Image</span><code>{selectedService.image}</code></div>{/if}
      <div class="svc-actions">
        <a href="/services/{selectedService.id}" class="svc-link" onclick={closeServiceDialog}>Open Service Page →</a>
      </div>
    </div>
  {/if}
</Modal>

<!-- Edit Modal -->
<Modal bind:open={editOpen} title="Edit Environment" titleIcon={EnvironmentIcon} onClose={closeEditModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleEdit(); }} class="edit-form">
    <div class="form-field">
      <label for="edit-name">Name *</label>
      <Input
        id="edit-name"
        bind:value={editForm.name}
        placeholder="production"
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-worker-selector">Worker Selector</label>
      {#if workerOptions.length > 1}
        <Select
          id="edit-worker-selector"
          bind:value={editForm.loom_worker_selector}
          options={workerOptions}
          disabled={editing}
        />
      {:else}
        <Input
          id="edit-worker-selector"
          bind:value={editForm.loom_worker_selector}
          placeholder="Worker pubkey or selector"
          disabled={editing}
        />
      {/if}
    </div>

    <div class="form-field">
      <label for="edit-runtime-config">Runtime Config (JSON object)</label>
      <Textarea
        id="edit-runtime-config"
        bind:value={editForm.runtime_config}
        placeholder={'{}'}
        rows={10}
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-deploy-strategy">Deploy Strategy *</label>
      <Select
        id="edit-deploy-strategy"
        bind:value={editForm.deploy_strategy}
        options={deployStrategyOptions}
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <Checkbox
        id="edit-protected"
        bind:checked={editForm.protected}
        disabled={editing}
        label="Protected (requires approval for deployments)"
      />
    </div>

    {#if editError}
      <p class="error">{editError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        onclick={closeEditModal}
        disabled={editing}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={editing}
      >
        Save Environment
      </LoadingButton>
    </div>
  </form>
</Modal>

<!-- Delete Confirmation Dialog -->
<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete Environment"
  titleIcon={WarningIcon}
  confirmLabel="Delete"
  variant="danger"
  loading={deleting}
  onConfirm={handleDelete}
  onCancel={closeDeleteModal}
  onClose={closeDeleteModal}
>
  <div class="delete-content">
    <p>Are you sure you want to delete <strong>{environment?.name}</strong>?</p>
    <p class="warning">This action cannot be undone. All deployments to this environment will be affected.</p>

    {#if deleteError}
      <p class="error">{deleteError}</p>
    {/if}
  </div>
</ConfirmDialog>

<style>
  .page { max-width: 1000px; }
  .back {
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.875rem;
    display: inline-block;
    margin-bottom: 1rem;
  }
  .back:hover { color: var(--text-primary); }
  
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .header h1 {
    margin: 0;
  }
  .title-with-icon,
  .section-title {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .actions {
    display: flex;
    gap: 0.75rem;
  }

  .info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    margin-bottom: 1.5rem;
    border: 1px solid var(--border-color);
  }
  section h2 {
    font-size: 1rem;
    color: var(--text-muted);
    margin: 0 0 1rem 0;
  }

  .config-json {
    background: var(--bg);
    padding: 1rem;
    border-radius: 6px;
    font-size: 0.8rem;
    overflow-x: auto;
    color: var(--text-primary);
    border: 1px solid var(--border-color);
  }

  .loading, .error {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }
  .error { color: var(--error); }

  :global(.svc-name-link) {
    color: var(--primary);
    cursor: pointer;
    text-decoration: underline;
    text-decoration-style: dotted;
  }
  :global(.svc-name-link:hover) {
    text-decoration-style: solid;
  }

  .svc-detail {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .svc-row {
    display: flex;
    gap: 1rem;
    align-items: flex-start;
    font-size: 0.875rem;
  }
  .svc-label {
    min-width: 90px;
    color: var(--text-muted);
    font-weight: 500;
  }
  .svc-id {
    font-size: 0.75rem;
    word-break: break-all;
  }
  .svc-actions {
    margin-top: 0.5rem;
  }
  .svc-link {
    color: var(--primary);
    font-size: 0.875rem;
  }

  .edit-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .form-field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .form-field label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }
  .form-actions {
    display: flex;
    gap: 0.75rem;
    justify-content: flex-end;
    margin-top: 0.5rem;
  }

  .delete-content {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .delete-content p {
    margin: 0;
    line-height: 1.5;
  }
  .warning {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
</style>
