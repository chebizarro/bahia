<script>
  import IntentGate from '$lib/components/IntentGate.svelte';
  import { untrack } from 'svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import { systemInfo, loadSystemInfo } from '$lib/stores';
  import { orgsState } from '$lib/stores/orgs.svelte.js';
  import { orgRoles } from '$lib/stores/auth-roles.svelte.js';
  import { createManualRepositorySelection } from '$lib/stores/repositories.js';
  import { fetchRepoBranches, isNostrRepository } from '$lib/nostr/branches.js';
  import { createService as createServiceCommand } from '$lib/stores/public-controlplane.svelte.js';
  import { toast } from '$lib/components/toast.js';
  import { isEntityIdConflict, mintEntityId } from '$lib/entity-id.js';
  import { buildArtifactRepo, validateCreateServiceForm, buildCreateServicePayload } from './create-service-form.js';

  // Reusable create-service dialog shared by the services page and the dashboard.
  // `open` is bindable so either host can toggle the dialog in place. `onCreated`
  // is an optional callback invoked with the created service after a successful
  // create (the store projection is always updated regardless).
  let { open = $bindable(false), onCreated = null } = $props();

  // Registry state
  let availableRegistries = $state([]);
  let registriesLoading = $state(true);
  let selectedRegistry = $state('custom');
  let repoPath = $state('');
  let registriesInitialized = $state(false);

  // Client-minted entity id for this create attempt (bahia-irsry.35). It is kept
  // across retries of the same form so a retry after a lost reply is idempotent,
  // and re-minted only when the form is reset.
  let createEntityId = mintEntityId();

  // Create form state
  let creating = $state(false);
  let createError = $state(null);
  let createForm = $state({
    org_id: '',
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

  const runtimeOptions = [
    { value: 'docker', label: 'Docker' },
    { value: 'compose', label: 'Docker Compose' },
    { value: 'kubernetes', label: 'Kubernetes' },
    { value: 'podman', label: 'Podman' }
  ];

  // Load available registries once on mount so the dialog works standalone from
  // any host page (dashboard or services list).
  $effect(() => {
    if (registriesInitialized) return;
    registriesInitialized = true;
    void untrack(() => loadRegistries());
  });

  async function loadRegistries() {
    try {
      const info = systemInfo.data || await loadSystemInfo();
      availableRegistries = info?.registries || [];
      const defaultReg = availableRegistries.find((r) => r.default);
      if (defaultReg) {
        selectedRegistry = defaultReg.id;
      }
    } catch (err) {
      console.warn('Failed to load registries:', err);
    } finally {
      registriesLoading = false;
    }
  }

  async function handleRepositoryChange(selection) {
    // Reset branch state
    detectedBranches = [];
    detectedDefaultBranch = null;
    branchesError = null;

    // Only fetch branches for relay-backed repositories
    if (!isNostrRepository(selection)) {
      return;
    }

    branchesLoading = true;
    try {
      const result = await fetchRepoBranches(selection.repoCoordinate, { relayUrls: selection.relayUrls });
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

  function resetForm() {
    createEntityId = mintEntityId();
    createForm = {
      org_id: '',
      name: '',
      repositorySelection: createManualRepositorySelection(''),
      artifact_repo: '',
      runtime_type: 'docker',
      default_branch: 'main'
    };
    repoPath = '';
    const defaultReg = availableRegistries.find((r) => r.default);
    selectedRegistry = defaultReg?.id || 'custom';
    detectedBranches = [];
    detectedDefaultBranch = null;
    branchesError = null;
  }

  function closeDialog() {
    open = false;
    createError = null;
    resetForm();
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
      const payload = { ...buildCreateServicePayload(createForm, { id: createEntityId }), org_id: createForm.org_id.trim() };
      await createServiceCommand(payload);
      toast.success(`Service "${payload.name}" submitted`);
      onCreated?.(null, payload);
      closeDialog();
    } catch (err) {
      const message = err?.message || 'Failed to create service';
      createError = /method not found/i.test(message)
        ? 'Service creation is not available from this Bahia service yet (missing service/create handler on the backend).'
        : isEntityIdConflict(err)
          ? 'This service was already created with different settings. Close the dialog and start again to create another service.'
          : message;
      toast.error(createError);
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
    ...availableRegistries.map((r) => ({
      value: r.id,
      label: r.default ? `${r.name} (default)` : r.name
    })),
    { value: 'custom', label: 'Custom Registry' }
  ]);

  let organizationOptions = $derived([...new Set([
    ...orgsState.orgs.map(org => org.id || org.org_id), ...Object.keys(orgRoles)
  ].filter(Boolean))].map(id => ({ value: id, label: orgsState.orgs.find(org => (org.id || org.org_id) === id)?.name || id })));

  $effect(() => {
    if (!createForm.org_id && organizationOptions.length === 1) createForm.org_id = organizationOptions[0].value;
  });

  // Watch for repository selection changes and fetch branches
  $effect(() => {
    if (createForm.repositorySelection) {
      handleRepositoryChange(createForm.repositorySelection);
    }
  });

  let branchOptions = $derived(detectedBranches.map((b) => ({
    value: b,
    label: b === detectedDefaultBranch ? `${b} (default)` : b
  })));
</script>

<Modal bind:open title="Create Service" onClose={closeDialog}>
  <form onsubmit={(event) => { event.preventDefault(); handleCreate(); }} class="create-form">
    <div class="form-field">
      <label for="service-org-id">Organization ID *</label>
      {#if organizationOptions.length}
        <Select id="service-org-id" bind:value={createForm.org_id} options={organizationOptions} required disabled={creating} />
      {:else}
        <Input id="service-org-id" bind:value={createForm.org_id} placeholder="Organization UUID" required disabled={creating} />
      {/if}
    </div>
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
        onclick={closeDialog}
        disabled={creating}
      >
        Cancel
      </LoadingButton>
      <IntentGate domain="service" orgId={createForm.org_id} orgField>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={creating}
      >
        Create
      </LoadingButton>
      </IntentGate>
    </div>
  </form>
</Modal>

<style>
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
