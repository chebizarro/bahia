<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import {
    ArtifactIcon,
    BlossomIcon,
    ServiceIcon,
    SuccessIcon,
    ErrorIcon,
    WarningIcon,
    blossomContentTypeIcon
  } from '$lib/icons/domain-icons.js';
  import { artifacts as registryArtifacts, services, loadArtifacts, loadServices } from '$lib/stores';
  import { api } from '$lib/api/client.js';
  import { authState } from '$lib/stores/auth.js';

  // Tab state
  let activeTab = $state('registry');

  // Registry artifacts state
  let registryLoading = $state(true);
  let artifacts = $state([]);
  let serviceList = [];
  let serviceMap = $state({});

  // Blossom state
  let blossomLoading = $state(false);
  let blossomBlobs = $state([]);
  let blossomServers = $state([]);
  let blossomHealth = $state({});
  let blossomError = $state(null);
  let pubkeyFilter = $state('');
  let typeFilter = $state('');
  // Default owner pubkey to the signed-in user's pubkey
  $effect(() => {
    if (authState.pubkey && !pubkeyFilter) {
      pubkeyFilter = authState.pubkey;
    }
  });

  $effect(() => {
    void loadRegistryArtifacts();
  });

  async function loadRegistryArtifacts() {
    // Load registry artifacts
    try {
      await Promise.all([loadServices(), loadArtifacts()]);
      serviceList = services;
      serviceMap = serviceList.reduce((map, service) => {
        map[service.id] = service.name;
        return map;
      }, {});
      artifacts = registryArtifacts;
    } catch (err) {
      console.error('Failed to load artifacts:', err);
    } finally {
      registryLoading = false;
    }
  }

  async function loadBlossomBlobs() {
    if (!api) {
      blossomError = 'API client not available';
      return;
    }
    blossomLoading = true;
    blossomError = null;

    try {
      // Use allSettled so one failing endpoint doesn't abort the others
      const [serversResult, healthResult, blobsResult] = await Promise.allSettled([
        api.getBlossomServers(),
        api.checkBlossomHealth(),
        api.listBlossomBlobs(pubkeyFilter.trim() || null)
      ]);

      blossomServers = serversResult.status === 'fulfilled' && Array.isArray(serversResult.value)
        ? serversResult.value : [];
      blossomHealth = healthResult.status === 'fulfilled' && healthResult.value && typeof healthResult.value === 'object'
        ? healthResult.value : {};
      blossomBlobs = blobsResult.status === 'fulfilled' && Array.isArray(blobsResult.value)
        ? blobsResult.value : [];

      // Surface a friendly error if all three failed (likely not configured)
      const allFailed = [serversResult, healthResult, blobsResult].every(r => r.status === 'rejected');
      if (allFailed) {
        const firstErr = serversResult.reason?.message || '';
        if (firstErr.includes('500') || firstErr.includes('Internal Server Error')) {
          blossomError = 'Blossom storage is not configured on this server. Enable and configure Blossom in your Bahia server settings.';
        } else {
          blossomError = firstErr || 'Failed to contact Blossom storage endpoints';
        }
      } else if (blobsResult.status === 'rejected') {
        const err = blobsResult.reason?.message || '';
        blossomError = err.includes('500')
          ? 'Blossom blob listing failed — storage may not be configured for this pubkey'
          : err || 'Failed to list Blossom blobs';
      }
    } catch (err) {
      console.error('Failed to load Blossom blobs:', err);
      blossomError = err.message || 'Failed to load Blossom blobs';
    } finally {
      blossomLoading = false;
    }
  }

  function handleTabChange(tab) {
    activeTab = tab;
    if (tab === 'blossom' && blossomBlobs.length === 0) {
      loadBlossomBlobs();
    }
  }

  function handlePubkeySearch() {
    loadBlossomBlobs();
  }

  function getSBOMBadge(artifact) {
    if (artifact.sbom_url) {
      return { variant: 'success', text: 'Verified' };
    }
    return { variant: 'default', text: 'None' };
  }

  function getSignatureBadge(artifact) {
    if (artifact.signature_ref) {
      return { variant: 'success', text: 'Signed' };
    }
    return { variant: 'default', text: 'None' };
  }

  function formatDate(dateStr) {
    if (!dateStr) return '-';
    const date = new Date(dateStr);
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  function formatSize(bytes) {
    if (!bytes) return '-';
    const units = ['B', 'KB', 'MB', 'GB'];
    let size = bytes;
    let unitIndex = 0;
    while (size >= 1024 && unitIndex < units.length - 1) {
      size /= 1024;
      unitIndex++;
    }
    return `${size.toFixed(1)} ${units[unitIndex]}`;
  }

  // Filter blobs by type
  let filteredBlobs = $derived(blossomBlobs.filter(blob => {
    if (!typeFilter) return true;
    return blob.type?.toLowerCase().includes(typeFilter.toLowerCase());
  }));

  // Unique content types for filter dropdown
  let uniqueTypes = $derived([...new Set(blossomBlobs.map(b => b.type).filter(Boolean))].sort());

  let registryColumns = $derived([
    { 
      key: 'image_tag', 
      label: 'Name',
      icon: ArtifactIcon,
      text: (r) => r.image_tag || r.image_digest?.slice(0, 12) || '-'
    },
    { 
      key: 'service_id', 
      label: 'Service',
      icon: ServiceIcon,
      text: (r) => serviceMap[r.service_id] || r.service_id?.slice(0, 8) || '-'
    },
    { 
      key: 'image_digest', 
      label: 'Version',
      render: (r) => `<code>${r.image_digest?.slice(7, 19) || '-'}...</code>`
    },
    { 
      key: 'created_at', 
      label: 'Created',
      render: (r) => formatDate(r.created_at)
    },
    { 
      key: 'sbom_status', 
      label: 'SBOM Status',
      render: (r) => {
        const badge = getSBOMBadge(r);
        return `<span class="badge-cell ${badge.variant}">${badge.text}</span>`;
      }
    },
    { 
      key: 'signature_status', 
      label: 'Signature Status',
      render: (r) => {
        const badge = getSignatureBadge(r);
        return `<span class="badge-cell ${badge.variant}">${badge.text}</span>`;
      }
    }
  ]);

  let blossomColumns = $derived([
    {
      key: 'type',
      label: 'Type',
      icon: (r) => blossomContentTypeIcon(r.type),
      text: (r) => r.type || 'unknown'
    },
    {
      key: 'sha256',
      label: 'SHA256',
      render: (r) => `<code class="hash">${r.sha256?.slice(0, 16) || '-'}...</code>`
    },
    {
      key: 'size',
      label: 'Size',
      render: (r) => formatSize(r.size)
    },
    {
      key: 'uploaded',
      label: 'Uploaded',
      render: (r) => formatDate(r.uploaded)
    },
    {
      key: 'url',
      label: 'Actions',
      render: (r) => `<a href="${r.url}" target="_blank" class="download-link">Download ↗</a>`
    }
  ]);
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>
        <ArtifactIcon size={28} stroke={1.75} aria-hidden="true" />
        Artifacts
      </h1>
      <span class="count">
        {#if activeTab === 'registry'}
          {artifacts.length} registry artifacts
        {:else}
          {filteredBlobs.length} blossom blobs
        {/if}
      </span>
    </div>
  </div>

  <!-- Tabs -->
  <div class="tabs">
    <button
      class="tab"
      class:active={activeTab === 'registry'}
      onclick={() => handleTabChange('registry')}
    >
      <ArtifactIcon size={18} stroke={1.75} aria-hidden="true" />
      Registry
    </button>
    <button
      class="tab"
      class:active={activeTab === 'blossom'}
      onclick={() => handleTabChange('blossom')}
    >
      <BlossomIcon size={18} stroke={1.75} aria-hidden="true" />
      Blossom
    </button>
  </div>

  <!-- Registry Tab -->
  {#if activeTab === 'registry'}
    {#if registryLoading}
      <p class="loading">Loading registry artifacts...</p>
    {:else if artifacts.length === 0}
      <EmptyState
        iconComponent={ArtifactIcon}
        title="No artifacts yet"
        message="Artifacts will appear here once services have builds"
      />
    {:else}
      <Table columns={registryColumns} data={artifacts} onRowClick={(row) => goto(`/artifacts/${row.id}`)} />
    {/if}
  {/if}

  <!-- Blossom Tab -->
  {#if activeTab === 'blossom'}
    <!-- Server Status -->
    {#if blossomServers.length > 0}
      <div class="server-status">
        <span class="server-label">Servers:</span>
        {#each blossomServers as server}
          {@const serverHostname = new URL(server).hostname}
          {@const serverHealth = blossomHealth[server]}
          <span
            class="server-badge"
            class:healthy={serverHealth === 'ok'}
            class:unhealthy={serverHealth && serverHealth !== 'ok'}
            aria-label={serverHealth === 'ok' ? `${serverHostname} healthy` : serverHealth ? `${serverHostname} unhealthy` : serverHostname}
          >
            {serverHostname}
            {#if serverHealth === 'ok'}
              <SuccessIcon size={14} stroke={2} aria-hidden="true" />
            {:else if serverHealth}
              <ErrorIcon size={14} stroke={2} aria-hidden="true" />
            {/if}
          </span>
        {/each}
      </div>
    {/if}

    <!-- Filters -->
    <div class="filters">
      <div class="filter-group">
        <label for="pubkey-filter">Owner Pubkey:</label>
        <input
          type="text"
          id="pubkey-filter"
          bind:value={pubkeyFilter}
          placeholder="Enter hex pubkey to filter..."
          class="filter-input"
        />
        <button class="filter-btn" onclick={handlePubkeySearch}>Search</button>
      </div>
      
      {#if uniqueTypes.length > 0}
        <div class="filter-group">
          <label for="type-filter">Content Type:</label>
          <select id="type-filter" bind:value={typeFilter} class="filter-select">
            <option value="">All types</option>
            {#each uniqueTypes as type}
              <option value={type}>{type}</option>
            {/each}
          </select>
        </div>
      {/if}
    </div>

    {#if blossomLoading}
      <p class="loading">Loading Blossom blobs...</p>
    {:else if blossomError}
      <EmptyState
        iconComponent={WarningIcon}
        title="Error loading blobs"
        message={blossomError}
      />
    {:else if blossomServers.length === 0}
      <EmptyState
        iconComponent={BlossomIcon}
        title="No Blossom servers configured"
        message="Configure Blossom servers in your deployment to browse artifacts"
      />
    {:else if filteredBlobs.length === 0}
      <EmptyState
        iconComponent={BlossomIcon}
        title="No blobs found"
        message={pubkeyFilter ? "No blobs found for this pubkey" : "No blobs stored on Blossom servers yet"}
      />
    {:else}
      <Table columns={blossomColumns} data={filteredBlobs} />
    {/if}
  {/if}
</div>

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
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

  /* Tabs */
  .tabs {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border);
    margin-bottom: 1.5rem;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 1.5rem;
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    font-size: 0.9rem;
    cursor: pointer;
    transition: all 0.2s;
  }
  .tab:hover {
    color: var(--text);
    background: var(--bg-hover);
  }
  .tab.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }

  /* Server Status */
  .server-status {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 1rem;
    padding: 0.5rem;
    background: var(--bg-secondary);
    border-radius: 6px;
    font-size: 0.85rem;
  }
  .server-label {
    color: var(--text-muted);
  }
  .server-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    background: var(--bg);
    border: 1px solid var(--border);
  }
  .server-badge.healthy {
    background: #065f46;
    border-color: #059669;
    color: #6ee7b7;
  }
  .server-badge.unhealthy {
    background: #7f1d1d;
    border-color: #dc2626;
    color: #fca5a5;
  }

  /* Filters */
  .filters {
    display: flex;
    gap: 1.5rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }
  .filter-group {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .filter-group label {
    font-size: 0.85rem;
    color: var(--text-muted);
  }
  .filter-input {
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    color: var(--text);
    font-size: 0.85rem;
    width: 280px;
  }
  .filter-input:focus {
    outline: none;
    border-color: var(--primary);
  }
  .filter-select {
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    color: var(--text);
    font-size: 0.85rem;
    min-width: 150px;
  }
  .filter-btn {
    padding: 0.5rem 1rem;
    background: var(--primary);
    color: white;
    border: none;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.85rem;
  }
  .filter-btn:hover {
    opacity: 0.9;
  }

  /* Table cell styles */
  :global(.badge-cell) {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-weight: 500;
    font-size: 0.75rem;
  }
  :global(.badge-cell.success) {
    background: #065f46;
    color: #6ee7b7;
  }
  :global(.badge-cell.warning) {
    background: #78350f;
    color: #fcd34d;
  }
  :global(.badge-cell.default) {
    background: #374151;
    color: #d1d5db;
  }

  :global(.hash) {
    font-size: 0.8rem;
    background: var(--bg-secondary);
    padding: 0.2rem 0.4rem;
    border-radius: 3px;
  }
  :global(.download-link) {
    color: var(--primary);
    text-decoration: none;
    font-size: 0.85rem;
  }
  :global(.download-link:hover) {
    text-decoration: underline;
  }
</style>
