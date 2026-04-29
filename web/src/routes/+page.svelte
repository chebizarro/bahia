<script>
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import { services, environments, states, workers, driftedStates, events, loading } from '$lib/stores';

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
    { key: 'type', label: 'Event' },
    { key: 'entity_id', label: 'Entity', render: (r) => r.entity_id ? `${r.entity_id.slice(0, 8)}...` : '-' }
  ];
</script>

<div class="dashboard">
  <h1>Dashboard</h1>
  
  <div class="stats">
    <Card title="Services" value={$services.length} subtitle="Total registered" />
    <Card title="Environments" value={$environments.length} subtitle="Configured" />
    <Card 
      title="Drifted" 
      value={$driftedStates.length} 
      subtitle="Need attention"
      status={$driftedStates.length > 0 ? 'error' : 'success'}
    />
    <Card title="Workers" value={$workers.length} subtitle="Available" />
  </div>

  <div class="sections">
    <section>
      <h2>Environment States</h2>
      <Table columns={stateColumns} data={$states.slice(0, 10)} />
    </section>

    <section>
      <h2>Recent Events</h2>
      <Table columns={eventColumns} data={$events.slice(0, 10)} />
      {#if $events.length === 0}
        <p class="hint">Events will appear here in real-time via SSE</p>
      {/if}
    </section>
  </div>
</div>

<style>
  .dashboard h1 {
    margin-bottom: 1.5rem;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
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
  :global(code) {
    font-family: 'SF Mono', Monaco, monospace;
    font-size: 0.8em;
  }
</style>
