<script>
  import { dnsState, seedDnsState, fetchEndpoints, fetchZones, fetchDrift, fetchPolicies } from '$lib/stores/dns.svelte.js';

  let { data } = $props();
  let activeTab = $state('zones');
  let selectedEndpoint = $state(null);
  let filters = $state({ zone: '', environment: '', capability: '', runtime: '', health: '' });
  let filterError = $state(null);

  $effect(() => {
    seedDnsState(data);
  });

  const zones = $derived(dnsState.zones || []);
  const endpoints = $derived(dnsState.endpoints || []);
  const driftEvents = $derived(dnsState.driftEvents || []);
  const policies = $derived(dnsState.policies || []);
  const endpointCount = $derived(endpoints.length);
  const unhealthyCount = $derived(endpoints.filter((endpoint) => healthTone(valueOf(endpoint, ['health', 'status', 'health_status'])) !== 'healthy').length);

  const zoneOptions = $derived(uniqueValues(zones, ['name', 'zone', 'zone_name']));
  const environmentOptions = $derived(uniqueValues(endpoints, ['environment', 'env']));
  const capabilityOptions = $derived(uniqueNestedValues(endpoints, ['capabilities', 'capability']));
  const runtimeOptions = $derived(uniqueValues(endpoints, ['runtime', 'runtime_name']));
  const healthOptions = $derived(uniqueValues(endpoints, ['health', 'status', 'health_status']));

  function valueOf(record, keys, fallback = '—') {
    for (const key of keys) {
      const value = record?.[key];
      if (value !== null && value !== undefined && value !== '') return value;
    }
    return fallback;
  }

  function listValue(value) {
    if (Array.isArray(value)) return value.filter(Boolean).join(', ');
    if (value && typeof value === 'object') return Object.values(value).filter(Boolean).join(', ');
    return value || '—';
  }

  function uniqueValues(records, keys) {
    return [...new Set(records.map((record) => valueOf(record, keys, '')).filter(Boolean).map(String))].sort();
  }

  function uniqueNestedValues(records, keys) {
    const values = [];
    for (const record of records) {
      const value = valueOf(record, keys, '');
      if (Array.isArray(value)) values.push(...value.filter(Boolean).map(String));
      else if (value) values.push(String(value));
    }
    return [...new Set(values)].sort();
  }

  function healthTone(value) {
    const normalized = String(value || '').toLowerCase();
    if (['healthy', 'ok', 'synced', 'active', 'passing'].includes(normalized)) return 'healthy';
    if (['degraded', 'warning', 'drifted', 'stale'].includes(normalized)) return 'warning';
    if (['unhealthy', 'failed', 'error', 'critical', 'down'].includes(normalized)) return 'critical';
    return 'unknown';
  }

  function enabledLabel(value) {
    return value === false || String(value).toLowerCase() === 'false' ? 'Disabled' : 'Enabled';
  }

  function formatDate(value) {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleString();
  }

  function resetFilters() {
    filters = { zone: '', environment: '', capability: '', runtime: '', health: '' };
    selectedEndpoint = null;
    applyFilters();
  }

  async function applyFilters() {
    filterError = null;
    try {
      await fetchEndpoints(filters);
    } catch (error) {
      filterError = error?.message || 'Failed to load DNS endpoints';
    }
  }

  async function refreshTab() {
    filterError = null;
    try {
      if (activeTab === 'zones') await fetchZones();
      if (activeTab === 'endpoints') await fetchEndpoints(filters);
      if (activeTab === 'drift') await fetchDrift();
      if (activeTab === 'policies') await fetchPolicies();
    } catch (error) {
      filterError = error?.message || 'Failed to refresh DNS data';
    }
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <p class="eyebrow">DNS fabric</p>
      <h1>DNS management</h1>
      <p class="subtitle">Zones, service endpoints, drift observations, and active DNS policies from Bahia read models.</p>
    </div>
    <div class="summary-card" aria-label="DNS summary">
      <span class="summary-number">{zones.length}</span>
      <span class="summary-label">Zones tracked</span>
      <span class:attention={unhealthyCount > 0} class="summary-note">{endpointCount} endpoint{endpointCount === 1 ? '' : 's'} · {unhealthyCount} attention</span>
    </div>
  </div>

  {#if data?.error || filterError}
    <div class="alert" role="status">
      <strong>DNS data unavailable.</strong>
      <span>{filterError || data.error}</span>
    </div>
  {/if}

  <nav class="tabs" aria-label="DNS views">
    <button type="button" class:active={activeTab === 'zones'} onclick={() => (activeTab = 'zones')}>Zones</button>
    <button type="button" class:active={activeTab === 'endpoints'} onclick={() => (activeTab = 'endpoints')}>Endpoints</button>
    <button type="button" class:active={activeTab === 'drift'} onclick={() => (activeTab = 'drift')}>Drift</button>
    <button type="button" class:active={activeTab === 'policies'} onclick={() => (activeTab = 'policies')}>Policies</button>
    <button type="button" class="refresh" onclick={refreshTab}>Refresh</button>
  </nav>

  {#if activeTab === 'zones'}
    <section class="panel" aria-label="DNS zones">
      <div class="panel-header">
        <h2>Zones</h2>
        <span>{zones.length} total</span>
      </div>
      {#if zones.length === 0 && !data?.error}
        <div class="empty-card">
          <h3>No DNS zones projected yet</h3>
          <p>Zones will appear after the DNS read model records zone state.</p>
        </div>
      {:else}
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Visibility</th>
                <th>Backend</th>
                <th>Health</th>
                <th>Records</th>
                <th>Last sync</th>
              </tr>
            </thead>
            <tbody>
              {#each zones as zone (valueOf(zone, ['id', 'name', 'zone'], JSON.stringify(zone)))}
                {@const health = valueOf(zone, ['health', 'status', 'health_status'])}
                <tr>
                  <td><strong>{valueOf(zone, ['name', 'zone', 'zone_name'])}</strong></td>
                  <td>{valueOf(zone, ['visibility', 'scope'])}</td>
                  <td>{valueOf(zone, ['backend', 'provider'])}</td>
                  <td><span class={`badge ${healthTone(health)}`}>{health}</span></td>
                  <td>{valueOf(zone, ['record_count', 'records_count', 'records'])}</td>
                  <td>{formatDate(valueOf(zone, ['last_sync_time', 'last_sync_at', 'synced_at'], ''))}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {:else if activeTab === 'endpoints'}
    <section class="panel" aria-label="DNS endpoints">
      <div class="panel-header">
        <h2>Endpoints</h2>
        <span>{endpoints.length} shown</span>
      </div>

      <form class="filters" onsubmit={(event) => { event.preventDefault(); applyFilters(); }}>
        <label>Zone<select bind:value={filters.zone}><option value="">All zones</option>{#each zoneOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Environment<select bind:value={filters.environment}><option value="">All environments</option>{#each environmentOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Capability<select bind:value={filters.capability}><option value="">All capabilities</option>{#each capabilityOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Runtime<select bind:value={filters.runtime}><option value="">All runtimes</option>{#each runtimeOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Health<select bind:value={filters.health}><option value="">All health</option>{#each healthOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <div class="filter-actions">
          <button type="submit">Apply</button>
          <button type="button" class="secondary" onclick={resetFilters}>Reset</button>
        </div>
      </form>

      {#if endpoints.length === 0 && !data?.error}
        <div class="empty-card">
          <h3>No DNS endpoints projected yet</h3>
          <p>Catalog endpoints will appear after DNS endpoint events are projected.</p>
        </div>
      {:else}
        <div class="endpoint-layout">
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>FQDN</th>
                  <th>Address</th>
                  <th>Health</th>
                  <th>Environment</th>
                  <th>Capabilities</th>
                  <th>Runtime</th>
                  <th>Hardware</th>
                </tr>
              </thead>
              <tbody>
                {#each endpoints as endpoint (valueOf(endpoint, ['id', 'fqdn', 'name'], JSON.stringify(endpoint)))}
                  {@const health = valueOf(endpoint, ['health', 'status', 'health_status'])}
                  <tr class:active-row={selectedEndpoint === endpoint} onclick={() => (selectedEndpoint = endpoint)}>
                    <td><button type="button" class="link-button" onclick={(event) => { event.stopPropagation(); selectedEndpoint = endpoint; }}>{valueOf(endpoint, ['fqdn', 'name', 'hostname'])}</button></td>
                    <td><code>{valueOf(endpoint, ['address', 'ip', 'target'])}</code></td>
                    <td><span class={`badge ${healthTone(health)}`}>{health}</span></td>
                    <td>{valueOf(endpoint, ['environment', 'env'])}</td>
                    <td>{listValue(valueOf(endpoint, ['capabilities', 'capability'], ''))}</td>
                    <td>{valueOf(endpoint, ['runtime', 'runtime_name'])}</td>
                    <td>{listValue(valueOf(endpoint, ['hardware', 'hardware_profile'], ''))}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>

          {#if selectedEndpoint}
            <aside class="detail-card" aria-label="Endpoint detail">
              <div class="detail-title">
                <h3>{valueOf(selectedEndpoint, ['fqdn', 'name', 'hostname'])}</h3>
                <button type="button" onclick={() => (selectedEndpoint = null)}>×</button>
              </div>
              <dl>
                <div><dt>Address</dt><dd>{valueOf(selectedEndpoint, ['address', 'ip', 'target'])}</dd></div>
                <div><dt>Zone</dt><dd>{valueOf(selectedEndpoint, ['zone', 'zone_name'])}</dd></div>
                <div><dt>Environment</dt><dd>{valueOf(selectedEndpoint, ['environment', 'env'])}</dd></div>
                <div><dt>Capabilities</dt><dd>{listValue(valueOf(selectedEndpoint, ['capabilities', 'capability'], ''))}</dd></div>
                <div><dt>Runtime</dt><dd>{valueOf(selectedEndpoint, ['runtime', 'runtime_name'])}</dd></div>
                <div><dt>Hardware</dt><dd>{listValue(valueOf(selectedEndpoint, ['hardware', 'hardware_profile'], ''))}</dd></div>
              </dl>
            </aside>
          {/if}
        </div>
      {/if}
    </section>
  {:else if activeTab === 'drift'}
    <section class="panel" aria-label="DNS drift history">
      <div class="panel-header">
        <h2>Drift history</h2>
        <span>{driftEvents.length} events</span>
      </div>
      {#if driftEvents.length === 0 && !data?.error}
        <div class="empty-card"><h3>No DNS drift events</h3><p>Recent drift observations will appear here after projection.</p></div>
      {:else}
        <ol class="timeline">
          {#each driftEvents as event (valueOf(event, ['id', 'event_id', 'timestamp'], JSON.stringify(event)))}
            <li>
              <span class="timeline-dot"></span>
              <article>
                <header><strong>{valueOf(event, ['record', 'record_name', 'fqdn'])}</strong><time>{formatDate(valueOf(event, ['timestamp', 'created_at', 'observed_at'], ''))}</time></header>
                <p>{valueOf(event, ['zone', 'zone_name'])}</p>
                <div class="diff"><span>Expected <code>{listValue(valueOf(event, ['expected'], ''))}</code></span><span>Actual <code>{listValue(valueOf(event, ['actual'], ''))}</code></span></div>
                <footer>{valueOf(event, ['resolution', 'status'])}</footer>
              </article>
            </li>
          {/each}
        </ol>
      {/if}
    </section>
  {:else}
    <section class="panel" aria-label="DNS policies">
      <div class="panel-header">
        <h2>Policies</h2>
        <span>{policies.length} active</span>
      </div>
      {#if policies.length === 0 && !data?.error}
        <div class="empty-card"><h3>No DNS policies projected yet</h3><p>Kind 31977 DNS policies or REST policy read models will appear here when available.</p></div>
      {:else}
        <div class="policy-grid">
          {#each policies as policy (valueOf(policy, ['id', 'name', 'd'], JSON.stringify(policy)))}
            <article class="policy-card">
              <header>
                <h3>{valueOf(policy, ['name', 'title', 'd'])}</h3>
                <span class={`badge ${enabledLabel(valueOf(policy, ['enabled'], true)) === 'Enabled' ? 'healthy' : 'unknown'}`}>{enabledLabel(valueOf(policy, ['enabled'], true))}</span>
              </header>
              <dl>
                <div><dt>Scope</dt><dd>{valueOf(policy, ['scope', 'zone'], 'global')}</dd></div>
                <div><dt>Rules</dt><dd>{valueOf(policy, ['rule_count', 'rules_count'], Array.isArray(policy?.rules) ? policy.rules.length : 0)}</dd></div>
              </dl>
            </article>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 1.5rem; }
  .header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  .eyebrow { color: var(--primary); font-size: 0.8rem; font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase; }
  h1 { margin-top: 0.25rem; font-size: 2rem; }
  .subtitle { color: var(--text-muted); margin-top: 0.35rem; max-width: 760px; }
  .summary-card, .panel, .empty-card, .alert, .detail-card, .policy-card { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 16px; }
  .summary-card { min-width: 210px; padding: 1rem; display: grid; gap: 0.15rem; }
  .summary-number { font-size: 2rem; font-weight: 800; }
  .summary-label, .summary-note, .panel-header span, .empty-card p, dt, time, .timeline p, .timeline footer { color: var(--text-muted); }
  .summary-note.attention { color: var(--warning); font-weight: 700; }
  .alert { padding: 1rem; display: grid; gap: 0.25rem; border-color: color-mix(in srgb, var(--error) 55%, var(--border-color)); }
  .alert strong { color: var(--error); }
  .tabs { display: flex; flex-wrap: wrap; gap: 0.5rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; }
  .tabs button, .filters button { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.55rem 0.9rem; background: var(--card-bg); color: var(--text-primary); cursor: pointer; font-weight: 700; }
  .tabs button.active, .filters button { border-color: var(--primary); background: color-mix(in srgb, var(--primary) 15%, transparent); color: var(--primary); }
  .tabs button.refresh { margin-left: auto; }
  .panel { padding: 1rem; display: grid; gap: 1rem; }
  .panel-header { display: flex; justify-content: space-between; gap: 1rem; align-items: center; }
  .panel-header h2 { font-size: 1.25rem; }
  .empty-card { padding: 2rem; }
  .table-wrap { overflow-x: auto; border: 1px solid var(--border-color); border-radius: 14px; }
  table { width: 100%; border-collapse: collapse; min-width: 820px; }
  th, td { padding: 0.85rem; text-align: left; border-bottom: 1px solid var(--border-color); vertical-align: top; }
  th { color: var(--text-muted); font-size: 0.78rem; letter-spacing: 0.05em; text-transform: uppercase; }
  tbody tr { cursor: default; }
  tbody tr:hover, tr.active-row { background: color-mix(in srgb, var(--hover-bg) 55%, transparent); }
  tbody tr:last-child td { border-bottom: 0; }
  code { color: var(--text-primary); font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 0.88rem; word-break: break-all; }
  .badge { border: 1px solid currentColor; border-radius: 999px; padding: 0.3rem 0.55rem; font-size: 0.72rem; font-weight: 800; text-transform: uppercase; white-space: nowrap; }
  .badge.healthy { color: var(--success); background: color-mix(in srgb, var(--success) 15%, transparent); }
  .badge.warning { color: var(--warning); background: color-mix(in srgb, var(--warning) 15%, transparent); }
  .badge.critical { color: var(--error); background: color-mix(in srgb, var(--error) 15%, transparent); }
  .badge.unknown { color: var(--text-muted); background: color-mix(in srgb, var(--hover-bg) 45%, transparent); }
  .filters { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 0.75rem; align-items: end; }
  .filters label { color: var(--text-muted); display: grid; gap: 0.35rem; font-size: 0.85rem; font-weight: 700; }
  select { width: 100%; border: 1px solid var(--border-color); border-radius: 10px; padding: 0.55rem; color: var(--text-primary); background: var(--card-bg); }
  .filter-actions { display: flex; gap: 0.5rem; }
  .filters button.secondary { border-color: var(--border-color); background: transparent; color: var(--text-primary); }
  .endpoint-layout { display: grid; grid-template-columns: minmax(0, 1fr) minmax(280px, 360px); gap: 1rem; align-items: start; }
  .link-button { border: 0; background: transparent; color: var(--primary); padding: 0; font: inherit; font-weight: 800; cursor: pointer; text-align: left; }
  .detail-card { padding: 1rem; display: grid; gap: 1rem; position: sticky; top: 1rem; }
  .detail-title { display: flex; justify-content: space-between; gap: 1rem; }
  .detail-title button { border: 0; background: transparent; color: var(--text-muted); cursor: pointer; font-size: 1.4rem; }
  dl { margin: 0; display: grid; gap: 0.75rem; }
  dd { margin: 0.2rem 0 0; word-break: break-word; }
  .timeline { list-style: none; margin: 0; padding: 0; display: grid; gap: 1rem; }
  .timeline li { display: grid; grid-template-columns: 1rem minmax(0, 1fr); gap: 0.75rem; }
  .timeline-dot { width: 0.75rem; height: 0.75rem; border-radius: 999px; margin-top: 0.4rem; background: var(--primary); box-shadow: 0 0 0 4px color-mix(in srgb, var(--primary) 15%, transparent); }
  .timeline article { border: 1px solid var(--border-color); border-radius: 14px; padding: 1rem; display: grid; gap: 0.65rem; }
  .timeline header { display: flex; justify-content: space-between; gap: 1rem; }
  .diff { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 0.75rem; }
  .diff span { border: 1px solid var(--border-color); border-radius: 10px; padding: 0.65rem; display: grid; gap: 0.25rem; }
  .policy-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 1rem; }
  .policy-card { padding: 1rem; display: grid; gap: 1rem; }
  .policy-card header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  @media (max-width: 920px) { .endpoint-layout { grid-template-columns: 1fr; } .detail-card { position: static; } }
  @media (max-width: 720px) { .header, .panel-header, .timeline header { flex-direction: column; align-items: stretch; } .summary-card { width: 100%; } .tabs button.refresh { margin-left: 0; } }
</style>
