<script>
  import {
    createDNSBackend, createDNSEndpoint, deleteDNSBackend, deleteDNSEndpoint,
    deleteDNSPolicy, deleteDNSZone, updateDNSBackend, updateDNSEndpoint,
    updateDNSPolicy, updateDNSZone
  } from '$lib/stores/dns.svelte.js';

  let { zones = [], endpoints = [], backends = [], policies = [], disabled = false } = $props();
  let zoneKey = $state('');
  let endpointKey = $state('');
  let backendKey = $state('');
  let policyKey = $state('');
  let zoneForm = $state({ visibility: 'internal', backend_ref: '', ttl: 60, authoritative: false, allow_empty_authoritative: false });
  let endpointForm = $state({ family: 'service', name: '', environment: '', zone: '', fqdn: '', address: '', source: 'operator' });
  let backendForm = $state({ ref: '', type: 'coredns', health: 'healthy' });
  let policyForm = $state({ name: '', enabled: true, rules: '[]' });
  let submitting = $state('');
  let notice = $state('');
  let error = $state('');

  const zone = $derived(zones.find(row => row.name === zoneKey));
  const endpoint = $derived(endpoints.find(row => row.coordinate === endpointKey));
  const backend = $derived(backends.find(row => row.ref === backendKey));
  const policy = $derived(policies.find(row => row.id === policyKey));

  function selectZone(event) {
    zoneKey = event.currentTarget.value;
    const row = zones.find(item => item.name === zoneKey);
    if (row) zoneForm = { visibility: row.visibility, backend_ref: row.backend_ref || row.backend,
      ttl: row.ttl, authoritative: Boolean(row.authoritative),
      allow_empty_authoritative: Boolean(row.allow_empty_authoritative) };
  }

  function selectEndpoint(event) {
    endpointKey = event.currentTarget.value;
    const row = endpoints.find(item => item.coordinate === endpointKey);
    endpointForm = row ? { family: row.family, name: row.name, environment: row.environment,
      zone: row.zone, fqdn: row.fqdn, address: row.address, source: row.source || 'operator' }
      : { family: 'service', name: '', environment: '', zone: '', fqdn: '', address: '', source: 'operator' };
  }

  function selectBackend(event) {
    backendKey = event.currentTarget.value;
    const row = backends.find(item => item.ref === backendKey);
    backendForm = row ? { ref: row.ref, type: row.type, health: row.health }
      : { ref: '', type: 'coredns', health: 'healthy' };
  }

  function selectPolicy(event) {
    policyKey = event.currentTarget.value;
    const row = policies.find(item => item.id === policyKey);
    if (row) policyForm = { name: row.name, enabled: row.enabled !== false, rules: JSON.stringify(row.rules || [], null, 2) };
  }

  async function submit(op, action) {
    if (disabled) return;
    submitting = op;
    notice = '';
    error = '';
    try {
      await action();
      notice = `${op} intent pending daemon acceptance`;
    } catch (cause) {
      error = cause?.message || `Failed to publish ${op} intent`;
    } finally {
      submitting = '';
    }
  }

  function remove(op, label, action) {
    if (window.confirm(`Delete ${label}?`)) return submit(op, action);
  }

  function saveZone(event) {
    event.preventDefault();
    if (!zone) return;
    return submit('zone-update', () => updateDNSZone(zone, { name: zone.name, ...zoneForm, ttl: Number(zoneForm.ttl) }));
  }

  function saveEndpoint(event) {
    event.preventDefault();
    const coordinate = endpoint?.coordinate || `endpoint:${endpointForm.family}:${endpointForm.name.trim()}${endpointForm.family === 'worker' ? '' : `:${endpointForm.environment.trim()}`}`;
    const payload = { ...(endpoint && {
      ...(endpoint.registry_id ? { id: endpoint.registry_id } : {}),
      ...(endpoint.service_id && /^[0-9a-f-]{36}$/i.test(endpoint.service_id) ? { service_id: endpoint.service_id } : {}),
      ...(endpoint.llm_route_id ? { llm_route_id: endpoint.llm_route_id } : {}),
      ...(endpoint.ml_endpoint_id ? { ml_endpoint_id: endpoint.ml_endpoint_id } : {}),
      ...(endpoint.worker_pubkey ? { worker_pubkey: endpoint.worker_pubkey } : {}),
      protocol: endpoint.protocol, port: endpoint.port, runtime: endpoint.runtime,
      hardware: endpoint.hardware, capabilities: endpoint.capabilities,
      health: endpoint.health, drift_status: endpoint.drift_status, metadata: endpoint.metadata,
      materialized_at: endpoint.materialized_at
    }), ...endpointForm, coordinate };
    return submit(endpoint ? 'endpoint-update' : 'endpoint-create', () => endpoint
      ? updateDNSEndpoint(endpoint, payload) : createDNSEndpoint(payload));
  }

  function saveBackend(event) {
    event.preventDefault();
    const payload = { ...(backend && { zone_refs: backend.zone_refs, last_sync_at: backend.last_sync_at,
      metadata: backend.metadata }),
      ...backendForm, ref: backend?.ref || backendForm.ref.trim() };
    return submit(backend ? 'backend-update' : 'backend-create', () => backend
      ? updateDNSBackend(backend, payload) : createDNSBackend(payload));
  }

  function savePolicy(event) {
    event.preventDefault();
    if (!policy) return;
    return submit('policy-update', () => {
      const rules = JSON.parse(policyForm.rules);
      if (!Array.isArray(rules)) throw new Error('Policy rules must be a JSON array');
      return updateDNSPolicy(policy, { id: policy.id, name: policyForm.name.trim(), enabled: policyForm.enabled,
        rules, ...(policy.zone_id ? { zone_id: policy.zone_id } : {}),
        ...(policy.environment_id ? { environment_id: policy.environment_id } : {}),
        ...(policy.metadata ? { metadata: policy.metadata } : {}),
        ...(policy.created_at ? { created_at: policy.created_at } : {}) });
    });
  }
