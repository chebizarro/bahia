<script>
  import { page } from '$app/state';
  import Table from '$lib/components/Table.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { StandardIcon } from '$lib/icons/domain-icons.js';
  import { workers, loadWorkers } from '$lib/stores';
  import { inferWorkerStatus } from '../list-utils.js';

  let worker = $state(null);
  let loading = $state(true);
  let error = $state(null);
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
    error = null;
    worker = null;

    let decodedPubkey;
    try {
      decodedPubkey = decodeURIComponent(key);
    } catch (err) {
      if (isCurrentLoad(sequence)) {
        error = err.message || 'Failed to load worker';
        loading = false;
      }
      return;
    }

    try {
      await loadWorkers();
      if (!isCurrentLoad(sequence)) return;
      const loadedWorker = workers.find((candidate) => candidate.pubkey === decodedPubkey);
      if (!loadedWorker) throw new Error('Worker not found');
      worker = loadedWorker;
    } catch (err) {
      if (!isCurrentLoad(sequence)) return;
      error = err.message || 'Failed to load worker';
    } finally {
      if (isCurrentLoad(sequence)) {
        loading = false;
      }
    }
  }

  function isCurrentLoad(sequence) {
    return sequence === loadSequence;
  }

  function formatTimestamp(value) {
    if (!value) return 'Not advertised';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString();
  }

  function formatDuration(seconds) {
    if (!seconds) return 'Not advertised';
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    return `${Math.round(seconds / 3600)}h`;
  }

  function formatList(values) {
    return (values || []).filter(Boolean).join(', ') || 'Not advertised';
  }

  function formatRuntimeTarget(target) {
    if (!target) return 'Not advertised';
    const parts = [target.type, target.endpoint_ref, target.kube_namespace, target.compose_dir, target.public_base_url].filter(Boolean);
    return parts.join(' · ') || 'Not advertised';
  }

  function formatPricePerSecond(tier) {
    const price = tier.price_per_second;
    if (price === null || price === undefined || price === '') return 'Not advertised';
    return `${price} ${tier.unit || 'sat'}/sec`;
  }

  function formatHourlyEstimate(tier) {
    const price = Number(tier.price_per_second);
    if (!Number.isFinite(price)) return 'Not advertised';
    return `${price * 3600} ${tier.unit || 'sat'}/hour`;
  }

  function hasRuntimeTarget(target) {
    return Boolean(target && Object.values(target).some((value) => value !== null && value !== undefined && value !== ''));
  }

  let livenessStatus = $derived(worker ? inferWorkerStatus(worker) : 'offline');
  let mlCapabilities = $derived(worker?.ml_capabilities || {});
  let resources = $derived(worker?.resources || null);

  let overviewCards = $derived(worker ? [
    { label: 'Liveness', value: livenessStatus },
    { label: 'Queue Depth', value: worker.current_queue_depth ?? 0 },
    { label: 'Max Concurrent Jobs', value: worker.max_concurrent_jobs ?? 0 },
    { label: 'Last Advertisement', value: formatTimestamp(worker.last_advertisement_at) }
  ] : []);

  let capacityRows = $derived(worker ? [
    { label: 'Architecture', value: worker.architecture || 'Not advertised' },
    { label: 'Current queue depth', value: worker.current_queue_depth ?? 0 },
    { label: 'Max concurrent jobs', value: worker.max_concurrent_jobs ?? 0 },
    { label: 'Minimum duration', value: formatDuration(worker.min_duration_secs) },
    { label: 'Maximum duration', value: formatDuration(worker.max_duration_secs) },
    { label: 'Geohash', value: worker.geohash || 'Not advertised' }
  ] : []);

  let capabilityRows = $derived([
    { label: 'Task types', value: formatList(mlCapabilities.tasks) },
    { label: 'Runtimes', value: formatList(mlCapabilities.runtimes) },
    { label: 'Artifact formats', value: formatList(mlCapabilities.artifact_formats) },
    { label: 'Accelerators', value: formatList(mlCapabilities.accelerators) },
    { label: 'Toolchains', value: formatList(mlCapabilities.toolchains) },
    { label: 'Cached artifacts', value: formatList(mlCapabilities.cached_artifacts) }
  ]);

  let resourceRows = $derived(resources ? [
    { label: 'CPU cores', value: resources.cpu_cores || 'Not advertised' },
    { label: 'Memory', value: resources.memory_gb ? `${resources.memory_gb} GB` : 'Not advertised' },
    { label: 'Disk', value: resources.disk_gb ? `${resources.disk_gb} GB` : 'Not advertised' }
  ] : []);

  let acceleratorColumns = $derived([
    { key: 'vendor', label: 'Vendor' },
    { key: 'model', label: 'Model' },
    { key: 'count', label: 'Count' },
    { key: 'memory', label: 'Memory' },
    { key: 'driver', label: 'Driver' }
  ]);

  let acceleratorData = $derived((worker?.accelerators || []).map((accelerator) => ({
    vendor: accelerator.vendor || '-',
    model: accelerator.model || '-',
    count: accelerator.count || 1,
    memory: accelerator.memory_gb ? `${accelerator.memory_gb} GB` : '-',
    driver: accelerator.driver || '-'
  })));

  let softwareColumns = $derived([
    { key: 'name', label: 'Software' },
    { key: 'version', label: 'Version' },
    { key: 'path', label: 'Path' }
  ]);

  let softwareData = $derived((worker?.software || []).map((entry) => ({
    name: entry.name || '-',
    version: entry.version || '-',
    path: entry.path || '-'
  })));

  let pricingColumns = $derived([
    { key: 'mint_url', label: 'Mint URL' },
    { key: 'price_display', label: 'Price/sec' },
    { key: 'hourly_display', label: 'Hourly estimate' }
  ]);

  let pricingData = $derived((worker?.pricing || []).map((tier) => ({
    mint_url: tier.mint_url || 'Default mint',
    price_display: formatPricePerSecond(tier),
    hourly_display: formatHourlyEstimate(tier)
  })));

  let relayRows = $derived((worker?.preferred_relays || []).map((relay) => ({ relay })));
  let relayColumns = $derived([{ key: 'relay', label: 'Preferred Relay' }]);

  let timestampRows = $derived(worker ? [
    { label: 'Created', value: formatTimestamp(worker.created_at) },
    { label: 'Updated', value: formatTimestamp(worker.updated_at) },
    { label: 'Last advertisement', value: formatTimestamp(worker.last_advertisement_at) }
  ] : []);
