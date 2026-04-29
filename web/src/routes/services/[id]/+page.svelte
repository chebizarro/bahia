<script>
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import { api } from '$lib/api/client.js';

  let service = null;
  let builds = [];
  let artifacts = [];
  let secrets = [];
  let loading = true;
  let error = null;

  $: serviceId = $page.params.id;

  // Edit modal state
  let editOpen = false;
  let editing = false;
  let editError = null;
  let editForm = {
    name: '',
    repo_url: '',
    artifact_repo: '',
    runtime_type: '',
    default_branch: ''
  };

  // Delete modal state
  let deleteOpen = false;
  let deleting = false;
  let deleteError = null;
  let deleteForce = false;

  const runtimeOptions = [
    { value: 'docker', label: 'Docker' },
    { value: 'compose', label: 'Docker Compose' },
    { value: 'kubernetes', label: 'Kubernetes' },
    { value: 'podman', label: 'Podman' }
  ];

  onMount(async () => {
    try {
      [service, builds, artifacts, secrets] = await Promise.all([
        api.getService(serviceId),
        api.listBuilds(serviceId).catch(() => []),
        api.listArtifacts(serviceId).catch(() => []),
        api.listSecrets(serviceId).catch(() => [])
      ]);
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  });

  $: buildColumns = [
    { key: 'git_sha', label: 'Commit', render: (r) => `<code>${r.git_sha?.slice(0, 7)}</code>` },
    { key: 'git_ref', label: 'Ref' },
    { key: 'status', label: 'Status' },
    { key: 'ci_system', label: 'CI' }
  ];

  $: artifactColumns = [
    { key: 'image_tag', label: 'Tag' },
    { key: 'image_digest', label: 'Digest', render: (r) => `<code>${r.image_digest?.slice(7, 19)}...</code>` },
    { key: 'size_bytes', label: 'Size', render: (r) => formatBytes(r.size_bytes) }
  ];

  function formatBytes(bytes) {
    if (!bytes) return '-';
    const mb = bytes / (1024 * 1024);
    return `${mb.toFixed(1)} MB`;
  }

  function openEditModal() {
    if (!service) return;
    
    editForm = {
      name: service.name,
      repo_url: service.repo_url || '',
      artifact_repo: service.artifact_repo,
      runtime_type: service.runtime_type || 'docker',
      default_branch: service.default_branch || 'main'
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
    if (!editForm.artifact_repo.trim()) {
      editError = 'Artifact repository is required';
      return;
    }
    if (!editForm.runtime_type) {
      editError = 'Runtime type is required';
      return;
    }

    editing = true;
    editError = null;

    try {
      const updated = await api.updateService(serviceId, {
        name: editForm.name.trim(),
        repo_url: editForm.repo_url.trim(),
        artifact_repo: editForm.artifact_repo.trim(),
        runtime_type: editForm.runtime_type,
        default_branch: editForm.default_branch.trim() || 'main'
      });
      
      // Update local service with response
      service = updated;
      closeEditModal();
    } catch (err) {
      editError = err.message || 'Failed to update service';
    } finally {
      editing = false;
    }
  }

  function openDeleteModal() {
    deleteError = null;
    deleteForce = false;
    deleteOpen = true;
  }

  function closeDeleteModal() {
    deleteOpen = false;
    deleteError = null;
    deleteForce = false;
  }

  async function handleDelete() {
    deleting = true;
    deleteError = null;

    try {
      await api.deleteService(serviceId, deleteForce);
      // Navigate back to services list on success
      goto('/services');
    } catch (err) {
      deleteError = err.message || 'Failed to delete service';
      deleting = false;
      // Keep modal open to allow retry with force option
    }
  }
</script>

<div class="page">
  <a href="/services" class="back">← Services</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <p class="error">Error: {error}</p>
  {:else if service}
    <div class="header">
      <h1>{service.name}</h1>
      <div class="actions">
        <LoadingButton variant="secondary" on:click={openEditModal}>
          Edit
        </LoadingButton>
        <LoadingButton variant="danger" on:click={openDeleteModal}>
          Delete
        </LoadingButton>
      </div>
    </div>
    
    <div class="info-grid">
      <Card title="Repository" value={service.artifact_repo || '-'} />
      <Card title="Runtime" value={service.runtime_type || 'docker'} />
      <Card title="Default Branch" value={service.default_branch || 'main'} />
    </div>

    <section>
      <h2>Recent Builds ({builds.length})</h2>
      <Table columns={buildColumns} data={builds.slice(0, 10)} />
    </section>

    <section>
      <h2>Artifacts ({artifacts.length})</h2>
      <Table columns={artifactColumns} data={artifacts.slice(0, 10)} />
    </section>

    <section>
      <h2>Secrets ({secrets.length})</h2>
      {#if secrets.length > 0}
        <ul class="secrets-list">
          {#each secrets as secret}
            <li><code>{secret.name}</code> (v{secret.version})</li>
          {/each}
        </ul>
      {:else}
        <p class="empty">No secrets configured</p>
      {/if}
    </section>
  {/if}
</div>

<!-- Edit Modal -->
<Modal bind:open={editOpen} title="Edit Service" on:close={closeEditModal}>
  <form on:submit|preventDefault={handleEdit} class="edit-form">
    <div class="form-field">
      <label for="edit-name">Name *</label>
      <Input
        id="edit-name"
        bind:value={editForm.name}
        placeholder="my-service"
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-artifact-repo">Artifact Repository *</label>
      <Input
        id="edit-artifact-repo"
        bind:value={editForm.artifact_repo}
        placeholder="ghcr.io/org/my-service"
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-repo-url">Repository URL</label>
      <Input
        id="edit-repo-url"
        bind:value={editForm.repo_url}
        placeholder="https://github.com/org/repo"
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-runtime-type">Runtime Type *</label>
      <Select
        id="edit-runtime-type"
        bind:value={editForm.runtime_type}
        options={runtimeOptions}
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-default-branch">Default Branch</label>
      <Input
        id="edit-default-branch"
        bind:value={editForm.default_branch}
        placeholder="main"
        disabled={editing}
      />
    </div>

    {#if editError}
      <p class="error">{editError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        on:click={closeEditModal}
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
  title="Delete Service"
  confirmLabel="Delete"
  variant="danger"
  loading={deleting}
  on:confirm={handleDelete}
  on:cancel={closeDeleteModal}
  on:close={closeDeleteModal}
>
  <div class="delete-content">
    <p>Are you sure you want to delete <strong>{service?.name}</strong>?</p>
    <p class="warning">This action cannot be undone.</p>
    
    <div class="force-option">
      <Checkbox
        id="delete-force"
        bind:checked={deleteForce}
        disabled={deleting}
        label="Force delete (remove even if deployments exist)"
      />
    </div>

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
    margin-bottom: 1rem;
  }
  .secrets-list {
    list-style: none;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .secrets-list li {
    background: var(--hover-bg);
    padding: 0.25rem 0.75rem;
    border-radius: 4px;
    font-size: 0.875rem;
  }
  .empty, .loading, .error {
    color: var(--text-muted);
    padding: 1rem;
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
  .force-option {
    padding: 0.75rem;
    background: var(--hover-bg);
    border-radius: 4px;
  }
</style>
