<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import { services, loading, loadServices, systemInfo, loadSystemInfo, upsertServiceProjection } from '$lib/stores';
  import { createManualRepositorySelection } from '$lib/stores/repositories.js';
  import { fetchRepoBranches, isNostrRepository } from '$lib/nostr/branches.js';
  import { createService as createServiceCommand, resultContent } from '$lib/stores/public-controlplane.svelte.js';
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
      const info = systemInfo.data || await loadSystemInfo();
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

  let searchQuery = $state('');
  let runtimeFilter = $state('all');
  let pageSize = $state('25');
  let currentPage = $state(1);

  const pageSizeOptions = [
    { value: '10', label: '10' },
    { value: '25', label: '25' },
    { value: '50', label: '50' }
  ];

  let runtimeFilterOptions = $derived([
    { value: 'all', label: 'All runtimes' },
    ...Array.from(new Set(services.map((service) => service.runtime_type).filter(Boolean))).map((runtimeType) => ({
      value: runtimeType,
      label: runtimeType
    }))
  ]);

  let filteredServices = $derived(services.filter((service) => {
    const matchesSearch =
      !searchQuery ||
      service.name?.toLowerCase().includes(searchQuery.trim().toLowerCase());
    const matchesRuntime = runtimeFilter === 'all' || service.runtime_type === runtimeFilter;
    return matchesSearch && matchesRuntime;
  }));

  let totalPages = $derived(Math.max(1, Math.ceil(filteredServices.length / Number(pageSize))));
  let pagedServices = $derived(filteredServices.slice((currentPage - 1) * Number(pageSize), currentPage * Number(pageSize)));

  $effect(() => {
    searchQuery;
    runtimeFilter;
    pageSize;
    currentPage = 1;
  });

  function goToNextPage() {
    currentPage = Math.min(currentPage + 1, totalPages);
  }

  function goToPreviousPage() {
    currentPage = Math.max(currentPage - 1, 1);
  }

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
      const payload = buildCreateServicePayload(createForm);
      const resultEvent = await createServiceCommand(payload);
      const result = resultContent(resultEvent);
      const serviceId = result?.service?.id || result?.service_id || result?.id;
      if (serviceId) {
        upsertServiceProjection({
          ...payload,
          ...(result?.service || {}),
          id: serviceId,
          deleted: false
        });
      }
      searchQuery = '';
      runtimeFilter = 'all';
      currentPage = 1;

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

  <div class="filters">
    <div class="filter-field">
      <label for="service-search">Search</label>
      <Input id="service-search" bind:value={searchQuery} placeholder="Search by service name" />
    </div>

    <div class="filter-field">
      <label for="runtime-filter">Runtime</label>
      <Select id="runtime-filter" bind:value={runtimeFilter} options={runtimeFilterOptions} />
    </div>

    <div class="filter-field page-size-field">
      <label for="page-size">Page size</label>
      <Select id="page-size" bind:value={pageSize} options={pageSizeOptions} />
    </div>
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
  {:else if filteredServices.length === 0}
    <EmptyState
      icon="🔍"
      title="No services match current filters"
      message="Try adjusting your search or runtime filter"
    />
  {:else}
    <Table {columns} data={pagedServices} onRowClick={(row) => goto(`/services/${row.id}`)} />

    {#if filteredServices.length > Number(pageSize)}
      <div class="pagination" aria-label="Services pagination">
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

  .page-size-field {
    max-width: 180px;
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
