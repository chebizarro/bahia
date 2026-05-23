<script>
  let { meshState = {}, nodes = [] } = $props();

  let filters = $state({ health: '', worker: '', capability: '', projection: '' });

  const filteredNodes = $derived(nodes.filter((node) => matchesFilters(node)));
  const healthOptions = $derived(uniqueValues(nodes.map((node) => node.health).filter(Boolean)));
  const workerOptions = $derived(uniqueValues(nodes.map((node) => workerLabel(node)).filter(Boolean)));
  const capabilityOptions = $derived(uniqueValues(nodes.flatMap((node) => capabilityValues(node))));
  const projectionOptions = $derived(uniqueValues(nodes.map((node) => node.projectionStatus).filter(Boolean)));

  function valueOrDash(value) {
    return value === null || value === undefined || value === '' ? '—' : value;
  }

  function uniqueValues(values) {
    return [...new Set(values.map(String).filter(Boolean))].sort();
  }

  function healthTone(value) {
    const normalized = String(value || '').toLowerCase();
    if (['healthy', 'ok', 'passing'].includes(normalized)) return 'healthy';
    if (['degraded', 'warning', 'stale'].includes(normalized)) return 'warning';
    if (['unhealthy', 'failed', 'error', 'critical', 'down'].includes(normalized)) return 'critical';
    return 'unknown';
  }

  function workerLabel(node) {
    return node?.name || node?.npub || node?.pubkey || '';
  }

  function capabilityValues(node) {
    const values = [];
    const capabilities = node?.capabilities;
    const mlCapabilities = node?.mlCapabilities;
    if (Array.isArray(capabilities)) values.push(...capabilities.filter(Boolean).map(String));
    else if (capabilities && typeof capabilities === 'object') values.push(...Object.keys(capabilities).filter((key) => capabilities[key]));
    else if (capabilities) values.push(String(capabilities));
    if (Array.isArray(mlCapabilities)) values.push(...mlCapabilities.filter(Boolean).map(String));
    else if (mlCapabilities && typeof mlCapabilities === 'object') values.push(...Object.keys(mlCapabilities).filter((key) => mlCapabilities[key]));
    else if (mlCapabilities) values.push(String(mlCapabilities));
    return uniqueValues(values);
  }

  function endpointValues(node) {
    const values = [];
    const endpoints = Array.isArray(node?.transportEndpoints) ? node.transportEndpoints : [];
    for (const endpoint of endpoints) {
      const transport = endpoint?.transport ? `${endpoint.transport}:` : '';
      const address = endpoint?.address || '';
      const port = endpoint?.port ? `:${endpoint.port}` : '';
      if (address || transport) values.push(`${transport}${address}${port}`);
    }
    return values.length > 0 ? values.join(', ') : '—';
  }

  function hostnames(node) {
    const dns = Array.isArray(node?.dnsHostnames) ? node.dnsHostnames : [];
    const endpointHostnames = Array.isArray(node?.endpoints) ? node.endpoints.map((endpoint) => endpoint.fqdn || endpoint.hostname).filter(Boolean) : [];
    return uniqueValues([...dns, ...endpointHostnames]).join(', ') || '—';
  }

  function meshMetric(node, key) {
    const health = node?.meshHealth || node?.mesh_health || {};
    const value = health?.[key] ?? health?.[key.toUpperCase()] ?? '';
    if (value === null || value === undefined || value === '') return '—';
    if (key === 'loss' && typeof value === 'number') return `${Math.round(value * 1000) / 10}%`;
    return String(value);
  }

  function matchesFilters(node) {
    if (filters.health && node?.health !== filters.health) return false;
    if (filters.worker && workerLabel(node) !== filters.worker) return false;
    if (filters.projection && node?.projectionStatus !== filters.projection) return false;
    if (filters.capability && !capabilityValues(node).includes(filters.capability)) return false;
    return true;
  }

  function resetFilters() {
    filters = { health: '', worker: '', capability: '', projection: '' };
  }
</script>