</script>

<section class="panel" data-testid="dns-registry-mutations" aria-label="DNS registry mutations">
  <h2>Signed DNS registry intents</h2>
  <p>Changes remain pending until Bahia publishes a scoped intent status or canonical state. Deletes require the current canonical revision.</p>
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
  <div class="registry-forms">
    <form onsubmit={saveZone}>
      <h3>Edit zone</h3>
      <label>Existing zone<select value={zoneKey} onchange={selectZone} required><option value="">Select zone</option>{#each zones as row (row.name)}<option value={row.name}>{row.name}</option>{/each}</select></label>
      <label>Backend reference<input bind:value={zoneForm.backend_ref} required /></label>
      <label>Visibility<select bind:value={zoneForm.visibility}><option value="internal">Internal</option><option value="private">Private</option><option value="public">Public</option></select></label>
      <label>TTL<input type="number" min="1" bind:value={zoneForm.ttl} required /></label>
      <label class="inline"><input type="checkbox" bind:checked={zoneForm.authoritative} /> Authoritative</label>
      <label class="inline"><input type="checkbox" bind:checked={zoneForm.allow_empty_authoritative} /> Allow empty authoritative zone</label>
      <button type="submit" disabled={disabled || !zone || Boolean(submitting)}>Update zone</button>
      <button type="button" disabled={disabled || !zone || Boolean(submitting)} onclick={() => remove('zone-delete', zone.name, () => deleteDNSZone(zone))}>Delete zone</button>
    </form>
    <form onsubmit={saveEndpoint}>
      <h3>Endpoint</h3>
      <label>Existing endpoint<select value={endpointKey} onchange={selectEndpoint}><option value="">Create new</option>{#each endpoints as row (row.coordinate)}<option value={row.coordinate}>{row.fqdn || row.coordinate}</option>{/each}</select></label>
      <label>Family<select bind:value={endpointForm.family} disabled={Boolean(endpoint)}><option value="service">Service</option><option value="llm">LLM</option><option value="ml">ML</option><option value="worker">Worker</option><option value="mesh">Mesh</option></select></label>
      <label>Name<input bind:value={endpointForm.name} required disabled={Boolean(endpoint)} /></label>
      <label>Environment<input bind:value={endpointForm.environment} required={endpointForm.family !== 'worker'} disabled={Boolean(endpoint)} /></label>
      <label>Zone<input bind:value={endpointForm.zone} required /></label>
      <label>FQDN<input bind:value={endpointForm.fqdn} required /></label>
      <label>Address<input bind:value={endpointForm.address} required /></label>
      <label>Source<input bind:value={endpointForm.source} required /></label>
      <button type="submit" disabled={disabled || Boolean(submitting)}>{endpoint ? 'Update endpoint' : 'Create endpoint'}</button>
      {#if endpoint}<button type="button" disabled={disabled || Boolean(submitting)} onclick={() => remove('endpoint-delete', endpoint.fqdn, () => deleteDNSEndpoint(endpoint))}>Delete endpoint</button>{/if}
    </form>
    <form onsubmit={saveBackend}>
      <h3>Backend</h3>
      <label>Existing backend<select value={backendKey} onchange={selectBackend}><option value="">Create new</option>{#each backends as row (row.ref)}<option value={row.ref}>{row.ref}</option>{/each}</select></label>
      <label>Reference<input bind:value={backendForm.ref} required disabled={Boolean(backend)} /></label>
      <label>Type<select bind:value={backendForm.type}><option value="coredns">CoreDNS</option><option value="powerdns">PowerDNS</option><option value="dnsmasq">dnsmasq</option><option value="dnsmasq_agent">dnsmasq agent</option><option value="fips">FIPS</option><option value="consul">Consul</option><option value="etcd">etcd</option><option value="k8s_external_dns">K8s external DNS</option></select></label>
      <label>Health<select bind:value={backendForm.health}><option value="healthy">Healthy</option><option value="unhealthy">Unhealthy</option><option value="degraded">Degraded</option><option value="unknown">Unknown</option></select></label>
      <button type="submit" disabled={disabled || Boolean(submitting)}>{backend ? 'Update backend' : 'Create backend'}</button>
      {#if backend}<button type="button" disabled={disabled || Boolean(submitting)} onclick={() => remove('backend-delete', backend.ref, () => deleteDNSBackend(backend))}>Delete backend</button>{/if}
    </form>
    <form onsubmit={savePolicy}>
      <h3>Edit policy</h3>
      <label>Existing policy<select value={policyKey} onchange={selectPolicy} required><option value="">Select policy</option>{#each policies as row (row.id)}<option value={row.id}>{row.name}</option>{/each}</select></label>
      <label>Name<input bind:value={policyForm.name} required /></label>
      <label>Rules (JSON array)<textarea bind:value={policyForm.rules} required></textarea></label>
      <label class="inline"><input type="checkbox" bind:checked={policyForm.enabled} /> Enabled</label>
      <button type="submit" disabled={disabled || !policy || Boolean(submitting)}>Update policy</button>
      <button type="button" disabled={disabled || !policy || Boolean(submitting)} onclick={() => remove('policy-delete', policy.name, () => deleteDNSPolicy(policy))}>Delete policy</button>
    </form>
  </div>
</section>

<style>
  .panel { padding: 1rem; display: grid; gap: 1rem; background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 16px; }
  .panel p { color: var(--text-muted); }
  .registry-forms { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 1rem; }
  form, label { display: grid; gap: 0.4rem; }
  form { align-content: start; padding: 1rem; border: 1px solid var(--border-color); border-radius: 0.75rem; }
  label.inline { display: flex; align-items: center; }
  input, select, textarea { width: 100%; }
  label.inline input { width: auto; }
  button:disabled { opacity: 0.5; }
</style>
