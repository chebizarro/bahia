<script>
  import { page } from '$app/state';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Select from '$lib/components/Select.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { ArtifactIcon, WarningIcon, UnknownIcon } from '$lib/icons/domain-icons.js';
  import {
    packageRepositories,
    packageArtifacts,
    packagePromotions,
    loadPackageRepositories,
    loadPackageArtifacts,
    loadPackagePromotions
  } from '$lib/stores';
  import { promotePackage, yankPackage } from '$lib/stores/public-controlplane.svelte.js';

  let loading = $state(true);
  let error = $state(null);
  let actionError = $state(null);
  let promoteOpen = $state(false);
  let yankOpen = $state(false);
  let submitting = $state(false);
  let selectedArtifact = $state(null);
  let promoteForm = $state({ target_repository_id: '', environment: '', channel: '', metadata: '' });
  let yankForm = $state({ reason: '', deprecated: false, metadata: '' });

  let repositoryId = $derived(page.params.id);
  let repository = $derived(packageRepositories.find((candidate) => candidate.id === repositoryId) || null);
  let artifacts = $derived(packageArtifacts.filter((artifact) => artifact.repository_id === repositoryId && !artifact.deleted));
  let promotions = $derived(packagePromotions.filter((promotion) => promotion.repository_id === repositoryId || artifacts.some((artifact) => artifact.id === promotion.artifact_id)));
  let targetRepositoryOptions = $derived(packageRepositories
    .filter((candidate) => candidate.id !== repositoryId && !candidate.deleted)
    .map((candidate) => ({ value: candidate.id, label: candidate.name || candidate.id })));

  $effect(() => {
    if (!repositoryId) return;
    void loadDetail();
  });

  async function loadDetail() {
    loading = true;
    error = null;
    try {
      await Promise.all([loadPackageRepositories(), loadPackageArtifacts(), loadPackagePromotions()]);
      if (!packageRepositories.find((candidate) => candidate.id === repositoryId)) {
        throw new Error('Package repository not found');
      }
    } catch (err) {
      error = err.message || 'Failed to load package repository';
    } finally {
      loading = false;
    }
  }

  function formatDate(value) {
    if (!value) return '-';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '-';
    return date.toLocaleString();
  }

  function formatBytes(bytes) {
    const value = Number(bytes || 0);
    if (!value) return '-';
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
    return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  }

  function formatDigest(digest) {
    if (!digest) return '-';
    const value = String(digest);
    return value.length > 24 ? `${value.slice(0, 18)}…${value.slice(-6)}` : value;
  }

  function displayConfig(repo) {
    if (!repo) return '-';
    return JSON.stringify({
      backend_ref: repo.backend_ref,
      external_repository_name: repo.external_repository_name,
      namespace_prefix: repo.namespace_prefix,
      public_url: repo.public_url,
      policy: repo.policy,
      metadata: repo.metadata
    }, null, 2);
  }

  function driftStatus(repo) {
    const metadata = repo?.metadata || {};
    return metadata.drift_status || metadata.drift || metadata.backend_drift || 'unknown';
  }

  function artifactPromotions(artifact) {
    return promotions.filter((promotion) => promotion.artifact_id === artifact.id);
  }

  function artifactName(artifact) {
    return artifact.package_name || artifact.name || artifact.filename || artifact.id;
  }

  function openPromote(artifact) {
    selectedArtifact = artifact;
    actionError = null;
    promoteForm = { target_repository_id: '', environment: '', channel: '', metadata: '' };
    promoteOpen = true;
  }

  function openYank(artifact) {
    selectedArtifact = artifact;
    actionError = null;
    yankForm = { reason: '', deprecated: false, metadata: '' };
    yankOpen = true;
  }

  function parseMetadata(raw) {
    if (!raw.trim()) return undefined;
    return JSON.parse(raw);
  }

  async function handlePromote() {
    if (!selectedArtifact || !repository) return;
    if (!promoteForm.target_repository_id) {
      actionError = 'Select a target repository';
      return;
    }
    const target = packageRepositories.find((candidate) => candidate.id === promoteForm.target_repository_id);
    submitting = true;
    actionError = null;
    try {
      await promotePackage({
        source_repository_id: repository.id,
        source_repository_name: repository.name,
        target_repository_id: target?.id,
        target_repository_name: target?.name,
        namespace: selectedArtifact.namespace || '',
        package_name: selectedArtifact.package_name,
        version: selectedArtifact.version,
        filename: selectedArtifact.filename,
        environment: promoteForm.environment.trim(),
        channel: promoteForm.channel.trim(),
        metadata: parseMetadata(promoteForm.metadata || '')
      });
      promoteOpen = false;
      await loadDetail();
    } catch (err) {
      actionError = err.message || 'Failed to promote package';
    } finally {
      submitting = false;
    }
  }

  async function handleYank() {
    if (!selectedArtifact || !repository) return;
    submitting = true;
    actionError = null;
    try {
      await yankPackage({
        repository_id: repository.id,
        repository_name: repository.name,
        namespace: selectedArtifact.namespace || '',
        package_name: selectedArtifact.package_name,
        version: selectedArtifact.version,
        filename: selectedArtifact.filename,
        reason: yankForm.reason.trim(),
        deprecated: yankForm.deprecated,
        metadata: parseMetadata(yankForm.metadata || '')
      });
      yankOpen = false;
      await loadDetail();
    } catch (err) {
      actionError = err.message || 'Failed to yank package';
    } finally {
      submitting = false;
    }
  }

  let artifactColumns = $derived([
    { key: 'package_name', label: 'Name', text: artifactName },
    { key: 'version', label: 'Version' },
    { key: 'status', label: 'Status' },
    { key: 'sha256', label: 'Digest', render: (row) => `<code>${formatDigest(row.sha256)}</code>` },
    { key: 'size_bytes', label: 'Size', render: (row) => formatBytes(row.size_bytes) },
    { key: 'created_at', label: 'Published', render: (row) => formatDate(row.published_at || row.created_at) },
    {
      key: 'actions',
      label: 'Actions',
      render: (row) => `<button type="button" class="package-action" data-action="promote" data-artifact-id="${row.id}">Promote</button> <button type="button" class="package-action danger" data-action="yank" data-artifact-id="${row.id}">Yank</button>`
    }
  ]);

  function handleArtifactClick(row, event) {
    const button = event?.target?.closest('.package-action');
    if (!button) return;
    event.preventDefault();
    event.stopPropagation();
    const artifact = artifacts.find((candidate) => candidate.id === button.dataset.artifactId) || row;
    if (button.dataset.action === 'promote') openPromote(artifact);
    if (button.dataset.action === 'yank') openYank(artifact);
  }
