<script>
  import Table from '$lib/components/Table.svelte';
  import { events } from '$lib/stores';

  $: columns = [
    { key: 'time', label: 'Time', render: (r) => r.time || '-' },
    { key: 'type', label: 'Event Type' },
    { key: 'entity_id', label: 'Entity ID', render: (r) => r.entity_id ? `<code>${r.entity_id}</code>` : '-' },
    { key: 'data', label: 'Data', render: (r) => r.data ? `<code>${JSON.stringify(r.data).slice(0, 50)}...</code>` : '-' }
  ];
</script>

<div class="page">
  <div class="header">
    <h1>Live Events</h1>
    <span class="status">🟢 Connected via SSE</span>
  </div>

  <p class="hint">Events are streamed in real-time from the server.</p>

  <Table columns={columns} data={$events} />
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1rem; }
  .status { color: var(--success); font-size: 0.875rem; }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-bottom: 1.5rem; }
</style>