</script>

<div class="page">
  <a href="/workers" class="back">← Workers</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <ErrorState message={error} />
  {:else if worker}
    <div class="header">
      <div>
        <h1>
          <StandardIcon size={28} strokeWidth={1.75} ariaHidden="true" />
          {worker.name || `Worker ${worker.pubkey?.slice(0, 12)}...`}
        </h1>
        {#if worker.description}
          <p class="description">{worker.description}</p>
        {/if}
      </div>
      <span class="worker-status status-{livenessStatus}"><span class="status-dot" aria-hidden="true"></span>{livenessStatus}</span>
    </div>

    <div class="info-grid">
      {#each overviewCards as card}
        <div class="metric-card">
          <span class="metric-label">{card.label}</span>
          <strong>{card.value}</strong>
        </div>
      {/each}
    </div>

    <section>
      <h2>Public Key</h2>
      <div class="pubkey-container">
        <code class="pubkey">{worker.pubkey}</code>
      </div>
    </section>

    <section>
      <h2>Execution Capacity</h2>
      <dl class="detail-list">
        {#each capacityRows as row}
          <div>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        {/each}
      </dl>
    </section>

    <section>
      <h2>Inference Placement Capabilities</h2>
      <dl class="detail-list">
        {#each capabilityRows as row}
          <div>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        {/each}
      </dl>
    </section>

    <section>
      <h2>Resources</h2>
      {#if resourceRows.length > 0}
        <dl class="detail-list">
          {#each resourceRows as row}
            <div>
              <dt>{row.label}</dt>
              <dd>{row.value}</dd>
            </div>
          {/each}
        </dl>
      {:else}
        <EmptyState message="No host resources advertised" />
      {/if}
    </section>

    <section>
      <h2>Accelerators</h2>
      {#if acceleratorData.length > 0}
        <Table columns={acceleratorColumns} data={acceleratorData} />
      {:else}
        <EmptyState message="No accelerators advertised" />
      {/if}
    </section>

    <section>
      <h2>Software</h2>
      {#if softwareData.length > 0}
        <Table columns={softwareColumns} data={softwareData} />
      {:else}
        <EmptyState message="No software entries advertised" />
      {/if}
    </section>

    <section>
      <h2>Runtime Target</h2>
      {#if hasRuntimeTarget(worker.runtime_target)}
        <p class="runtime-target">{formatRuntimeTarget(worker.runtime_target)}</p>
      {:else}
        <EmptyState message="No runtime target advertised" />
      {/if}
    </section>

    <section>
      <h2>Pricing Tiers</h2>
      {#if pricingData.length > 0}
        <Table columns={pricingColumns} data={pricingData} />
      {:else}
        <EmptyState message="No pricing tiers advertised" />
      {/if}
    </section>

    <section>
      <h2>Preferred Relays</h2>
      {#if relayRows.length > 0}
        <Table columns={relayColumns} data={relayRows} />
      {:else}
        <EmptyState message="No preferred relays advertised" />
      {/if}
    </section>

    <section>
      <h2>Timestamps</h2>
      <dl class="detail-list">
        {#each timestampRows as row}
          <div>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        {/each}
      </dl>
    </section>
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
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
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
  .description {
    margin: 0.5rem 0 0;
    color: var(--text-muted);
  }

  .info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }
  .metric-card {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 1rem;
  }
  .metric-label {
    color: var(--text-muted);
    font-size: 0.8rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
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

  .pubkey-container,
  .runtime-target {
    background: var(--hover-bg);
    padding: 1rem;
    border-radius: 4px;
    overflow-x: auto;
  }
  .runtime-target { margin: 0; }

  .pubkey {
    font-family: 'Monaco', 'Courier New', monospace;
    font-size: 0.875rem;
    word-break: break-all;
  }

  .detail-list {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 1rem;
    margin: 0;
  }
  .detail-list div {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .detail-list dt {
    color: var(--text-muted);
    font-size: 0.8rem;
    text-transform: uppercase;
  }
  .detail-list dd {
    margin: 0;
  }

  .worker-status {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.35rem 0.6rem;
    border-radius: 999px;
    text-transform: capitalize;
    background: rgba(100, 100, 120, 0.2);
    white-space: nowrap;
  }
  .status-dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 999px;
    display: inline-block;
  }
  .status-online .status-dot { background: #22c55e; }
  .status-stale .status-dot { background: #f59e0b; }
  .status-offline .status-dot { background: #ef4444; }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  @media (max-width: 700px) {
    .header { flex-direction: column; }
  }
</style>
