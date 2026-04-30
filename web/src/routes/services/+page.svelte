<script>
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import { services, loading, loadServices } from '$lib/stores';
  import { createManualRepositorySelection } from '$lib/stores/repositories.js';
  import { api } from '$lib/api/client.js';

  onMount(() => loadServices());

  // Create modal state
  let createOpen = false;
  let creating = false;
  let createError = null;

  let createForm = {
    name: '',
    repositorySelection: createManualRepositorySelection(''),
    artifact_repo: '',
    runtime_type: 'docker',
    default_branch: 'main'
  };

  const runtimeOptions = [
    { value: 'docker', label: 'Docker' },
    { value: 'compose', label: 'Docker Compose' },
    { value: 'kubernetes', label: 'Kubernetes' },
    { value: 'podman', label: 'Podman' }
  ];

  $: columns = [
    { key: 'name', label: 'Name' },
    { key: 'artifact_repo', label: 'Artifact Repo' },
    { key: 'runtime_type', label: 'Runtime' },
    { key: 'default_branch', label: 'Branch' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
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
  }

  async function handleCreate() {
    // Validate required fields
    if (!createForm.name.trim()) {
      createError = 'Name is required';
      return;
    }
    if (!createForm.artifact_repo.trim()) {
      createError = 'Artifact repository is required';
      return;
    }
    if (!createForm.runtime_type) {
      createError = 'Runtime type is required';
      return;
    }

    creating = true;
    createError = null;

    try {
      await api.createService({
        name: createForm.name.trim(),
        repo_url: createForm.repositorySelection?.repoUrl || '',
        artifact_repo: createForm.artifact_repo.trim(),
        runtime_type: createForm.runtime_type,
        default_branch: createForm.default_branch.trim() || 'main'
      });
      
      closeCreateModal();
      await loadServices();
    } catch (err) {
      createError = err.message || 'Failed to create service';
    } finally {
      creating = false;
    }
  }
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Services</h1>
      <span class="count">{$services.length} services</span>
    </div>
    <LoadingButton variant="primary" on:click={openCreateModal}>
      Create Service
    </LoadingButton>
  </div>

  {#if $loading.services}
    <p class="loading">Loading...</p>
  {:else if $services.length === 0}
    <EmptyState
      icon="📦"
      title="No services yet"
      message="Create your first service to get started with deployments"
      actionLabel="Create Service"
      on:click={openCreateModal}
    />
  {:else}
    <Table {columns} data={$services} onRowClick={(row) => goto(`/services/${row.id}`)} />
  {/if}
</div>

<Modal bind:open={createOpen} title="Create Service" on:close={closeCreateModal}>
  <form on:submit|preventDefault={handleCreate} class="create-form">
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
      <label for="artifact-repo">Artifact Repository *</label>
      <Input
        id="artifact-repo"
        bind:value={createForm.artifact_repo}
        placeholder="ghcr.io/org/my-service"
        required
        disabled={creating}
      />
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
      <Input
        id="default-branch"
        bind:value={createForm.default_branch}
        placeholder="main"
        disabled={creating}
      />
    </div>

    {#if createError}
      <p class="error">{createError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        on:click={closeCreateModal}
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
