<script>
  import { onMount } from 'svelte';
  import { authState } from '$lib/stores/auth.js';
  import { controlplaneConnection } from '$lib/stores/controlplane.svelte.js';
  import {
    applyDNSPolicy,
    connect,
    createDNSZone,
    disconnect,
    dnsState,
    overrideDNSRecord,
    remediateDNSDrift
  } from '$lib/stores/dns.svelte.js';
  import { DNS_COMMANDS } from '$lib/nostr/dns-controlplane.js';
  import {
    DNS_CONTROL_FORMS,
    buildDNSCommandPayload,
    commandRunView,
    initialDNSCommandForms,
    validateDNSCommandForm
  } from './page-model.js';
  import { bootstrapFipsMesh, disconnectFipsMesh, fipsMeshState, meshNodes } from '$lib/stores/fips-mesh.svelte.js';
  import FipsMeshPanel from './FipsMeshPanel.svelte';

  let { data } = $props();
  let activeTab = $state('zones');
  let selectedEndpoint = $state(null);
  let filters = $state({ zone: '', environment: '', capability: '', runtime: '', health: '' });
  let filterError = $state(null);
  let commandForms = $state(initialDNSCommandForms());
  let commandFormErrors = $state({});
  let submittingCommand = $state(null);

  onMount(() => {
    connect(data?.relayUrls || data?.relayUrl || [], data?.servicePubkey);
    bootstrapFipsMesh({ relays: data?.relayUrls || data?.relayUrl || [], servicePubkey: data?.servicePubkey });
    return () => {
      disconnect();
      disconnectFipsMesh();
    };
  });

  const zones = $derived(dnsState.zones || []);
  const allEndpoints = $derived(dnsState.endpoints || []);
  const endpoints = $derived(allEndpoints.filter((endpoint) => matchesEndpointFilters(endpoint)));
  const driftEvents = $derived(dnsState.driftEvents || []);
  const policies = $derived(dnsState.policies || []);
  const meshNodeCount = $derived((meshNodes || []).length);
  const endpointCount = $derived(endpoints.length);
  const unhealthyCount = $derived(endpoints.filter((endpoint) => healthTone(valueOf(endpoint, ['health', 'status', 'health_status'])) !== 'healthy').length);
  const operatorReady = $derived(authState.status === 'authenticated' && Boolean(authState.pubkey));
  const commandDisabledReason = $derived(operatorReady ? '' : 'Authenticate with NIP-07 or NIP-46 before signing DNS commands.');
  const recentCommandRuns = $derived(dnsState.commandRuns || []);

  const zoneOptions = $derived(uniqueValues(zones, ['name', 'zone', 'zone_name']));
  const environmentOptions = $derived(uniqueValues(allEndpoints, ['environment', 'env']));
  const capabilityOptions = $derived(uniqueNestedValues(allEndpoints, ['capabilities', 'capability']));
  const runtimeOptions = $derived(uniqueValues(allEndpoints, ['runtime', 'runtime_name']));
  const healthOptions = $derived(uniqueValues(allEndpoints, ['health', 'status', 'health_status']));

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

  function matchesEndpointFilters(endpoint) {
    if (filters.zone && valueOf(endpoint, ['zone', 'zone_name'], '') !== filters.zone) return false;
    if (filters.environment && valueOf(endpoint, ['environment', 'env'], '') !== filters.environment) return false;
    if (filters.runtime && valueOf(endpoint, ['runtime', 'runtime_name'], '') !== filters.runtime) return false;
    if (filters.health && valueOf(endpoint, ['health', 'status', 'health_status'], '') !== filters.health) return false;
    if (filters.capability) {
      const capabilities = valueOf(endpoint, ['capabilities', 'capability'], []);
      const values = Array.isArray(capabilities) ? capabilities.map(String) : [String(capabilities || '')];
      if (!values.includes(filters.capability)) return false;
    }
    return true;
  }

  function resetFilters() {
    filters = { zone: '', environment: '', capability: '', runtime: '', health: '' };
    selectedEndpoint = null;
    filterError = null;
  }

  function applyFilters() {
    selectedEndpoint = null;
    filterError = null;
  }

  const commandStarters = {
    [DNS_COMMANDS.ZONE_CREATE]: createDNSZone,
    [DNS_COMMANDS.POLICY_APPLY]: applyDNSPolicy,
    [DNS_COMMANDS.RECORD_OVERRIDE]: overrideDNSRecord,
    [DNS_COMMANDS.DRIFT_REMEDIATE]: remediateDNSDrift
  };

  async function submitDNSCommand(command) {
    commandFormErrors = { ...commandFormErrors, [command]: [] };
    const validation = validateDNSCommandForm(command, commandForms[command]);
    if (!validation.valid) {
      commandFormErrors = { ...commandFormErrors, [command]: validation.errors };
      return;
    }
    if (!operatorReady) {
      commandFormErrors = { ...commandFormErrors, [command]: [commandDisabledReason] };
      return;
    }

    submittingCommand = command;
    try {
      const payload = buildDNSCommandPayload(command, commandForms[command]);
      await commandStarters[command](payload);
    } catch (error) {
      commandFormErrors = { ...commandFormErrors, [command]: [error?.message || String(error)] };
    } finally {
      submittingCommand = null;
    }
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <p class="eyebrow">DNS fabric</p>
      <h1>DNS management</h1>
      <p class="subtitle">Zones, service endpoints, drift observations, and active DNS policies from Bahia Nostr read models.</p>
    </div>
    <div class="summary-card" aria-label="DNS summary">
      <span class="summary-number">{zones.length}</span>
      <span class="summary-label">Zones tracked</span>
      <span class:attention={unhealthyCount > 0} class="summary-note">{endpointCount} endpoint{endpointCount === 1 ? '' : 's'} · {unhealthyCount} attention · {meshNodeCount} mesh node{meshNodeCount === 1 ? '' : 's'}</span>
    </div>
  </div>

  {#if dnsState.error.subscription || filterError}
    <div class="alert" role="status">
      <strong>DNS relay subscription unavailable.</strong>
      <span>{filterError || dnsState.error.subscription}</span>
    </div>
  {/if}

  <div class="connection-status" aria-label="DNS Nostr subscription status">
    <span class={`badge ${dnsState.connection.status === 'live' ? 'healthy' : dnsState.connection.status === 'error' || dnsState.connection.status === 'auth-required' ? 'critical' : 'unknown'}`}>{dnsState.connection.status}</span>
    <span>{dnsState.connection.relays.length} relay{dnsState.connection.relays.length === 1 ? '' : 's'} · {dnsState.connection.eoseRelays.length} EOSE</span>
  </div>

  <section class="panel command-panel" aria-label="DNS Nostr command controls">
    <div class="panel-header">
      <div>
        <h2>Signed DNS control-plane commands</h2>
        <p>Operator actions are signed Nostr events and complete only after explicit Bahia result events.</p>
      </div>
      <span class={`badge ${operatorReady ? 'healthy' : 'critical'}`}>{operatorReady ? 'operator ready' : 'auth required'}</span>
    </div>

    <div class="operator-grid" aria-label="DNS operator readiness">
      <div><strong>Operator</strong><span>{authState.pubkey || 'Not authenticated'}</span></div>
      <div><strong>Signer</strong><span>{authState.authMethod || authState.status}</span></div>
      <div><strong>Command relays</strong><span>{controlplaneConnection.relays.length || dnsState.connection.relays.length} configured</span></div>
      <div><strong>Control plane</strong><span>{controlplaneConnection.status}</span></div>
    </div>
    {#if commandDisabledReason}
      <div class="alert compact" role="status"><strong>Signing unavailable.</strong><span>{commandDisabledReason}</span></div>
    {/if}

    <div class="command-grid">
      <form class="command-card" onsubmit={(event) => { event.preventDefault(); submitDNSCommand(DNS_COMMANDS.ZONE_CREATE); }}>
        <h3>{DNS_CONTROL_FORMS[DNS_COMMANDS.ZONE_CREATE].title}</h3>
        <p>{DNS_CONTROL_FORMS[DNS_COMMANDS.ZONE_CREATE].description}</p>
        <label>Zone<input bind:value={commandForms[DNS_COMMANDS.ZONE_CREATE].zone} autocomplete="off" /></label>
        <label>Backend<input bind:value={commandForms[DNS_COMMANDS.ZONE_CREATE].backend} autocomplete="off" /></label>
        <label>Visibility<select bind:value={commandForms[DNS_COMMANDS.ZONE_CREATE].visibility}><option value="public">Public</option><option value="private">Private</option><option value="internal">Internal</option></select></label>
        <label class="inline"><input type="checkbox" bind:checked={commandForms[DNS_COMMANDS.ZONE_CREATE].reconcile} /> Reconcile existing zone state</label>
        <label>Idempotency key<input bind:value={commandForms[DNS_COMMANDS.ZONE_CREATE].idempotencyKey} autocomplete="off" /></label>
        {#if commandFormErrors[DNS_COMMANDS.ZONE_CREATE]?.length}<ul class="form-errors">{#each commandFormErrors[DNS_COMMANDS.ZONE_CREATE] as error}<li>{error}</li>{/each}</ul>{/if}
        <button type="submit" disabled={!operatorReady || submittingCommand === DNS_COMMANDS.ZONE_CREATE}>{submittingCommand === DNS_COMMANDS.ZONE_CREATE ? 'Signing…' : DNS_CONTROL_FORMS[DNS_COMMANDS.ZONE_CREATE].submitLabel}</button>
      </form>

      <form class="command-card" onsubmit={(event) => { event.preventDefault(); submitDNSCommand(DNS_COMMANDS.POLICY_APPLY); }}>
        <h3>{DNS_CONTROL_FORMS[DNS_COMMANDS.POLICY_APPLY].title}</h3>
        <p>{DNS_CONTROL_FORMS[DNS_COMMANDS.POLICY_APPLY].description}</p>
        <label>Policy id<input bind:value={commandForms[DNS_COMMANDS.POLICY_APPLY].policyId} autocomplete="off" /></label>
        <label>Zone scope<input bind:value={commandForms[DNS_COMMANDS.POLICY_APPLY].zone} autocomplete="off" /></label>
        <label>Environment<input bind:value={commandForms[DNS_COMMANDS.POLICY_APPLY].environment} autocomplete="off" /></label>
        <label>Idempotency key<input bind:value={commandForms[DNS_COMMANDS.POLICY_APPLY].idempotencyKey} autocomplete="off" /></label>
        {#if commandFormErrors[DNS_COMMANDS.POLICY_APPLY]?.length}<ul class="form-errors">{#each commandFormErrors[DNS_COMMANDS.POLICY_APPLY] as error}<li>{error}</li>{/each}</ul>{/if}
        <button type="submit" disabled={!operatorReady || submittingCommand === DNS_COMMANDS.POLICY_APPLY}>{submittingCommand === DNS_COMMANDS.POLICY_APPLY ? 'Signing…' : DNS_CONTROL_FORMS[DNS_COMMANDS.POLICY_APPLY].submitLabel}</button>
      </form>

      <form class="command-card" onsubmit={(event) => { event.preventDefault(); submitDNSCommand(DNS_COMMANDS.RECORD_OVERRIDE); }}>
        <h3>{DNS_CONTROL_FORMS[DNS_COMMANDS.RECORD_OVERRIDE].title}</h3>
        <p>{DNS_CONTROL_FORMS[DNS_COMMANDS.RECORD_OVERRIDE].description}</p>
        <label>Zone<input bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].zone} autocomplete="off" /></label>
        <label>Record name<input bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].recordName} autocomplete="off" /></label>
        <label>Record type<input bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].recordType} autocomplete="off" /></label>
        <label>Value<input bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].value} autocomplete="off" /></label>
        <label>TTL<input inputmode="numeric" bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].ttl} autocomplete="off" /></label>
        <label>Reason<textarea bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].reason}></textarea></label>
        <label>Idempotency key<input bind:value={commandForms[DNS_COMMANDS.RECORD_OVERRIDE].idempotencyKey} autocomplete="off" /></label>
        {#if commandFormErrors[DNS_COMMANDS.RECORD_OVERRIDE]?.length}<ul class="form-errors">{#each commandFormErrors[DNS_COMMANDS.RECORD_OVERRIDE] as error}<li>{error}</li>{/each}</ul>{/if}
        <button type="submit" disabled={!operatorReady || submittingCommand === DNS_COMMANDS.RECORD_OVERRIDE}>{submittingCommand === DNS_COMMANDS.RECORD_OVERRIDE ? 'Signing…' : DNS_CONTROL_FORMS[DNS_COMMANDS.RECORD_OVERRIDE].submitLabel}</button>
      </form>

      <form class="command-card" onsubmit={(event) => { event.preventDefault(); submitDNSCommand(DNS_COMMANDS.DRIFT_REMEDIATE); }}>
        <h3>{DNS_CONTROL_FORMS[DNS_COMMANDS.DRIFT_REMEDIATE].title}</h3>
        <p>{DNS_CONTROL_FORMS[DNS_COMMANDS.DRIFT_REMEDIATE].description}</p>
        <label>Zone<input bind:value={commandForms[DNS_COMMANDS.DRIFT_REMEDIATE].zone} autocomplete="off" /></label>
        <label>FQDN<input bind:value={commandForms[DNS_COMMANDS.DRIFT_REMEDIATE].fqdn} autocomplete="off" /></label>
        <label>Reason<textarea bind:value={commandForms[DNS_COMMANDS.DRIFT_REMEDIATE].reason}></textarea></label>
        <label>Idempotency key<input bind:value={commandForms[DNS_COMMANDS.DRIFT_REMEDIATE].idempotencyKey} autocomplete="off" /></label>
        {#if commandFormErrors[DNS_COMMANDS.DRIFT_REMEDIATE]?.length}<ul class="form-errors">{#each commandFormErrors[DNS_COMMANDS.DRIFT_REMEDIATE] as error}<li>{error}</li>{/each}</ul>{/if}
        <button type="submit" disabled={!operatorReady || submittingCommand === DNS_COMMANDS.DRIFT_REMEDIATE}>{submittingCommand === DNS_COMMANDS.DRIFT_REMEDIATE ? 'Signing…' : DNS_CONTROL_FORMS[DNS_COMMANDS.DRIFT_REMEDIATE].submitLabel}</button>
      </form>
    </div>

    <div class="runs" aria-label="DNS command run tracker">
      <h3>Recent command runs</h3>
      {#if recentCommandRuns.length === 0}
        <p class="muted">No DNS command events signed in this browser session.</p>
      {:else}
        <ol>
          {#each recentCommandRuns as run (run.id)}
            {@const view = commandRunView(run)}
            <li class="run-card">
              <header><strong>{view.command}</strong><span class={`badge ${view.error ? 'critical' : view.phase === 'completed' ? 'healthy' : view.phase === 'failed' || view.phase === 'rejected' || view.phase === 'error' ? 'critical' : 'unknown'}`}>{view.phase}</span></header>
              <dl>
                <div><dt>Request event id</dt><dd><code>{view.requestEventId || 'Pending signed event'}</code></dd></div>
                <div><dt>Relay OK</dt><dd>{view.okSummary}</dd></div>
              </dl>
              {#if view.statusLines.length}
                <div class="run-section"><strong>Status progress</strong><ul>{#each view.statusLines as line}<li>{line}</li>{/each}</ul></div>
              {/if}
              {#if view.resultLine}<p class="result-line"><strong>Result:</strong> {view.resultLine}</p>{/if}
              {#if view.error}<p class="error-line"><strong>Error:</strong> {view.error}</p>{/if}
            </li>
          {/each}
        </ol>
      {/if}
    </div>
  </section>

  <nav class="tabs" aria-label="DNS views">
    <button type="button" class:active={activeTab === 'zones'} onclick={() => (activeTab = 'zones')}>Zones</button>
    <button type="button" class:active={activeTab === 'endpoints'} onclick={() => (activeTab = 'endpoints')}>Endpoints</button>
    <button type="button" class:active={activeTab === 'drift'} onclick={() => (activeTab = 'drift')}>Drift</button>
    <button type="button" class:active={activeTab === 'policies'} onclick={() => (activeTab = 'policies')}>Policies</button>
    <button type="button" class:active={activeTab === 'mesh'} onclick={() => (activeTab = 'mesh')}>FIPS/Mesh</button>
  </nav>

  {#if activeTab === 'zones'}
    <section class="panel" aria-label="DNS zones">
      <div class="panel-header">
        <h2>Zones</h2>
        <span>{zones.length} total</span>
      </div>
      {#if zones.length === 0 && !dnsState.error.subscription}
        <div class="empty-card"><h3>No DNS zones projected yet</h3><p>Zones will appear after Kind 31975 DNS zone events are projected.</p></div>
      {:else}
        <div class="table-wrap">
          <table>
            <thead><tr><th>Name</th><th>Visibility</th><th>Backend</th><th>Health</th><th>Records</th><th>Last sync</th></tr></thead>
            <tbody>
              {#each zones as zone (valueOf(zone, ['id', 'name', 'zone'], JSON.stringify(zone)))}
                {@const health = valueOf(zone, ['health', 'status', 'health_status'])}
                <tr>
                  <td><strong>{valueOf(zone, ['name', 'zone', 'zone_name'])}</strong></td>
                  <td>{valueOf(zone, ['visibility', 'scope'])}</td>
                  <td>{valueOf(zone, ['backend', 'provider'])}</td>
                  <td><span class={`badge ${healthTone(health)}`}>{health}</span></td>
                  <td>{valueOf(zone, ['record_count', 'records_count', 'records'])}</td>
                  <td>{formatDate(valueOf(zone, ['last_sync_time', 'last_sync_at', 'synced_at', 'updated_at'], ''))}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {:else if activeTab === 'endpoints'}
    <section class="panel" aria-label="DNS endpoints">
      <div class="panel-header"><h2>Endpoints</h2><span>{endpoints.length} shown</span></div>
      <form class="filters" onsubmit={(event) => { event.preventDefault(); applyFilters(); }}>
        <label>Zone<select bind:value={filters.zone}><option value="">All zones</option>{#each zoneOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Environment<select bind:value={filters.environment}><option value="">All environments</option>{#each environmentOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Capability<select bind:value={filters.capability}><option value="">All capabilities</option>{#each capabilityOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Runtime<select bind:value={filters.runtime}><option value="">All runtimes</option>{#each runtimeOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <label>Health<select bind:value={filters.health}><option value="">All health</option>{#each healthOptions as option}<option value={option}>{option}</option>{/each}</select></label>
        <div class="filter-actions"><button type="submit">Apply</button><button type="button" class="secondary" onclick={resetFilters}>Reset</button></div>
      </form>

      {#if endpoints.length === 0 && !dnsState.error.subscription}
        <div class="empty-card"><h3>No DNS endpoints projected yet</h3><p>Catalog endpoints will appear after Kind 31976 DNS endpoint events are projected.</p></div>
      {:else}
        <div class="endpoint-layout">
          <div class="table-wrap">
            <table>
              <thead><tr><th>FQDN</th><th>Address</th><th>Health</th><th>Environment</th><th>Capabilities</th><th>Runtime</th><th>Hardware</th></tr></thead>
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
              <div class="detail-title"><h3>{valueOf(selectedEndpoint, ['fqdn', 'name', 'hostname'])}</h3><button type="button" onclick={() => (selectedEndpoint = null)}>×</button></div>
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
      <div class="panel-header"><h2>Drift history</h2><span>{driftEvents.length} events</span></div>
      {#if driftEvents.length === 0 && !dnsState.error.subscription}
        <div class="empty-card"><h3>No DNS drift events</h3><p>Drift derived from DNS endpoint state will appear here after projection.</p></div>
      {:else}
        <ol class="timeline">
          {#each driftEvents as event (valueOf(event, ['id', 'event_id', 'timestamp'], JSON.stringify(event)))}
            <li><span class="timeline-dot"></span><article><header><strong>{valueOf(event, ['record', 'record_name', 'fqdn'])}</strong><time>{formatDate(valueOf(event, ['timestamp', 'created_at', 'observed_at'], ''))}</time></header><p>{valueOf(event, ['zone', 'zone_name'])}</p><div class="diff"><span>Expected <code>{listValue(valueOf(event, ['expected'], ''))}</code></span><span>Actual <code>{listValue(valueOf(event, ['actual'], ''))}</code></span></div><footer>{valueOf(event, ['resolution', 'status'])}</footer></article></li>
          {/each}
        </ol>
      {/if}
    </section>
  {:else if activeTab === 'policies'}
    <section class="panel" aria-label="DNS policies">
      <div class="panel-header"><h2>Policies</h2><span>{policies.length} active</span></div>
      {#if policies.length === 0 && !dnsState.error.subscription}
        <div class="empty-card"><h3>No DNS policies projected yet</h3><p>Kind 31977 DNS policies will appear here after relay projection.</p></div>
      {:else}
        <div class="policy-grid">
          {#each policies as policy (valueOf(policy, ['id', 'name', 'd'], JSON.stringify(policy)))}
            <article class="policy-card">
              <header><h3>{valueOf(policy, ['name', 'title', 'd'])}</h3><span class={`badge ${enabledLabel(valueOf(policy, ['enabled'], true)) === 'Enabled' ? 'healthy' : 'unknown'}`}>{enabledLabel(valueOf(policy, ['enabled'], true))}</span></header>
              <dl><div><dt>Scope</dt><dd>{valueOf(policy, ['scope', 'zone'], 'global')}</dd></div><div><dt>Rules</dt><dd>{valueOf(policy, ['rule_count', 'rules_count'], Array.isArray(policy?.rules) ? policy.rules.length : 0)}</dd></div></dl>
            </article>
          {/each}
        </div>
      {/if}
    </section>
  {:else}
    <FipsMeshPanel meshState={fipsMeshState} nodes={meshNodes} />
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
  .summary-label, .summary-note, .panel-header span, .empty-card p, dt, time, .timeline p, .timeline footer, .connection-status { color: var(--text-muted); }
  .summary-note.attention { color: var(--warning); font-weight: 700; }
  .alert { padding: 1rem; display: grid; gap: 0.25rem; border-color: color-mix(in srgb, var(--error) 55%, var(--border-color)); }
  .alert strong { color: var(--error); }
  .connection-status { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
  .tabs { display: flex; flex-wrap: wrap; gap: 0.5rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; }
  .tabs button, .filters button { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.55rem 0.9rem; background: var(--card-bg); color: var(--text-primary); cursor: pointer; font-weight: 700; }
  .tabs button.active, .filters button { border-color: var(--primary); background: color-mix(in srgb, var(--primary) 15%, transparent); color: var(--primary); }
  .panel { padding: 1rem; display: grid; gap: 1rem; }
  .command-panel { gap: 1.25rem; }
  .operator-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 0.75rem; }
  .operator-grid div, .command-card, .run-card { border: 1px solid var(--border-color); border-radius: 14px; padding: 1rem; background: color-mix(in srgb, var(--card-bg) 90%, transparent); }
  .operator-grid div { display: grid; gap: 0.25rem; }
  .operator-grid span, .command-card p, .muted { color: var(--text-muted); word-break: break-word; }
  .alert.compact { padding: 0.75rem; }
  .command-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 1rem; align-items: start; }
  .command-card { display: grid; gap: 0.75rem; }
  .command-card label { color: var(--text-muted); display: grid; gap: 0.35rem; font-size: 0.85rem; font-weight: 700; }
  .command-card label.inline { display: flex; align-items: center; color: var(--text-primary); }
  .command-card label.inline input { width: auto; }
  .command-card button { border: 1px solid var(--primary); border-radius: 999px; padding: 0.65rem 1rem; background: color-mix(in srgb, var(--primary) 15%, transparent); color: var(--primary); cursor: pointer; font-weight: 800; }
  .command-card button:disabled { border-color: var(--border-color); color: var(--text-muted); cursor: not-allowed; background: transparent; }
  .form-errors { margin: 0; padding-left: 1.2rem; color: var(--error); }
  .runs { display: grid; gap: 0.75rem; }
  .runs ol { list-style: none; margin: 0; padding: 0; display: grid; gap: 0.75rem; }
  .run-card { display: grid; gap: 0.75rem; }
  .run-card header { display: flex; justify-content: space-between; gap: 1rem; align-items: center; }
  .run-section ul { margin: 0.35rem 0 0; padding-left: 1.2rem; }
  .result-line { color: var(--success); }
  .error-line { color: var(--error); }
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
  input, select, textarea { width: 100%; border: 1px solid var(--border-color); border-radius: 10px; padding: 0.55rem; color: var(--text-primary); background: var(--card-bg); }
  textarea { min-height: 4.5rem; resize: vertical; }
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
  @media (max-width: 720px) { .header, .panel-header, .timeline header { flex-direction: column; align-items: stretch; } .summary-card { width: 100%; } }
</style>
