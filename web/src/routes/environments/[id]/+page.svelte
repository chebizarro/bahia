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
  import { api } from '$lib/api/client.js';

  let environment = $state(null);
  let states = $state([]);
  let loading = $state(true);
  let error = $state(null);

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

    try {
      environment = await api.getEnvironment(id);
      
      // Load all states and filter by environment_id
      try {
        const allStates = await api.listStates();
        states = allStates.filter(state => state.environment_id === id);
      } catch (err) {
        // If listStates fails, still show environment details
        console.error('Failed to load states:', err);
        states = [];
      }
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  let stateColumns = $derived([
    { key: 'service_id', label: 'Service', render: (r) => `<code>${r.service_id?.slice(0, 12)}...</code>` },
    { key: 'artifact_id', label: 'Artifact', render: (r) => `<code>${r.artifact_id?.slice(0, 12)}...</code>` },
    { key: 'status', label: 'Status' },
    { key: 'deployed_at', label: 'Deployed', render: (r) => r.deployed_at ? new Date(r.deployed_at).toLocaleString() : '-' }
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
    // Validate required fields
    if (!editForm.name.trim()) {
      editError = 'Name is required';
      return;
    }
    if (!editForm.deploy_strategy) {
      editError = 'Deploy strategy is required';
      return;
    }

    // Validate runtime_config JSON
    let parsedRuntimeConfig = {};
    if (editForm.runtime_config.trim()) {
      try {
        parsedRuntimeConfig = JSON.parse(editForm.runtime_config);
      } catch (err) {
        editError = 'Runtime config must be valid JSON';
        return;
      }
    }

    editing = true;
    editError = null;

    try {
      const updated = await api.updateEnvironment(environmentId, {
        name: editForm.name.trim(),
        loom_worker_selector: editForm.loom_worker_selector.trim(),
        runtime_config: parsedRuntimeConfig,
        deploy_strategy: editForm.deploy_strategy,
        protected: editForm.protected
      });
      
      // Update local environment with response
      environment = updated;
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
      await api.deleteEnvironment(environmentId);
      // Navigate back to environments list on success
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
      <h1>{environment.name}</h1>
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
      <Card title="Deploy Strategy" value={environment.deploy_strategy || 'replace'} />
      <Card title="Protected" value={environment.protected ? '🔒 Yes' : 'No'} />
      <Card title="Worker Selector" value={environment.loom_worker_selector || '-'} />
      <Card title="ID" value={environment.id?.slice(0, 16) + '...' || '-'} />
    </div>

    {#if environment.runtime_config && Object.keys(environment.runtime_config).length > 0}
      <section>
        <h2>Runtime Configuration</h2>
        <pre class="config-json">{JSON.stringify(environment.runtime_config, null, 2)}</pre>
      </section>
    {/if}

    <section>
      <h2>Deployed Services ({states.length})</h2>
      {#if states.length > 0}
        <Table columns={stateColumns} data={states} />
      {:else}
        <EmptyState
          icon="📦"
          title="No services deployed"
          message="No services are currently deployed to this environment"
        />
      {/if}
    </section>
  {/if}
</div>

<!-- Edit Modal -->
<Modal bind:open={editOpen} title="Edit Environment" onClose={closeEditModal}>
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
      <label for="edit-worker-selector">Loom Worker Selector</label>
      <Input
        id="edit-worker-selector"
        bind:value={editForm.loom_worker_selector}
        placeholder="region=us-west"
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-runtime-config">Runtime Config (JSON)</label>
      <Textarea
        id="edit-runtime-config"
        bind:value={editForm.runtime_config}
        placeholder={'{}'}
        rows={8}
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
        Save
      </LoadingButton>
    </div>
  </form>
</Modal>

<!-- Delete Confirmation Dialog -->
<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete Environment"
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
