<script>
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import { api } from '$lib/api/client.js';
  import { services, environments, states, workers, driftedStates, events, loading } from '$lib/stores';

  // Pending deployments state
  let pendingDeployments = [];
  let pendingLoading = false;
  let pendingError = null;

  $: stateColumns = [
    { key: 'service_id', label: 'Service', render: (r) => `<code>${r.service_id?.slice(0, 8)}...</code>` },
    { key: 'environment_id', label: 'Environment', render: (r) => `<code>${r.environment_id?.slice(0, 8)}...</code>` },
    { key: 'drift_status', label: 'Drift', render: (r) => {
      const variant = r.drift_status === 'in_sync' ? 'success' : r.drift_status === 'drifted' ? 'error' : 'default';
      return `<span class="badge-${variant}">${r.drift_status}</span>`;
    }}
  ];

  $: eventColumns = [
    { key: 'time', label: 'Time', render: (r) => r.time?.slice(11, 19) || '-' },
    { key: 'type', label: 'Event', render: (r) => {
      const type = r.type || '';
      const variant = eventBadgeVariant(type);
      const escaped = escapeHtml(type);
      return `<span class="badge ${variant}">${escaped}</span>`;
    }},
    { key: 'entity_id', label: 'Entity', render: (r) => r.entity_id ? `${r.entity_id.slice(0, 8)}...` : '-' }
  ];

  // Helper: determine badge variant for event type
  function eventBadgeVariant(type) {
    if (type.startsWith('deployment.')) return 'info';
    if (type.startsWith('drift.')) return 'warning';
    if (type.startsWith('service.')) return 'success';
    if (type.startsWith('worker.')) return 'default';
    return 'default';
  }

  // Helper: escape HTML to prevent XSS
  function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  // Load pending deployments
  async function loadPendingDeployments() {
    if (!api) {
      pendingDeployments = [];
      return;
    }

    pendingLoading = true;
    pendingError = null;

    try {
      // Load services and environments
      const [servicesList, envsList] = await Promise.all([
        api.listServices().catch(() => []),
        api.listEnvironments().catch(() => [])
      ]);

      // Fetch intents for all service/environment pairs
      const intentPromises = [];
      const intentMap = new Map(); // dedupe by intent.id

      for (const service of servicesList) {
        for (const env of envsList) {
          intentPromises.push(
            api.listIntents(service.id, env.id)
              .then(intents => {
                if (Array.isArray(intents)) {
                  intents.forEach(intent => {
                    if (intent?.id) {
                      intentMap.set(intent.id, intent);
                    }
                  });
                }
              })
              .catch(err => {
                // Silently handle per-pair failures
                console.debug(`Failed to load intents for ${service.id}/${env.id}:`, err);
              })
          );
        }
      }

      await Promise.all(intentPromises);

      // Filter for pending approvals
      pendingDeployments = Array.from(intentMap.values()).filter(intent => {
        const status = String(intent.approval_status || '').toLowerCase();
        return status === 'pending';
      });

    } catch (err) {
      console.error('Failed to load pending deployments:', err);
      pendingError = err.message;
      pendingDeployments = [];
    } finally {
      pendingLoading = false;
    }
  }

  onMount(() => {
    loadPendingDeployments();
  });

  $: pendingCount = pendingDeployments.length;
  $: pendingSubtitle = pendingError
    ? 'Unable to load'
    : pendingCount > 0
      ? 'Needs review'
      : 'All clear';
</script>

<div class="dashboard">
  <h1>Dashboard</h1>
  
  <div class="quick-actions">
    <a href="/services" class="action-link">+ Create Service</a>
    <a href="/deployments" class="action-link">View Deployments</a>
  </div>

  <div class="stats">
    <Card title="Services" value={$services.length} subtitle="Total registered" />
    <Card title="Environments" value={$environments.length} subtitle="Configured" />
    <Card title="Workers" value={$workers.length} subtitle="Available" />
    <Card 
      title="Drifted" 
      value={$driftedStates.length} 
      subtitle="Need attention"
      status={$driftedStates.length > 0 ? 'error' : 'success'}
    />
    <a href="/deployments/pending" class="card-link">
      <Card
        title="Pending Approvals"
        value={pendingLoading ? '...' : pendingCount}
        subtitle={pendingSubtitle}
        status={pendingError ? 'error' : pendingCount > 0 ? 'warning' : 'success'}
      />
    </a>
  </div>

  <div class="sections">
    <section>
      <h2>Environment States</h2>
      <Table columns={stateColumns} data={$states.slice(0, 10)} />
    </section>

    <section>
      <h2>Recent Activity</h2>
      <Table columns={eventColumns} data={$events.slice(0, 10)} />
      {#if $events.length === 0}
        <p class="hint">Events will appear here in real-time via SSE</p>
      {/if}
    </section>
  </div>
</div>

<style>
  .dashboard h1 {
    margin-bottom: 1rem;
  }
  .quick-actions {
    display: flex;
    gap: 0.75rem;
    margin-bottom: 1.5rem;
  }
  .action-link {
    padding: 0.5rem 1rem;
    background: var(--primary);
    color: white;
    border-radius: 6px;
    text-decoration: none;
    font-size: 0.875rem;
    font-weight: 500;
    transition: opacity 0.2s;
  }
  .action-link:hover {
    opacity: 0.9;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }
  .card-link {
    text-decoration: none;
    color: inherit;
  }
  .sections {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
    gap: 2rem;
  }
  section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }
  section h2 {
    font-size: 1rem;
    margin-bottom: 1rem;
    color: var(--text-muted);
  }
  .hint {
    color: var(--text-muted);
    font-size: 0.875rem;
    text-align: center;
    padding: 2rem;
  }
  :global(.badge-success) {
    background: var(--success);
    color: #fff;
    padding: 0.125rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    opacity: 0.9;
  }
  :global(.badge-error) {
    background: var(--error);
    color: #fff;
    padding: 0.125rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    opacity: 0.9;
  }
  :global(.badge-default) {
    background: var(--text-muted);
    color: var(--bg);
    padding: 0.125rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    opacity: 0.9;
  }
  :global(.badge) {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-weight: 500;
    font-size: 0.75rem;
  }
  :global(.badge.info) {
    background: #1e3a5f;
    color: #93c5fd;
  }
  :global(.badge.warning) {
    background: #78350f;
    color: #fcd34d;
  }
  :global(.badge.success) {
    background: #065f46;
    color: #6ee7b7;
  }
  :global(code) {
    font-family: 'SF Mono', Monaco, monospace;
    font-size: 0.8em;
  }
</style>
