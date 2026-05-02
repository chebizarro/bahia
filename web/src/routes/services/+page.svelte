<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import { services, loading, loadServices } from '$lib/stores';
  import { createManualRepositorySelection } from '$lib/stores/repositories.js';
  import { fetchRepoBranches, isNostrRepository } from '$lib/nostr/branches.js';
  import { api } from '$lib/api/client.js';
  import { buildArtifactRepo, validateCreateServiceForm, buildCreateServicePayload } from './create-service-form.js';

  // Registry state
  let availableRegistries = $state([]);
  let registriesLoading = $state(true);
  let selectedRegistry = $state('custom');
  let repoPath = $state('');

  $effect(() => {
    void initializeServicesPage();
  });

  async function initializeServicesPage() {
    loadServices();
    // Load available registries
    try {
      const info = await api.getSystemInfo();
      availableRegistries = info?.registries || [];
      // Auto-select default registry if available
      const defaultReg = availableRegistries.find(r => r.default);
      if (defaultReg) {
        selectedRegistry = defaultReg.id;
      }
    } catch (err) {
      console.warn('Failed to load registries:', err);
    } finally {
      registriesLoading = false;
    }
  }

  // Create modal state
  let createOpen = $state(false);
  let creating = $state(false);
  let createError = $state(null);

  let createForm = $state({
    name: '',
    repositorySelection: createManualRepositorySelection(''),
    artifact_repo: '',
    runtime_type: 'docker',
    default_branch: 'main'
  });



  // Branch detection state
  let detectedBranches = $state([]);
  let detectedDefaultBranch = $state(null);
  let branchesLoading = $state(false);
  let branchesError = $state(null);


  async function handleRepositoryChange(selection) {
    // Reset branch state
    detectedBranches = [];
    detectedDefaultBranch = null;
    branchesError = null;

    // Only fetch branches for NIP-34 repos
    if (!isNostrRepository(selection)) {
      return;
    }

    branchesLoading = true;
    try {
      const result = await fetchRepoBranches(selection.repoCoordinate);
      detectedBranches = result.branches;
      detectedDefaultBranch = result.defaultBranch;
      branchesError = result.error;

      // Auto-select detected default branch
      if (result.defaultBranch && !createForm.default_branch) {
        createForm.default_branch = result.defaultBranch;
      } else if (result.defaultBranch && createForm.default_branch === 'main') {
        // Update if still using default 'main'
        createForm.default_branch = result.defaultBranch;
      }
    } catch (err) {
      branchesError = err?.message || 'Failed to fetch branches';
    } finally {
      branchesLoading = false;
    }
  }


  const runtimeOptions = [
    { value: 'docker', label: 'Docker' },
    { value: 'compose', label: 'Docker Compose' },
    { value: 'kubernetes', label: 'Kubernetes' },
    { value: 'podman', label: 'Podman' }
  ];


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
      repositorySelection: createManualRepositorySelection(''),
      artifact_repo: '',
      runtime_type: 'docker',
      default_branch: 'main'
    };
    // Reset registry state
    repoPath = '';
    const defaultReg = availableRegistries.find(r => r.default);
    selectedRegistry = defaultReg?.id || 'custom';
  }

  async function handleCreate() {
    const validationError = validateCreateServiceForm({
      name: createForm.name,
      artifactRepo: createForm.artifact_repo,
      runtimeType: createForm.runtime_type
    });
    if (validationError) {
      createError = validationError;
      return;
    }

    creating = true;
    createError = null;

    try {
      await api.createService(buildCreateServicePayload(createForm));
      
      closeCreateModal();
      await loadServices();
    } catch (err) {
      createError = err.message || 'Failed to create service';
    } finally {
      creating = false;
    }
  }
  // Compute artifact_repo from registry selection + path
  $effect(() => {
    createForm.artifact_repo = buildArtifactRepo({
      selectedRegistry,
      repoPath,
      availableRegistries
    });
  });
  let registryOptions = $derived([
    ...availableRegistries.map(r => ({
      value: r.id,
      label: r.default ? `${r.name} (default)` : r.name
    })),
    { value: 'custom', label: 'Custom Registry' }
  ]);
  // Watch for repository selection changes and fetch branches
  $effect(() => {
    if (createForm.repositorySelection) {
      handleRepositoryChange(createForm.repositorySelection);
    }
  });
  let branchOptions = $derived(detectedBranches.map(b => ({
    value: b,
    label: b === detectedDefaultBranch ? `${b} (default)` : b
  })));
  let columns = $derived([
    { key: 'name', label: 'Name' },
    { key: 'artifact_repo', label: 'Artifact Repo' },
    { key: 'runtime_type', label: 'Runtime' },
    { key: 'default_branch', label: 'Branch' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ]);
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Services</h1>
      <span class="count">{services.length} services</span>
    </div>
    <LoadingButton variant="primary" onclick={openCreateModal}>
      Create Service
    </LoadingButton>
  </div>

  {#if loading.services}
    <p class="loading">Loading...</p>
  {:else if services.length === 0}
    <EmptyState
      icon="📦"
      title="No services yet"
      message="Create your first service to get started with deployments"
      actionLabel="Create Service"
      onAction={openCreateModal}
    />
  {:else}
    <Table {columns} data={services} onRowClick={(row) => goto(`/services/${row.id}`)} />
  {/if}
</div>

<Modal bind:open={createOpen} title="Create Service" onClose={closeCreateModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleCreate(); }} class="create-form">
    <div class="form-field">
      <label for="service-name">Name *</label>
      <Input
        id="service-name"
        bind:value={createForm.name}
        placeholder="my-service"
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="artifact-registry">Artifact Repository *</label>
      {#if registriesLoading}
        <div class="registry-loading">
          <span class="spinner-small"></span>
          Loading registries...
        </div>
      {:else}
        <div class="registry-picker">
          <Select
            id="artifact-registry"
            bind:value={selectedRegistry}
            options={registryOptions}
            disabled={creating}
          />
          <Input
            id="artifact-repo-path"
            bind:value={repoPath}
            placeholder={selectedRegistry === 'custom' ? 'ghcr.io/org/my-service' : 'org/my-service'}
            required
            disabled={creating}
          />
        </div>
        {#if selectedRegistry !== 'custom' && repoPath}
          <span class="registry-preview">→ {createForm.artifact_repo}</span>
        {/if}
      {/if}
    </div>

    <div class="form-field">
      <RepositoryPicker bind:value={createForm.repositorySelection} context="service" disabled={creating} />
    </div>

    <div class="form-field">
      <label for="runtime-type">Runtime Type *</label>
      <Select
        id="runtime-type"
        bind:value={createForm.runtime_type}
        options={runtimeOptions}
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="default-branch">Default Branch</label>
      {#if branchesLoading}
        <div class="branch-loading">
          <span class="spinner-small"></span>
          Detecting branches...
        </div>
      {:else if detectedBranches.length > 0}
        <Select
          id="default-branch"
          bind:value={createForm.default_branch}
          options={branchOptions}
          disabled={creating}
        />
        {#if detectedDefaultBranch}
          <span class="branch-hint">Detected from repository state</span>
        {/if}
      {:else}
        <Input
          id="default-branch"
          bind:value={createForm.default_branch}
          placeholder="main"
          disabled={creating}
        />
        {#if isNostrRepository(createForm.repositorySelection) && !branchesError}
          <span class="branch-hint">No branches detected - enter manually</span>
        {/if}
      {/if}
      {#if branchesError}
        <span class="branch-error">{branchesError}</span>
      {/if}
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

  .branch-loading {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.875rem;
    color: var(--text-muted);
    padding: 0.5rem 0;
  }

  .spinner-small {
    width: 14px;
    height: 14px;
    border: 2px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .branch-hint {
    display: block;
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-top: 0.25rem;
  }

  .branch-error {
    display: block;
    font-size: 0.75rem;
    color: var(--error);
    margin-top: 0.25rem;
  }

  .registry-loading {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.875rem;
    color: var(--text-muted);
    padding: 0.5rem 0;
  }

  .registry-picker {
    display: flex;
    gap: 0.5rem;
  }

  .registry-picker :global(select) {
    flex: 0 0 200px;
  }

  .registry-picker :global(input) {
    flex: 1;
  }

  .registry-preview {
    display: block;
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-top: 0.25rem;
    font-family: monospace;
  }
</style>
