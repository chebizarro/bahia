<script>
  import { page } from '$app/stores';
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import { api } from '$lib/api/client.js';

  let service = null;
  let builds = [];
  let artifacts = [];
  let secrets = [];
  let loading = true;
  let error = null;

  $: serviceId = $page.params.id;

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
</script>

<div class="page">
  <a href="/services" class="back">← Services</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <p class="error">Error: {error}</p>
  {:else if service}
    <h1>{service.name}</h1>
    
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
  h1 { margin-bottom: 1.5rem; }
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
</style>