<section class="panel" aria-label="FIPS mesh">
  <div class="panel-header">
    <div>
      <h2>FIPS mesh</h2>
      <p>Mesh-connected workers and DNS/FIPS endpoint projections from Nostr read models.</p>
    </div>
    <span>{filteredNodes.length} shown · {nodes.length} total</span>
  </div>

  {#if state.error}
    <div class="empty-card error" role="status">
      <h3>FIPS mesh unavailable</h3>
      <p>{state.error}</p>
    </div>
  {:else if state.status === 'idle' || state.status === 'discovering' || state.status === 'connecting' || state.status === 'bootstrapping' || state.loading}
    <div class="empty-card" role="status">
      <h3>Connecting to FIPS mesh read models</h3>
      <p>Status: {state.status || 'idle'} · waiting for relay EOSE before marking the view live.</p>
    </div>
  {:else if state.relays?.length === 0 && nodes.length === 0}
    <div class="empty-card" role="status">
      <h3>FIPS mesh relay configuration disabled</h3>
      <p>No browser relays are available for FIPS mesh read models.</p>
    </div>
  {:else}
    <div class="mesh-status" aria-label="FIPS mesh Nostr status">
      <span class={`badge ${state.status === 'live' ? 'healthy' : state.status === 'degraded' || state.status === 'disconnected' ? 'warning' : 'unknown'}`}>{state.status || 'unknown'}</span>
      <span>{state.relays?.length || 0} relay{state.relays?.length === 1 ? '' : 's'} · EOSE {state.bootstrapComplete ? 'complete' : 'pending'}</span>
      {#if state.lastClosed?.reason}<span>Last CLOSED: {state.lastClosed.reason}</span>{/if}
    </div>

    <form class="filters" onsubmit={(event) => event.preventDefault()}>
      <label>Health<select bind:value={filters.health}><option value="">All health</option>{#each healthOptions as option}<option value={option}>{option}</option>{/each}</select></label>
      <label>Worker<select bind:value={filters.worker}><option value="">All workers</option>{#each workerOptions as option}<option value={option}>{option}</option>{/each}</select></label>
      <label>Capability<select bind:value={filters.capability}><option value="">All capabilities</option>{#each capabilityOptions as option}<option value={option}>{option}</option>{/each}</select></label>
      <label>Projection<select bind:value={filters.projection}><option value="">All projection states</option>{#each projectionOptions as option}<option value={option}>{option}</option>{/each}</select></label>
      <div class="filter-actions"><button type="button" class="secondary" onclick={resetFilters}>Reset</button></div>
    </form>

    {#if nodes.length === 0}
      <div class="empty-card"><h3>No FIPS mesh nodes projected yet</h3><p>Workers appear after FIPS mesh worker and DNS endpoint events are materialized by the Nostr read model.</p></div>
    {:else if filteredNodes.length === 0}
      <div class="empty-card"><h3>No FIPS mesh nodes match these filters</h3><p>Adjust health, worker, capability, or projection state filters.</p></div>
    {:else}
      <div class="table-wrap">
        <table>
          <thead><tr><th>Worker</th><th>Overlay</th><th>Nostr identity</th><th>Transport endpoints</th><th>Mesh health</th><th>DNS/FIPS hostnames</th><th>Projection</th><th>Gating reason</th></tr></thead>
          <tbody>
            {#each filteredNodes as node (node.pubkey || node.npub || node.name)}
              <tr>
                <td><strong>{workerLabel(node) || 'Unknown worker'}</strong></td>
                <td><code>{valueOrDash(node.overlayAddress || node.fipsOverlayAddr)}</code></td>
                <td><code>{valueOrDash(node.npub || node.pubkey)}</code></td>
                <td>{endpointValues(node)}</td>
                <td><span class={`badge ${healthTone(node.health)}`}>{valueOrDash(node.health)}</span><div class="metrics">RTT {meshMetric(node, 'rtt')} · Loss {meshMetric(node, 'loss')} · Jitter {meshMetric(node, 'jitter')} · Goodput {meshMetric(node, 'goodput')}</div></td>
                <td>{hostnames(node)}</td>
                <td>{valueOrDash(node.projectionStatus)}</td>
                <td>{valueOrDash(node.gatingReason)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</section>

<style>
  .panel { padding: 1rem; display: grid; gap: 1rem; background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 16px; }
  .panel-header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  .panel-header h2 { font-size: 1.25rem; }
  .panel-header p, .panel-header span, .empty-card p, .mesh-status, .metrics { color: var(--text-muted); }
  .empty-card { padding: 2rem; background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 16px; }
  .empty-card.error { border-color: color-mix(in srgb, var(--error) 55%, var(--border-color)); }
  .mesh-status { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
  .filters { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 0.75rem; align-items: end; }
  .filters label { color: var(--text-muted); display: grid; gap: 0.35rem; font-size: 0.85rem; font-weight: 700; }
  select { width: 100%; border: 1px solid var(--border-color); border-radius: 10px; padding: 0.55rem; color: var(--text-primary); background: var(--card-bg); }
  .filter-actions { display: flex; gap: 0.5rem; }
  .filters button { border: 1px solid var(--primary); border-radius: 999px; padding: 0.55rem 0.9rem; background: color-mix(in srgb, var(--primary) 15%, transparent); color: var(--primary); cursor: pointer; font-weight: 700; }
  .filters button.secondary { border-color: var(--border-color); background: transparent; color: var(--text-primary); }
  .table-wrap { overflow-x: auto; border: 1px solid var(--border-color); border-radius: 14px; }
  table { width: 100%; border-collapse: collapse; min-width: 1120px; }
  th, td { padding: 0.85rem; text-align: left; border-bottom: 1px solid var(--border-color); vertical-align: top; }
  th { color: var(--text-muted); font-size: 0.78rem; letter-spacing: 0.05em; text-transform: uppercase; }
  tbody tr:last-child td { border-bottom: 0; }
  code { color: var(--text-primary); font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 0.88rem; word-break: break-all; }
  .badge { border: 1px solid currentColor; border-radius: 999px; padding: 0.3rem 0.55rem; font-size: 0.72rem; font-weight: 800; text-transform: uppercase; white-space: nowrap; }
  .badge.healthy { color: var(--success); background: color-mix(in srgb, var(--success) 15%, transparent); }
  .badge.warning { color: var(--warning); background: color-mix(in srgb, var(--warning) 15%, transparent); }
  .badge.critical { color: var(--error); background: color-mix(in srgb, var(--error) 15%, transparent); }
  .badge.unknown { color: var(--text-muted); background: color-mix(in srgb, var(--hover-bg) 45%, transparent); }
  .metrics { margin-top: 0.35rem; font-size: 0.8rem; }
  @media (max-width: 720px) { .panel-header { flex-direction: column; align-items: stretch; } }
</style>
