<script>
  import { page } from '$app/state';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { PaymentIcon, PendingIcon, StandardIcon } from '$lib/icons/domain-icons.js';
  import { workers, loadWorkers } from '$lib/stores';

  let worker = $state(null);
  let pricing = $state([]);
  let loading = $state(true);
  let pricingLoading = $state(false);
  let error = $state(null);
  let pricingError = $state(null);
  let loadSequence = 0;

  let pubkey = $derived(page.params.pubkey);

  $effect(() => {
    const key = pubkey;
    if (!key) return;
    void loadWorker(key);
  });

  async function loadWorker(key) {
    const sequence = ++loadSequence;
    loading = true;
    pricingLoading = true;
    error = null;
    pricingError = null;
    worker = null;
    pricing = [];

    let decodedPubkey;
    try {
      decodedPubkey = decodeURIComponent(key);
    } catch (err) {
      if (isCurrentLoad(sequence)) {
        error = err.message || 'Failed to load worker';
        loading = false;
        pricingLoading = false;
      }
      return;
    }

    try {
      await loadWorkers();
      if (!isCurrentLoad(sequence)) return;
      const loadedWorker = workers.find((candidate) => candidate.pubkey === decodedPubkey);
      if (!loadedWorker) throw new Error('Worker not found');
      worker = loadedWorker;
      pricing = normalizePricingTiers(loadedWorker.pricing || loadedWorker.prices);
    } catch (err) {
      if (!isCurrentLoad(sequence)) return;
      error = err.message || 'Failed to load worker';
      pricingError = err.message || 'Failed to load pricing tiers';
    } finally {
      if (isCurrentLoad(sequence)) {
        loading = false;
        pricingLoading = false;
      }
    }
  }

  function isCurrentLoad(sequence) {
    return sequence === loadSequence;
  }

  function normalizePricingTiers(value) {
    if (Array.isArray(value)) return value;
    if (Array.isArray(value?.pricing)) return value.pricing;
    if (value && typeof value === 'object' && ('price_per_second' in value || 'price_per_sec' in value || 'mint_url' in value)) {
      return [value];
    }
    return [];
  }

  function formatPricePerSecond(tier) {
    const price = tier.price_per_second ?? tier.price_per_sec;
    if (price === null || price === undefined || price === '') return 'Not set';
    return `${price} ${tier.unit || 'sat'}/sec`;
  }

  function formatHourlyEstimate(tier) {
    const price = Number(tier.price_per_second ?? tier.price_per_sec);
    if (!Number.isFinite(price)) return 'Not available';
    return `${price * 3600} ${tier.unit || 'sat'}/hour`;
  }

  let capabilitiesColumns = $derived([
    { key: 'name', label: 'Capability' }
  ]);

  let capabilitiesData = $derived(worker?.capabilities 
    ? worker.capabilities.map(cap => ({ name: cap }))
    : []);

  let pricingColumns = $derived([
    { key: 'mint_url', label: 'Mint URL' },
    { key: 'price_display', label: 'Price/sec' },
    { key: 'hourly_display', label: 'Hourly estimate' }
  ]);

  let pricingData = $derived(pricing.map(tier => ({
    mint_url: tier.mint_url || 'Default mint',
    price_display: formatPricePerSecond(tier),
    hourly_display: formatHourlyEstimate(tier)
  })));
</script>

<div class="page">
  <a href="/workers" class="back">← Workers</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <ErrorState message={error} />
  {:else if worker}
    <div class="header">
      <h1>
        <StandardIcon size={28} stroke={1.75} aria-hidden="true" />
        {worker.name || `Worker ${worker.pubkey?.slice(0, 12)}...`}
      </h1>
    </div>
    
    <div class="info-grid">
      <Card title="Price per Second" titleIcon={PaymentIcon} value={worker.price_per_sec ? `${worker.price_per_sec} sats/sec` : 'Not available'} />
      <Card title="Last Seen" titleIcon={PendingIcon} value={worker.last_seen?.slice(0, 19).replace('T', ' ') || 'Never'} />
      <Card title="Capabilities" titleIcon={StandardIcon} value={worker.capabilities?.length || 0} />
      <Card title="Pricing Tiers" titleIcon={PaymentIcon} value={pricingLoading ? 'Loading...' : pricingData.length} />
    </div>

    <section>
      <h2>Public Key</h2>
      <div class="pubkey-container">
        <code class="pubkey">{worker.pubkey}</code>
      </div>
    </section>

    <section>
      <h2>Pricing tiers</h2>
      {#if pricingLoading}
        <p class="loading inline">Loading pricing tiers...</p>
      {:else if pricingError}
        <ErrorState message={pricingError} />
      {:else if pricingData.length > 0}
        <Table columns={pricingColumns} data={pricingData} />
      {:else}
        <EmptyState message="No pricing tiers advertised" />
      {/if}
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
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .header h1 :global(svg) {
    display: block;
    flex-shrink: 0;
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

  .loading.inline {
    padding: 0;
    text-align: left;
  }
</style>
