<script>
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { api } from '$lib/api/client.js';

  let loading = true;
  let artifacts = [];
  let services = [];
  let serviceMap = {};

  onMount(async () => {
    try {
      // First load all services
      services = await api.listServices();
      
      // Create service lookup map
      serviceMap = services.reduce((map, service) => {
        map[service.id] = service.name;
        return map;
      }, {});

      // Fetch artifacts for each service
      const artifactPromises = services.map(service => 
        api.listArtifacts(service.id).catch(() => [])
      );
      
      const artifactsByService = await Promise.all(artifactPromises);
      
      // Flatten all artifacts into one array
      artifacts = artifactsByService.flat();
    } catch (err) {
      console.error('Failed to load artifacts:', err);
    } finally {
      loading = false;
    }
  });

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

  $: columns = [
    { 
      key: 'image_tag', 
      label: 'Name',
      render: (r) => r.image_tag || r.image_digest?.slice(0, 12) || '-'
    },
    { 
      key: 'service_id', 
      label: 'Service',
      render: (r) => serviceMap[r.service_id] || r.service_id?.slice(0, 8) || '-'
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
  ];
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Artifacts</h1>
      <span class="count">{artifacts.length} artifacts</span>
    </div>
  </div>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if artifacts.length === 0}
    <EmptyState
      icon="📦"
      title="No artifacts yet"
      message="Artifacts will appear here once services have builds"
    />
  {:else}
    <Table {columns} data={artifacts} onRowClick={(row) => goto(`/artifacts/${row.id}`)} />
  {/if}
</div>

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
</style>
