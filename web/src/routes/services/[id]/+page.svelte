<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import { api } from '$lib/api/client.js';
  import {
    repositories,
    createManualRepositorySelection,
    resolveSelectionFromRepoUrl,
    loadRepositories
  } from '$lib/stores/repositories.js';
  import { fetchRepoBranches, isNostrRepository } from '$lib/nostr/branches.js';
  import { secretFormSchema, secretValueSchema, serviceFormSchema, validateForm } from '$lib/validation/forms.js';

  let service = $state(null);
  let builds = $state([]);
  let artifacts = $state([]);
  let environments = $state([]);
  let artifactsLoadError = $state(null);
  let environmentsLoadError = $state(null);
  let secrets = $state([]);
  let loading = $state(true);
  let error = $state(null);
  let serviceId = $derived(page.params.id);


  // Edit modal state
  let editOpen = $state(false);
  let editing = $state(false);
  let editError = $state(null);
  let editForm = $state({
    name: '',
    repositorySelection: createManualRepositorySelection(''),
    artifact_repo: '',
    runtime_type: '',
    default_branch: ''
  });

  // Branch detection state for edit form
  let editDetectedBranches = $state([]);
  let editDetectedDefaultBranch = $state(null);
  let editBranchesLoading = $state(false);
  let editBranchesError = $state(null);


  async function handleEditRepositoryChange(selection) {
    // Reset branch state
    editDetectedBranches = [];
    editDetectedDefaultBranch = null;
    editBranchesError = null;

    // Only fetch branches for NIP-34 repos
    if (!isNostrRepository(selection)) {
      return;
    }

    editBranchesLoading = true;
    try {
      const result = await fetchRepoBranches(selection.repoCoordinate);
      editDetectedBranches = result.branches;
      editDetectedDefaultBranch = result.defaultBranch;
      editBranchesError = result.error;
    } catch (err) {
      editBranchesError = err?.message || 'Failed to fetch branches';
    } finally {
      editBranchesLoading = false;
    }
  }


  // Delete modal state
  let deleteOpen = $state(false);
  let deleting = $state(false);
  let deleteError = $state(null);
  let deleteForce = $state(false);

  // Deployment intent modal state
  let deployOpen = $state(false);
  let deploying = $state(false);
  let deployError = $state(null);
  let deployPolicyPreview = $state(null);
  let deployPolicyPreviewLoading = $state(false);
  let deployPolicyPreviewError = $state(null);
  let deployPolicyPreviewKey = $state('');
  let deployForm = $state({
    environment_id: '',
    artifact_id: ''
  });
  const COST_ESTIMATE_UNAVAILABLE_COPY = 'Cost estimate unavailable before run creation: the current API estimates cost from a deployment run ID, which does not exist until after an intent is approved and executed.';

  // Secret create modal state
  let secretCreateOpen = $state(false);
  let secretCreating = $state(false);
  let secretCreateError = $state(null);
  let secretForm = $state({
    name: '',
    value: ''
  });

  // Secret update modal state
  let secretUpdateOpen = $state(false);
  let secretUpdating = $state(false);
  let secretUpdateError = $state(null);
  let secretToUpdate = $state(null);
  let secretUpdateValue = $state('');

  // Secret reveal modal state
  let secretRevealOpen = $state(false);
  let secretRevealWarningAccepted = $state(false);
  let secretRevealValue = $state('');
  let secretRevealName = $state('');
  let secretRevealCopyStatus = $state('');
  let secretValueCache = $state({});

  // Secret delete modal state
  let secretDeleteOpen = $state(false);
  let secretDeleting = $state(false);
  let secretDeleteError = $state(null);
  let secretToDelete = $state(null);

  // Artifact registration modal state
  let artifactRegisterOpen = $state(false);
  let artifactRegistering = $state(false);
  let artifactRegisterError = $state(null);
  let artifactForm = $state({
    name: '',
    version: '',
    digest: '',
    metadata: ''
  });

  const runtimeOptions = [
    { value: 'docker', label: 'Docker' },
    { value: 'compose', label: 'Docker Compose' },
    { value: 'kubernetes', label: 'Kubernetes' },
    { value: 'podman', label: 'Podman' }
  ];

  $effect(() => {
    const id = serviceId;
    if (!id) return;
    void loadServiceDetail(id);
  });

  async function loadServiceDetail(id) {
    loading = true;
    error = null;
    service = null;
    builds = [];
    artifacts = [];
    environments = [];
    artifactsLoadError = null;
    environmentsLoadError = null;
    secrets = [];

    try {
      const [loadedService, loadedBuilds, loadedArtifacts, loadedEnvironments, loadedSecrets] = await Promise.all([
        api.getService(id),
        api.listBuilds(id).catch(() => []),
        api.listArtifacts(id).catch((err) => {
          artifactsLoadError = err.message || 'Failed to load artifacts';
          return [];
        }),
        api.listEnvironments().catch((err) => {
          environmentsLoadError = err.message || 'Failed to load environments';
          return [];
        }),
        api.listSecrets(id).catch(() => [])
      ]);

      service = loadedService;
      builds = loadedBuilds;
      artifacts = loadedArtifacts;
      environments = loadedEnvironments;
      secrets = loadedSecrets;

      await loadRepositories();
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }



  function formatDate(timestamp) {
    if (!timestamp) return '-';
    return new Date(timestamp).toLocaleDateString('en-US', {
      year: 'numeric',
      month: 'short',
      day: 'numeric'
    });
  }

  function formatBytes(bytes) {
    if (!bytes) return '-';
    const mb = bytes / (1024 * 1024);
    return `${mb.toFixed(1)} MB`;
  }

  function formatDigest(digest) {
    if (!digest) return '-';
    const value = String(digest);
    return value.length > 22 ? `${value.slice(0, 18)}…${value.slice(-6)}` : value;
  }

  function artifactDisplayName(artifact) {
    if (!artifact) return 'Unknown artifact';
    return artifact.image_tag || artifact.version || artifact.name || formatDigest(artifact.image_digest || artifact.digest || artifact.id);
  }

  function artifactBuildLabel(artifact) {
    if (!artifact) return null;
    const buildId = artifact.build_id || artifact.metadata?.build_id;
    const build = builds.find((candidate) => candidate.id === buildId);
    if (build?.git_sha) return `build ${build.git_sha.slice(0, 7)}`;
    if (buildId) return `build ${String(buildId).slice(0, 8)}`;
    return null;
  }

  function artifactOptionLabel(artifact) {
    const parts = [artifactDisplayName(artifact)];
    const digest = artifact.image_digest || artifact.digest;
    if (digest) parts.push(formatDigest(digest));
    const buildLabel = artifactBuildLabel(artifact);
    if (buildLabel) parts.push(buildLabel);
    return parts.filter(Boolean).join(' · ');
  }

  function environmentDisplayName(environment) {
    if (!environment) return 'Unknown environment';
    return environment.name || environment.slug || environment.id;
  }

  function policyStatusLabel(preview) {
    if (!preview) return 'Not evaluated';
    return preview.allowed === false ? 'Blocked' : 'Allowed';
  }

  function policyStatusClass(preview) {
    if (!preview) return 'muted';
    return preview.allowed === false ? 'error' : 'success';
  }

  function openEditModal() {
    if (!service) return;
    
    const currentRepositories = repositories;

    editForm = {
      name: service.name,
      repositorySelection: resolveSelectionFromRepoUrl(service.repo_url || '', currentRepositories),
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
    const validationResult = validateForm(serviceFormSchema, {
      name: editForm.name,
      artifactRepo: editForm.artifact_repo,
      runtimeType: editForm.runtime_type
    });
    if (!validationResult.success) {
      editError = validationResult.error;
      return;
    }

    editing = true;
    editError = null;

    try {
      const updated = await api.updateService(serviceId, {
        name: editForm.name.trim(),
        repo_url: editForm.repositorySelection?.repoUrl || '',
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

  function resetDeployPreview() {
    deployPolicyPreview = null;
    deployPolicyPreviewError = null;
    deployPolicyPreviewLoading = false;
    deployPolicyPreviewKey = '';
  }

  function openDeployModal() {
    deployForm = {
      environment_id: '',
      artifact_id: ''
    };
    deployError = null;
    resetDeployPreview();
    deployOpen = true;
  }

  function closeDeployModal() {
    if (deploying) return;
    deployOpen = false;
    deployError = null;
    resetDeployPreview();
  }

  async function loadDeploymentPolicyPreview(environmentId, artifactId) {
    const key = `${environmentId}:${artifactId}`;
    deployPolicyPreviewKey = key;
    deployPolicyPreview = null;
    deployPolicyPreviewError = null;
    deployPolicyPreviewLoading = true;

    try {
      const preview = await api.evaluatePolicy({
        artifact_id: artifactId,
        environment_id: environmentId
      });
      if (deployPolicyPreviewKey === key) {
        deployPolicyPreview = preview;
      }
    } catch (err) {
      if (deployPolicyPreviewKey === key) {
        deployPolicyPreviewError = err.message || 'Policy preview is not available';
      }
    } finally {
      if (deployPolicyPreviewKey === key) {
        deployPolicyPreviewLoading = false;
      }
    }
  }

  async function handleDeploy() {
    if (!deployForm.environment_id) {
      deployError = 'Select an environment';
      return;
    }
    if (!deployForm.artifact_id) {
      deployError = 'Select an artifact';
      return;
    }

    deploying = true;
    deployError = null;

    try {
      await api.createIntent(serviceId, deployForm.environment_id, deployForm.artifact_id);
      deployOpen = false;
      goto('/deployments');
    } catch (err) {
      deployError = err.message || 'Failed to create deployment intent';
    } finally {
      deploying = false;
    }
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

  async function reloadSecrets() {
    try {
      secrets = await api.listSecrets(serviceId);
    } catch (err) {
      // Keep current list intact on reload failure
      console.error('Failed to reload secrets:', err);
    }
  }

  function openSecretCreateModal() {
    secretForm = {
      name: '',
      value: ''
    };
    secretCreateError = null;
    secretCreateOpen = true;
  }

  function closeSecretCreateModal() {
    secretCreateOpen = false;
    secretCreateError = null;
    // Clear value immediately for security
    secretForm.value = '';
  }

  function openSecretUpdateModal(secret) {
    secretToUpdate = secret;
    secretUpdateValue = '';
    secretUpdateError = null;
    secretUpdateOpen = true;
  }

  function closeSecretUpdateModal() {
    secretUpdateOpen = false;
    secretUpdateError = null;
    secretToUpdate = null;
    secretUpdateValue = '';
  }

  function openSecretRevealModal(secretName, value) {
    secretRevealName = secretName;
    secretRevealValue = value;
    secretRevealWarningAccepted = false;
    secretRevealCopyStatus = '';
    secretRevealOpen = true;
  }

  function closeSecretRevealModal() {
    secretRevealOpen = false;
    secretRevealWarningAccepted = false;
    secretRevealValue = '';
    secretRevealName = '';
    secretRevealCopyStatus = '';
  }

  function handleSecretReveal(secret) {
    const value = secretValueCache[secret.id];
    if (!value) return;
    openSecretRevealModal(secret.name, value);
  }

  async function handleCopyRevealedSecret() {
    if (!secretRevealValue) return;

    try {
      await navigator.clipboard.writeText(secretRevealValue);
      secretRevealCopyStatus = 'Copied';
    } catch {
      secretRevealCopyStatus = 'Copy failed';
    }
  }

  async function handleSecretCreate() {
    const validationResult = validateForm(secretFormSchema, secretForm);
    if (!validationResult.success) {
      secretCreateError = validationResult.error;
      return;
    }

    secretCreating = true;
    secretCreateError = null;

    try {
      const createdSecret = await api.createSecret(serviceId, {
        name: secretForm.name.trim(),
        value: secretForm.value
      });

      const createdValue = secretForm.value;

      // Clear value immediately for security
      secretForm.value = '';
      
      // Close modal and reload secrets
      closeSecretCreateModal();
      await reloadSecrets();

      if (createdSecret?.id && createdValue) {
        secretValueCache = {
          ...secretValueCache,
          [createdSecret.id]: createdValue
        };
        openSecretRevealModal(createdSecret.name || secretForm.name.trim(), createdValue);
      }
    } catch (err) {
      // Never log the secret value
      secretCreateError = err.message || 'Failed to create secret';
    } finally {
      secretCreating = false;
    }
  }

  async function handleSecretUpdate() {
    if (!secretToUpdate) return;

    const validationResult = validateForm(secretValueSchema, { value: secretUpdateValue });
    if (!validationResult.success) {
      secretUpdateError = validationResult.error;
      return;
    }

    secretUpdating = true;
    secretUpdateError = null;

    try {
      const secretName = secretToUpdate.name;
      const updatedSecret = await api.updateSecret(serviceId, secretToUpdate.id, {
        value: secretUpdateValue
      });

      const updatedValue = secretUpdateValue;
      closeSecretUpdateModal();
      await reloadSecrets();

      if (updatedSecret?.id && updatedValue) {
        secretValueCache = {
          ...secretValueCache,
          [updatedSecret.id]: updatedValue
        };
        openSecretRevealModal(secretName, updatedValue);
      }
    } catch (err) {
      secretUpdateError = err.message || 'Failed to update secret';
    } finally {
      secretUpdating = false;
    }
  }

  function openSecretDeleteModal(secret) {
    secretToDelete = secret;
    secretDeleteError = null;
    secretDeleteOpen = true;
  }

  function closeSecretDeleteModal() {
    secretDeleteOpen = false;
    secretDeleteError = null;
    secretToDelete = null;
  }

  async function handleSecretDelete() {
    if (!secretToDelete) return;

    secretDeleting = true;
    secretDeleteError = null;

    try {
      await api.deleteSecret(serviceId, secretToDelete.id);
      
      // Close dialog and reload secrets
      closeSecretDeleteModal();
      await reloadSecrets();
    } catch (err) {
      secretDeleteError = err.message || 'Failed to delete secret';
    } finally {
      secretDeleting = false;
    }
  }

  async function reloadArtifacts() {
    try {
      artifacts = await api.listArtifacts(serviceId);
    } catch (err) {
      console.error('Failed to reload artifacts:', err);
    }
  }

  function openArtifactRegisterModal() {
    artifactForm = {
      name: '',
      version: '',
      digest: '',
      metadata: ''
    };
    artifactRegisterError = null;
    artifactRegisterOpen = true;
  }

  function closeArtifactRegisterModal() {
    artifactRegisterOpen = false;
    artifactRegisterError = null;
  }

  async function handleArtifactRegister() {
    // Validate required fields
    if (!artifactForm.name.trim()) {
      artifactRegisterError = 'Artifact name is required';
      return;
    }
    if (!artifactForm.version.trim()) {
      artifactRegisterError = 'Version is required';
      return;
    }
    if (!artifactForm.digest.trim()) {
      artifactRegisterError = 'Digest is required';
      return;
    }

    // Validate metadata JSON if provided
    let metadata = null;
    if (artifactForm.metadata.trim()) {
      try {
        metadata = JSON.parse(artifactForm.metadata);
      } catch (err) {
        artifactRegisterError = 'Metadata must be valid JSON';
        return;
      }
    }

    artifactRegistering = true;
    artifactRegisterError = null;

    try {
      await api.registerArtifact({
        service_id: serviceId,
        name: artifactForm.name.trim(),
        version: artifactForm.version.trim(),
        digest: artifactForm.digest.trim(),
        metadata: metadata
      });
      
      // Close modal and reload artifacts
      closeArtifactRegisterModal();
      await reloadArtifacts();
    } catch (err) {
      artifactRegisterError = err.message || 'Failed to register artifact';
    } finally {
      artifactRegistering = false;
    }
  }
  // Watch for edit form repository selection changes
  $effect(() => {
    if (editOpen && editForm.repositorySelection) {
      handleEditRepositoryChange(editForm.repositorySelection);
    }
  });
  let editBranchOptions = $derived(editDetectedBranches.map(b => ({
    value: b,
    label: b === editDetectedDefaultBranch ? `${b} (default)` : b
  })));

  let deployEnvironmentOptions = $derived(environments.map(environment => ({
    value: environment.id,
    label: environmentDisplayName(environment)
  })));
  let deployArtifactOptions = $derived(artifacts.map(artifact => ({
    value: artifact.id,
    label: artifactOptionLabel(artifact)
  })));
  let selectedDeployEnvironment = $derived(environments.find(environment => environment.id === deployForm.environment_id));
  let selectedDeployArtifact = $derived(artifacts.find(artifact => artifact.id === deployForm.artifact_id));
  let selectedDeployArtifactBuild = $derived.by(() => {
    if (!selectedDeployArtifact) return null;
    const buildId = selectedDeployArtifact.build_id || selectedDeployArtifact.metadata?.build_id;
    return builds.find(build => build.id === buildId) || null;
  });

  $effect(() => {
    if (!deployOpen) return;
    const environmentId = deployForm.environment_id;
    const artifactId = deployForm.artifact_id;
    if (!environmentId || !artifactId) {
      resetDeployPreview();
      return;
    }
    void loadDeploymentPolicyPreview(environmentId, artifactId);
  });

  let buildColumns = $derived([
    { key: 'git_sha', label: 'Commit', render: (r) => `<code>${r.git_sha?.slice(0, 7)}</code>` },
    { key: 'git_ref', label: 'Ref' },
    { key: 'status', label: 'Status' },
    { key: 'ci_system', label: 'CI' }
  ]);
  let artifactColumns = $derived([
    { key: 'image_tag', label: 'Tag' },
    { key: 'image_digest', label: 'Digest', render: (r) => `<code>${r.image_digest?.slice(7, 19)}...</code>` },
    { key: 'size_bytes', label: 'Size', render: (r) => formatBytes(r.size_bytes) }
  ]);
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
        <LoadingButton variant="primary" onclick={openDeployModal}>
          Deploy
        </LoadingButton>
        <LoadingButton variant="secondary" onclick={openEditModal}>
          Edit
        </LoadingButton>
        <LoadingButton variant="danger" onclick={openDeleteModal}>
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
      <div class="section-header">
        <h2>Artifacts ({artifacts.length})</h2>
        <LoadingButton variant="primary" onclick={openArtifactRegisterModal}>
          Register Artifact
        </LoadingButton>
      </div>
      {#if artifacts.length > 0}
        <div class="artifacts-table">
          {#each artifacts as artifact}
            <a href="/artifacts/{artifact.id}" class="artifact-row">
              <div class="artifact-info">
                <div class="artifact-main">
                  <code class="artifact-name">{artifact.name}</code>
                  <span class="artifact-version">v{artifact.version}</span>
                </div>
                <div class="artifact-meta">
                  <span class="artifact-created">{formatDate(artifact.created_at)}</span>
                  {#if artifact.sbom_url}
                    <span class="badge badge-sbom">SBOM</span>
                  {/if}
                  {#if artifact.signatures && artifact.signatures.length > 0}
                    <span class="badge badge-signatures">{artifact.signatures.length} signature{artifact.signatures.length > 1 ? 's' : ''}</span>
                  {/if}
                </div>
              </div>
            </a>
          {/each}
        </div>
      {:else}
        <div class="empty-state">
          <p class="empty">No artifacts registered</p>
          <LoadingButton variant="primary" onclick={openArtifactRegisterModal}>
            Register Your First Artifact
          </LoadingButton>
        </div>
      {/if}
    </section>

    <section>
      <div class="section-header">
        <h2>Secrets ({secrets.length})</h2>
        <LoadingButton variant="primary" onclick={openSecretCreateModal}>
          Add Secret
        </LoadingButton>
      </div>
      {#if secrets.length > 0}
        <div class="secrets-table">
          {#each secrets as secret}
            <div class="secret-row">
              <div class="secret-info">
                <code class="secret-name">{secret.name}</code>
                <span class="secret-version">v{secret.version}</span>
                {#if secret.environment_id}
                  <span class="secret-scope">env: {secret.environment_id}</span>
                {/if}
              </div>
              <div class="secret-actions">
                <LoadingButton
                  variant="secondary"
                  disabled={!secretValueCache[secret.id]}
                  onclick={() => handleSecretReveal(secret)}
                >
                  Reveal
                </LoadingButton>
                <LoadingButton
                  variant="secondary"
                  onclick={() => openSecretUpdateModal(secret)}
                >
                  Update
                </LoadingButton>
                <LoadingButton 
                  variant="danger" 
                  onclick={() => openSecretDeleteModal(secret)}
                >
                  Delete
                </LoadingButton>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="empty-state">
          <p class="empty">No secrets configured</p>
          <LoadingButton variant="primary" onclick={openSecretCreateModal}>
            Add Your First Secret
          </LoadingButton>
        </div>
      {/if}
    </section>
  {/if}
</div>

<!-- Edit Modal -->
<Modal bind:open={editOpen} title="Edit Service" onClose={closeEditModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleEdit(); }} class="edit-form">
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
      <RepositoryPicker bind:value={editForm.repositorySelection} context="service" disabled={editing} />
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
      {#if editBranchesLoading}
        <div class="branch-loading">
          <span class="spinner-small"></span>
          Detecting branches...
        </div>
      {:else if editDetectedBranches.length > 0}
        <Select
          id="edit-default-branch"
          bind:value={editForm.default_branch}
          options={editBranchOptions}
          disabled={editing}
        />
        {#if editDetectedDefaultBranch}
          <span class="branch-hint">Detected from repository state</span>
        {/if}
      {:else}
        <Input
          id="edit-default-branch"
          bind:value={editForm.default_branch}
          placeholder="main"
          disabled={editing}
        />
        {#if isNostrRepository(editForm.repositorySelection) && !editBranchesError}
          <span class="branch-hint">No branches detected - enter manually</span>
        {/if}
      {/if}
      {#if editBranchesError}
        <span class="branch-error">{editBranchesError}</span>
      {/if}
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
  title="Delete Service"
  confirmLabel="Delete"
  variant="danger"
  loading={deleting}
  onConfirm={handleDelete}
  onCancel={closeDeleteModal}
  onClose={closeDeleteModal}
>
  <div class="delete-content">
    <p>Are you sure you want to delete <strong>{service?.name}</strong>?</p>
    <p class="warning">This action cannot be undone.</p>
    <p class="warning">Deleting this service will cascade to related resources (such as secrets, artifacts, and deployment records).</p>

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

<!-- Deployment Intent Modal -->
<Modal bind:open={deployOpen} title="Create Deployment Intent" size="lg" onClose={closeDeployModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleDeploy(); }} class="deploy-form">
    <p class="modal-intro">
      Choose where to deploy <strong>{service?.name}</strong> and which recent artifact should be promoted.
    </p>

    {#if environmentsLoadError || artifactsLoadError}
      <div class="deploy-input-warning" role="alert">
        {#if environmentsLoadError}
          <p>Unable to load environments: {environmentsLoadError}</p>
        {/if}
        {#if artifactsLoadError}
          <p>Unable to load artifacts: {artifactsLoadError}</p>
        {/if}
      </div>
    {/if}

    <div class="form-field">
      <label for="deploy-environment">Environment *</label>
      <Select
        id="deploy-environment"
        bind:value={deployForm.environment_id}
        options={deployEnvironmentOptions}
        placeholder={environments.length > 0 ? 'Select environment' : 'No environments available'}
        required
        disabled={deploying || environments.length === 0}
      />
      <span class="field-hint">Deployment intent will target the selected environment.</span>
    </div>

    <div class="form-field">
      <label for="deploy-artifact">Artifact from recent builds *</label>
      <Select
        id="deploy-artifact"
        bind:value={deployForm.artifact_id}
        options={deployArtifactOptions}
        placeholder={artifacts.length > 0 ? 'Select artifact' : 'No artifacts registered'}
        required
        disabled={deploying || artifacts.length === 0}
      />
      <span class="field-hint">
        Artifacts are sourced from this service's registered artifacts. Build details are shown when the artifact includes build metadata.
      </span>
    </div>

    {#if selectedDeployEnvironment || selectedDeployArtifact}
      <div class="deploy-summary">
        <h3>Selection Summary</h3>
        <dl>
          <div>
            <dt>Environment</dt>
            <dd>{selectedDeployEnvironment ? environmentDisplayName(selectedDeployEnvironment) : 'Select an environment'}</dd>
          </div>
          <div>
            <dt>Artifact</dt>
            <dd>{selectedDeployArtifact ? artifactOptionLabel(selectedDeployArtifact) : 'Select an artifact'}</dd>
          </div>
          {#if selectedDeployArtifactBuild}
            <div>
              <dt>Build</dt>
              <dd>
                {selectedDeployArtifactBuild.git_sha?.slice(0, 7) || selectedDeployArtifactBuild.id}
                {selectedDeployArtifactBuild.git_ref ? ` · ${selectedDeployArtifactBuild.git_ref}` : ''}
              </dd>
            </div>
          {/if}
        </dl>
      </div>
    {/if}

    <div class="preview-grid">
      <section class="preview-card">
        <div class="preview-card-header">
          <h3>Policy Preview</h3>
          {#if deployPolicyPreview}
            <span class="status-pill {policyStatusClass(deployPolicyPreview)}">{policyStatusLabel(deployPolicyPreview)}</span>
          {/if}
        </div>
        {#if !deployForm.environment_id || !deployForm.artifact_id}
          <p class="preview-muted">Select an environment and artifact to preview policy evaluation.</p>
        {:else if deployPolicyPreviewLoading}
          <p class="preview-muted">Evaluating policies...</p>
        {:else if deployPolicyPreviewError}
          <p class="preview-warning">
            Policy preview unavailable: {deployPolicyPreviewError}. You can still create the intent; backend policy enforcement remains authoritative.
          </p>
        {:else if deployPolicyPreview}
          <p class="preview-muted">
            {deployPolicyPreview.results?.length || 0} policy result{(deployPolicyPreview.results?.length || 0) === 1 ? '' : 's'} ·
            {deployPolicyPreview.warnings || 0} warning{(deployPolicyPreview.warnings || 0) === 1 ? '' : 's'} ·
            {deployPolicyPreview.blockers || 0} blocker{(deployPolicyPreview.blockers || 0) === 1 ? '' : 's'}
          </p>
          {#if deployPolicyPreview.results?.length > 0}
            <ul class="policy-results">
              {#each deployPolicyPreview.results as result}
                <li>
                  <span class="status-dot" class:pass={result.passed} class:fail={!result.passed}></span>
                  <span>
                    <strong>{result.policy_name || result.policy_id}</strong>
                    <small>{result.enforcement || 'warn'}</small>
                    {#if result.violations?.length > 0}
                      <em>{result.violations.map(v => v.message || v.rule).join('; ')}</em>
                    {/if}
                  </span>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="preview-muted">No applicable policy rules were returned for this selection.</p>
          {/if}
        {/if}
      </section>

      <section class="preview-card">
        <h3>Cost Estimate</h3>
        <p class="preview-muted">{COST_ESTIMATE_UNAVAILABLE_COPY}</p>
      </section>
    </div>

    {#if deployError}
      <p class="error">{deployError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        onclick={closeDeployModal}
        disabled={deploying}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={deploying}
        disabled={!deployForm.environment_id || !deployForm.artifact_id}
      >
        Create Intent
      </LoadingButton>
    </div>
  </form>
</Modal>

<!-- Secret Create Modal -->
<Modal bind:open={secretCreateOpen} title="Add Secret" onClose={closeSecretCreateModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleSecretCreate(); }} class="secret-form">
    <div class="form-field">
      <label for="secret-name">Name *</label>
      <Input
        id="secret-name"
        bind:value={secretForm.name}
        placeholder="DATABASE_URL"
        required
        disabled={secretCreating}
      />
      <span class="field-hint">A unique identifier for this secret</span>
    </div>

    <div class="form-field">
      <label for="secret-value">Value *</label>
      <Textarea
        id="secret-value"
        bind:value={secretForm.value}
        placeholder="Enter the secret value..."
        rows={6}
        required
        disabled={secretCreating}
      />
      <span class="field-hint">The secret value will be encrypted and never displayed</span>
    </div>

    {#if secretCreateError}
      <p class="error">{secretCreateError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        onclick={closeSecretCreateModal}
        disabled={secretCreating}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={secretCreating}
      >
        Create Secret
      </LoadingButton>
    </div>
  </form>
</Modal>

<!-- Secret Update Modal -->
<Modal bind:open={secretUpdateOpen} title="Update Secret" onClose={closeSecretUpdateModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleSecretUpdate(); }} class="secret-form">
    <div class="form-field">
      <label for="secret-update-name">Name</label>
      <Input id="secret-update-name" value={secretToUpdate?.name || ''} disabled />
    </div>

    <div class="form-field">
      <label for="secret-update-value">New Value *</label>
      <Textarea
        id="secret-update-value"
        bind:value={secretUpdateValue}
        placeholder="Enter the new secret value..."
        rows={6}
        required
        disabled={secretUpdating}
      />
      <span class="field-hint">The old value cannot be viewed. Save this value securely.</span>
    </div>

    {#if secretUpdateError}
      <p class="error">{secretUpdateError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton type="button" variant="secondary" onclick={closeSecretUpdateModal} disabled={secretUpdating}>
        Cancel
      </LoadingButton>
      <LoadingButton type="submit" variant="primary" loading={secretUpdating}>
        Update Secret
      </LoadingButton>
    </div>
  </form>
</Modal>

<!-- Secret Reveal Warning Modal -->
<Modal bind:open={secretRevealOpen} title="Reveal Secret Value" onClose={closeSecretRevealModal}>
  <div class="secret-reveal-flow">
    <p class="warning">Warning: this will display the plaintext value for <code>{secretRevealName}</code> on screen.</p>

    {#if !secretRevealWarningAccepted}
      <div class="form-actions">
        <LoadingButton type="button" variant="secondary" onclick={closeSecretRevealModal}>Cancel</LoadingButton>
        <LoadingButton type="button" variant="danger" onclick={() => { secretRevealWarningAccepted = true; }}>Reveal Value</LoadingButton>
      </div>
    {:else}
      <div class="revealed-secret" role="status">
        <code>{secretRevealValue}</code>
      </div>
      <div class="form-actions">
        <LoadingButton type="button" variant="secondary" onclick={closeSecretRevealModal}>Close</LoadingButton>
        <LoadingButton type="button" variant="primary" onclick={handleCopyRevealedSecret}>Copy to Clipboard</LoadingButton>
      </div>
      {#if secretRevealCopyStatus}
        <p class="field-hint">{secretRevealCopyStatus}</p>
      {/if}
    {/if}
  </div>
</Modal>

<!-- Secret Delete Confirmation Dialog -->
<ConfirmDialog
  bind:open={secretDeleteOpen}
  title="Delete Secret"
  confirmLabel="Delete"
  variant="danger"
  loading={secretDeleting}
  onConfirm={handleSecretDelete}
  onCancel={closeSecretDeleteModal}
  onClose={closeSecretDeleteModal}
>
  <div class="delete-content">
    <p>Are you sure you want to delete the secret <code>{secretToDelete?.name}</code>?</p>
    <p class="warning">This action cannot be undone. Any services using this secret will lose access.</p>

    {#if secretDeleteError}
      <p class="error">{secretDeleteError}</p>
    {/if}
  </div>
</ConfirmDialog>

<!-- Artifact Registration Modal -->
<Modal bind:open={artifactRegisterOpen} title="Register Artifact" onClose={closeArtifactRegisterModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleArtifactRegister(); }} class="artifact-form">
    <div class="form-field">
      <label for="artifact-name">Name *</label>
      <Input
        id="artifact-name"
        bind:value={artifactForm.name}
        placeholder="my-service"
        required
        disabled={artifactRegistering}
      />
      <span class="field-hint">The artifact name (e.g., service name or image name)</span>
    </div>

    <div class="form-field">
      <label for="artifact-version">Version *</label>
      <Input
        id="artifact-version"
        bind:value={artifactForm.version}
        placeholder="1.0.0"
        required
        disabled={artifactRegistering}
      />
      <span class="field-hint">Semantic version or tag</span>
    </div>

    <div class="form-field">
      <label for="artifact-digest">Digest *</label>
      <Input
        id="artifact-digest"
        bind:value={artifactForm.digest}
        placeholder="sha256:abcdef123456..."
        required
        disabled={artifactRegistering}
      />
      <span class="field-hint">Content-addressable digest (e.g., SHA256 hash)</span>
    </div>

    <div class="form-field">
      <label for="artifact-metadata">Metadata (JSON)</label>
      <Textarea
        id="artifact-metadata"
        bind:value={artifactForm.metadata}
        placeholder={'{"build_id": "123", "commit": "abc123"}'}
        rows={6}
        disabled={artifactRegistering}
      />
      <span class="field-hint">Optional JSON metadata about the artifact</span>
    </div>

    {#if artifactRegisterError}
      <p class="error">{artifactRegisterError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        onclick={closeArtifactRegisterModal}
        disabled={artifactRegistering}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={artifactRegistering}
      >
        Register Artifact
      </LoadingButton>
    </div>
  </form>
</Modal>

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

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1rem;
  }
  .section-header h2 {
    margin-bottom: 0;
  }

  .secrets-table {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .secret-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem;
    background: var(--hover-bg);
    border-radius: 4px;
    border: 1px solid var(--border-color);
  }
  .secret-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex: 1;
  }
  .secret-actions {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }
  .secret-reveal-flow {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .revealed-secret {
    padding: 0.75rem;
    border-radius: 4px;
    border: 1px solid var(--border-color);
    background: var(--hover-bg);
    word-break: break-all;
  }
  .secret-name {
    font-weight: 500;
    color: var(--text-primary);
  }
  .secret-version {
    font-size: 0.75rem;
    color: var(--text-muted);
    padding: 0.125rem 0.5rem;
    background: var(--bg);
    border-radius: 3px;
  }
  .secret-scope {
    font-size: 0.75rem;
    color: var(--text-muted);
    font-style: italic;
  }

  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1rem;
    padding: 2rem;
  }
  .empty-state .empty {
    margin: 0;
  }

  .secret-form,
  .deploy-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .modal-intro {
    margin: 0;
    color: var(--text-muted);
    line-height: 1.5;
  }
  .deploy-input-warning,
  .deploy-summary {
    padding: 1rem;
    background: var(--hover-bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }
  .deploy-input-warning {
    border-color: var(--warning, #d97706);
    color: var(--warning, #d97706);
    font-size: 0.875rem;
  }
  .deploy-input-warning p {
    margin: 0;
  }
  .deploy-input-warning p + p {
    margin-top: 0.5rem;
  }
  .deploy-summary h3,
  .preview-card h3 {
    margin: 0;
    font-size: 0.95rem;
    color: var(--text-primary);
  }
  .deploy-summary dl {
    display: grid;
    gap: 0.75rem;
    margin: 0.75rem 0 0;
  }
  .deploy-summary dl > div {
    display: grid;
    grid-template-columns: 110px 1fr;
    gap: 0.75rem;
  }
  .deploy-summary dt {
    color: var(--text-muted);
    font-size: 0.8rem;
  }
  .deploy-summary dd {
    margin: 0;
    color: var(--text-primary);
    font-size: 0.875rem;
  }
  .preview-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.4fr) minmax(220px, 0.8fr);
    gap: 1rem;
  }
  .preview-card {
    margin: 0;
    padding: 1rem;
    background: var(--hover-bg);
  }
  .preview-card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    margin-bottom: 0.75rem;
  }
  .preview-muted,
  .preview-warning {
    margin: 0.75rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
    line-height: 1.5;
  }
  .preview-warning {
    color: var(--warning, #d97706);
  }
  .status-pill {
    border-radius: 999px;
    padding: 0.2rem 0.55rem;
    font-size: 0.75rem;
    font-weight: 600;
  }
  .status-pill.success {
    background: #e8f5e9;
    color: #2e7d32;
  }
  .status-pill.error {
    background: #ffebee;
    color: #c62828;
  }
  .status-pill.muted {
    background: var(--bg);
    color: var(--text-muted);
  }
  .policy-results {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    list-style: none;
    padding: 0;
    margin: 1rem 0 0;
  }
  .policy-results li {
    display: flex;
    gap: 0.5rem;
    align-items: flex-start;
  }
  .policy-results small,
  .policy-results em {
    display: block;
    color: var(--text-muted);
    font-size: 0.75rem;
    font-style: normal;
    margin-top: 0.125rem;
  }
  .status-dot {
    width: 0.65rem;
    height: 0.65rem;
    border-radius: 999px;
    margin-top: 0.25rem;
    background: var(--text-muted);
    flex: 0 0 auto;
  }
  .status-dot.pass { background: #2e7d32; }
  .status-dot.fail { background: #c62828; }
  @media (max-width: 720px) {
    .preview-grid {
      grid-template-columns: 1fr;
    }
    .deploy-summary dl > div {
      grid-template-columns: 1fr;
      gap: 0.25rem;
    }
  }
  .field-hint {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-top: 0.25rem;
  }

  .artifacts-table {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .artifact-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem;
    background: var(--hover-bg);
    border-radius: 4px;
    border: 1px solid var(--border-color);
    text-decoration: none;
    color: inherit;
    transition: background 0.2s;
  }
  .artifact-row:hover {
    background: var(--bg);
    border-color: var(--primary);
  }
  .artifact-info {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    flex: 1;
  }
  .artifact-main {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .artifact-name {
    font-weight: 500;
    color: var(--text-primary);
    font-size: 0.9375rem;
  }
  .artifact-version {
    font-size: 0.75rem;
    color: var(--text-muted);
    padding: 0.125rem 0.5rem;
    background: var(--bg);
    border-radius: 3px;
  }
  .artifact-meta {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .artifact-created {
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .badge {
    font-size: 0.625rem;
    font-weight: 600;
    text-transform: uppercase;
    padding: 0.25rem 0.5rem;
    border-radius: 3px;
    letter-spacing: 0.025em;
  }
  .badge-sbom {
    background: #e3f2fd;
    color: #1976d2;
  }
  .badge-signatures {
    background: #e8f5e9;
    color: #388e3c;
  }

  .artifact-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
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
</style>
