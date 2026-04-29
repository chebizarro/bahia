<script>
  import { page } from '$app/stores';
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { api } from '$lib/api/client.js';

  let worker = null;
  let loading = true;
  let error = null;

  $: pubkey = $page.params.pubkey;

  onMount(async () => {
    try {
      const decodedPubkey = decodeURIComponent(pubkey);
      worker = await api.getWorker(decodedPubkey);
    } catch (err) {
      error = err.message || 'Failed to load worker';
    } finally {
      loading = false;
    }
  });

  $: capabilitiesColumns = [
    { key: 'name', label: 'Capability' }
  ];

  $: capabilitiesData = worker?.capabilities 
    ? worker.capabilities.map(cap => ({ name: cap }))
    : [];
</script>

<div class="page">
  <a href="/workers" class="back">← Workers</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <ErrorState message={error} />
  {:else if worker}
    <div class="header">
      <h1>{worker.name || `Worker ${worker.pubkey?.slice(0, 12)}...`}</h1>
    </div>
    
    <div class="info-grid">
      <Card title="Price per Second" value={worker.price_per_sec ? `${worker.price_per_sec} sats/sec` : 'Not available'} />
      <Card title="Last Seen" value={worker.last_seen?.slice(0, 19).replace('T', ' ') || 'Never'} />
      <Card title="Capabilities" value={worker.capabilities?.length || 0} />
    </div>

    <section>
      <h2>Public Key</h2>
      <div class="pubkey-container">
        <code class="pubkey">{worker.pubkey}</code>
      </div>
    </section>

    {#if capabilitiesData.length > 0}
      <section>
        <h2>Capabilities ({capabilitiesData.length})</h2>
        <Table columns={capabilitiesColumns} data={capabilitiesData} />
      </section>
    {:else}
      <section>
        <h2>Capabilities</h2>
        <EmptyState message="No capabilities registered" />
      </section>
    {/if}

    {#if worker.metadata}
      <section>
        <h2>Metadata</h2>
        <pre class="metadata">{JSON.stringify(worker.metadata, null, 2)}</pre>
      </section>
    {/if}
  {:else}
    <ErrorState message="Worker not found" />
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
  
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .header h1 {
    margin: 0;
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

  .pubkey-container {
    background: var(--hover-bg);
    padding: 1rem;
    border-radius: 4px;
    overflow-x: auto;
  }

  .pubkey {
    font-family: 'Monaco', 'Courier New', monospace;
    font-size: 0.875rem;
    word-break: break-all;
  }

  .metadata {
    background: var(--hover-bg);
    padding: 1rem;
    border-radius: 4px;
    overflow-x: auto;
    font-size: 0.875rem;
    margin: 0;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }
</style>