</script>

<div class="page">
  <a href="/packages" class="back">← Packages</a>

  {#if loading}
    <p class="loading">Loading package repository...</p>
  {:else if error}
    <EmptyState iconComponent={WarningIcon} title="Unable to load package repository" message={error} />
  {:else if repository}
    <div class="header">
      <h1><ArtifactIcon size={28} strokeWidth={1.75} ariaHidden="true" /> {repository.name}</h1>
      <span class="drift {driftStatus(repository)}">Drift: {driftStatus(repository)}</span>
    </div>

    <div class="info-grid">
      <Card title="Backend" titleIcon={ArtifactIcon} value={repository.backend_type || '-'} subtitle={repository.backend_ref || ''} />
      <Card title="Format" titleIcon={ArtifactIcon} value={repository.format || '-'} />
      <Card title="Status" titleIcon={UnknownIcon} value={repository.status || '-'} status={repository.status === 'ready' ? 'success' : 'default'} />
      <Card title="Artifacts" titleIcon={ArtifactIcon} value={artifacts.length} />
    </div>

    <section>
      <h2>Repository overview</h2>
      <dl class="details">
        <div><dt>Name</dt><dd>{repository.name || '-'}</dd></div>
        <div><dt>Backend</dt><dd>{repository.backend_type || '-'} · {repository.backend_ref || '-'}</dd></div>
        <div><dt>Format</dt><dd>{repository.format || '-'}</dd></div>
        <div><dt>Status</dt><dd>{repository.status || '-'}</dd></div>
        <div><dt>Created</dt><dd>{formatDate(repository.created_at)}</dd></div>
        <div><dt>Updated</dt><dd>{formatDate(repository.updated_at)}</dd></div>
      </dl>
      <h3>Config</h3>
      <pre>{displayConfig(repository)}</pre>
    </section>

    <section>
      <h2>Artifacts ({artifacts.length})</h2>
      {#if artifacts.length === 0}
        <EmptyState iconComponent={ArtifactIcon} title="No package artifacts" message="Artifacts will appear after packages are published." />
      {:else}
        <Table columns={artifactColumns} data={artifacts} onRowClick={handleArtifactClick} rowClickable={false} />
      {/if}
    </section>

    <section>
      <h2>Promotion history</h2>
      {#if promotions.length === 0}
        <p class="muted">No promotions or publications recorded.</p>
      {:else}
        <div class="promotion-list">
          {#each artifacts as artifact}
            {@const history = artifactPromotions(artifact)}
            {#if history.length > 0}
              <div class="promotion-group">
                <h3>{artifactName(artifact)} {artifact.version ? `v${artifact.version}` : ''}</h3>
                {#each history as promotion}
                  <div class="promotion-row">
                    <span>{promotion.environment || promotion.channel || 'publication'}</span>
                    <span>{promotion.status || '-'}</span>
                    <span>{formatDate(promotion.promoted_at || promotion.published_at || promotion.created_at)}</span>
                  </div>
                {/each}
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    </section>
  {/if}
</div>

<Modal bind:open={promoteOpen} title="Promote Package" titleIcon={ArtifactIcon} onClose={() => { promoteOpen = false; actionError = null; }}>
  <form class="action-form" onsubmit={(event) => { event.preventDefault(); handlePromote(); }}>
    <p class="muted">Promote <strong>{selectedArtifact ? artifactName(selectedArtifact) : ''}</strong> to another package repository.</p>
    <div class="form-field">
      <label for="target-repository">Target repository *</label>
      <Select id="target-repository" bind:value={promoteForm.target_repository_id} options={targetRepositoryOptions} disabled={submitting} />
    </div>
    <div class="form-field">
      <label for="promote-environment">Environment</label>
      <input id="promote-environment" bind:value={promoteForm.environment} disabled={submitting} />
    </div>
    <div class="form-field">
      <label for="promote-channel">Channel</label>
      <input id="promote-channel" bind:value={promoteForm.channel} disabled={submitting} />
    </div>
    <div class="form-field">
      <label for="promote-metadata">Metadata JSON</label>
      <Textarea id="promote-metadata" bind:value={promoteForm.metadata} rows={4} disabled={submitting} />
    </div>
    {#if actionError}<p class="error">{actionError}</p>{/if}
    <div class="form-actions">
      <LoadingButton type="button" variant="secondary" onclick={() => { promoteOpen = false; actionError = null; }} disabled={submitting}>Cancel</LoadingButton>
      <LoadingButton type="submit" variant="primary" loading={submitting}>Promote</LoadingButton>
    </div>
  </form>
</Modal>

<Modal bind:open={yankOpen} title="Yank Package" titleIcon={WarningIcon} onClose={() => { yankOpen = false; actionError = null; }}>
  <form class="action-form" onsubmit={(event) => { event.preventDefault(); handleYank(); }}>
    <p class="muted">Yank or deprecate <strong>{selectedArtifact ? artifactName(selectedArtifact) : ''}</strong>.</p>
    <label class="checkbox"><input type="checkbox" bind:checked={yankForm.deprecated} disabled={submitting} /> Deprecate instead of yank</label>
    <div class="form-field">
      <label for="yank-reason">Reason</label>
      <Textarea id="yank-reason" bind:value={yankForm.reason} rows={3} disabled={submitting} />
    </div>
    <div class="form-field">
      <label for="yank-metadata">Metadata JSON</label>
      <Textarea id="yank-metadata" bind:value={yankForm.metadata} rows={4} disabled={submitting} />
    </div>
    {#if actionError}<p class="error">{actionError}</p>{/if}
    <div class="form-actions">
      <LoadingButton type="button" variant="secondary" onclick={() => { yankOpen = false; actionError = null; }} disabled={submitting}>Cancel</LoadingButton>
      <LoadingButton type="submit" variant="danger" loading={submitting}>Yank</LoadingButton>
    </div>
  </form>
</Modal>

<style>
  .page { max-width: 1100px; }
  .back { color: var(--text-muted); text-decoration: none; font-size: 0.875rem; display: inline-block; margin-bottom: 1rem; }
  .back:hover { color: var(--text-primary); }
  .header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-bottom: 1.5rem; }
  h1 { display: inline-flex; align-items: center; gap: 0.5rem; margin: 0; }
  .info-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 1rem; margin-bottom: 1.5rem; }
  section { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 8px; padding: 1.5rem; margin-bottom: 1.5rem; }
  h2 { color: var(--text-muted); font-size: 1rem; margin: 0 0 1rem; }
  h3 { color: var(--text-primary); font-size: 0.95rem; margin: 1rem 0 0.5rem; }
  .loading, .muted { color: var(--text-muted); }
  .loading { padding: 2rem; text-align: center; }
  .error { color: var(--error); font-size: 0.875rem; }
  .details { display: grid; gap: 0.75rem; margin: 0; }
  .details div { display: grid; grid-template-columns: 150px 1fr; gap: 1rem; }
  dt { color: var(--text-muted); font-size: 0.875rem; }
  dd { margin: 0; color: var(--text-primary); }
  pre { overflow-x: auto; padding: 1rem; background: var(--hover-bg); border-radius: 6px; color: var(--text-primary); }
  .drift { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.35rem 0.75rem; color: var(--text-muted); font-size: 0.875rem; }
  .drift.ok, .drift.clean, .drift.none { color: #10b981; border-color: #10b981; }
  .drift.drifted, .drift.error { color: #ef4444; border-color: #ef4444; }
  .promotion-list, .promotion-group { display: grid; gap: 0.75rem; }
  .promotion-row { display: grid; grid-template-columns: 1fr 120px 220px; gap: 1rem; padding: 0.75rem; border: 1px solid var(--border-color); border-radius: 6px; }
  .action-form, .form-field { display: flex; flex-direction: column; gap: 0.75rem; }
  .action-form { gap: 1rem; }
  .form-field label, .checkbox { color: var(--text-primary); font-size: 0.875rem; }
  input { border: 1px solid var(--border-color); background: var(--card-bg); color: var(--text-primary); border-radius: 0.375rem; padding: 0.5rem 0.75rem; }
  .form-actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
  :global(.package-action) { border: 1px solid var(--border-color); background: var(--card-bg); color: var(--text-primary); border-radius: 0.375rem; padding: 0.25rem 0.55rem; cursor: pointer; }
  :global(.package-action.danger) { color: var(--error); }
  @media (max-width: 720px) {
    .header, .promotion-row { grid-template-columns: 1fr; display: grid; }
    .details div { grid-template-columns: 1fr; gap: 0.25rem; }
  }
</style>
