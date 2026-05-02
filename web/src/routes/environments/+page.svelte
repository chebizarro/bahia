<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Select from '$lib/components/Select.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { environments, loading, loadEnvironments } from '$lib/stores';
  import { api } from '$lib/api/client.js';

  $effect(() => {
    void loadEnvironments();
  });

  // Create modal state
  let createOpen = $state(false);
  let creating = $state(false);
  let createError = $state(null);

  let createForm = $state({
    name: '',
    loom_worker_selector: '',
    runtime_config: '{}',
    deploy_strategy: 'rolling',
    protected: false
  });

  const deployStrategyOptions = [
    { value: 'rolling', label: 'Rolling' },
    { value: 'blue-green', label: 'Blue-Green' },
    { value: 'canary', label: 'Canary' }
  ];

  const deployStrategyApiMap = {
    rolling: 'replace',
    'blue-green': 'blue_green',
    canary: 'canary'
  };

  let columns = $derived([
    { key: 'name', label: 'Name' },
    { key: 'deploy_strategy', label: 'Strategy' },
    { key: 'protected', label: 'Protected', render: (r) => r.protected ? '🔒' : '-' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ]);

  function openCreateModal() {
    createOpen = true;
    createError = null;
  }

  function closeCreateModal() {
    createOpen = false;
    createError = null;
    // Reset form
    createForm = {
      name: '',
      loom_worker_selector: '',
      runtime_config: '{}',
      deploy_strategy: 'rolling',
      protected: false
    };
  }

  async function handleCreate() {
    // Validate required fields
    if (!createForm.name.trim()) {
      createError = 'Name is required';
      return;
    }
    if (!createForm.deploy_strategy) {
      createError = 'Deploy strategy is required';
      return;
    }

    // Validate runtime_config JSON
    let parsedRuntimeConfig = {};
    if (createForm.runtime_config.trim()) {
      try {
        parsedRuntimeConfig = JSON.parse(createForm.runtime_config);
      } catch (err) {
        createError = 'Runtime config must be valid JSON';
        return;
      }
    }

    creating = true;
    createError = null;

    try {
      await api.createEnvironment({
        name: createForm.name.trim(),
        loom_worker_selector: createForm.loom_worker_selector.trim(),
        runtime_config: parsedRuntimeConfig,
        deploy_strategy: deployStrategyApiMap[createForm.deploy_strategy] || createForm.deploy_strategy,
        protected: createForm.protected
      });
      
      closeCreateModal();
      await loadEnvironments();
    } catch (err) {
      createError = err.message || 'Failed to create environment';
    } finally {
      creating = false;
    }
  }
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Environments</h1>
      <span class="count">{environments.length} environments</span>
    </div>
    <LoadingButton variant="primary" onclick={openCreateModal}>
      Create Environment
    </LoadingButton>
  </div>

  {#if loading.environments}
    <p class="loading">Loading...</p>
  {:else if environments.length === 0}
    <EmptyState
      icon="🌍"
      title="No environments yet"
      message="Create your first environment to define deployment targets"
      actionLabel="Create Environment"
      onAction={openCreateModal}
    />
  {:else}
    <Table {columns} data={environments} onRowClick={(row) => goto(`/environments/${row.id}`)} />
  {/if}
</div>

<Modal bind:open={createOpen} title="Create Environment" onClose={closeCreateModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleCreate(); }} class="create-form">
    <div class="form-field">
      <label for="env-name">Name *</label>
      <Input
        id="env-name"
        bind:value={createForm.name}
        placeholder="production"
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="worker-selector">Loom Worker Selector</label>
      <Input
        id="worker-selector"
        bind:value={createForm.loom_worker_selector}
        placeholder="region=us-west"
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="runtime-config">Runtime Config (JSON)</label>
      <Textarea
        id="runtime-config"
        bind:value={createForm.runtime_config}
        placeholder={'{}'}
        rows={6}
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="deploy-strategy">Deploy Strategy *</label>
      <Select
        id="deploy-strategy"
        bind:value={createForm.deploy_strategy}
        options={deployStrategyOptions}
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <Checkbox
        id="protected"
        bind:checked={createForm.protected}
        disabled={creating}
        label="Protected (requires approval for deployments)"
      />
    </div>

    {#if createError}
      <p class="error">{createError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        onclick={closeCreateModal}
        disabled={creating}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={creating}
      >
        Create
      </LoadingButton>
    </div>
  </form>
</Modal>

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
  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .create-form {
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
  .error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0;
    padding: 0.5rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
  }
</style>
