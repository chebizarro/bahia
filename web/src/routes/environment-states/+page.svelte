<script>
  import { untrack } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Select from '$lib/components/Select.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import {
    states,
    services,
    environments,
    loadStates,
    loadServices,
    loadEnvironments
  } from '$lib/stores';
  import {
    ArtifactIcon,
    EnvironmentIcon,
    ServiceIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';

  let initialized = $state(false);
  let loading = $state(true);
  let error = $state(null);
  let driftFilter = $state('all');
  let selectedState = $state(null);
  let driftDialogOpen = $state(false);

  $effect(() => {
    if (initialized) return;
    initialized = true;
    void untrack(() => bootstrap());
  });

  async function bootstrap() {
    loading = true;
    error = null;
    try {
      await Promise.all([loadStates(), loadServices(), loadEnvironments()]);
    } catch (err) {
      error = err?.message || 'Failed to load environment states';
    } finally {
      loading = false;
    }
  }

  function escapeHtml(text) {
    return String(text ?? '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function serviceName(serviceId) {
    const svc = services.find((candidate) => candidate.id === serviceId);
    return svc?.name || svc?.display_name || (serviceId ? `${String(serviceId).slice(0, 12)}...` : '-');
  }

  function environmentName(environmentId) {
    const env = environments.find((candidate) => candidate.id === environmentId);
    return env?.name || env?.slug || (environmentId ? `${String(environmentId).slice(0, 12)}...` : '-');
  }

  function driftVariant(status) {
    const value = String(status || 'unknown').toLowerCase();
    if (value === 'in_sync') return 'success';
    if (value === 'drifted') return 'error';
    return 'default';
  }

  function truncateId(value) {
    return value ? `${String(value).slice(0, 12)}...` : '-';
  }

  function formatTimestamp(value) {
    if (!value) return '-';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
  }

  function openDriftDialog(row) {
    if (!row) return;
    selectedState = row;
    driftDialogOpen = true;
  }

  function closeDriftDialog() {
    driftDialogOpen = false;
    selectedState = null;
  }

  function handleRowClick(row, event) {
    if (event?.target?.closest?.('[data-state-action="drift"]')) {
      openDriftDialog(row);
    }
  }

  function serializeState(row) {
    if (!row) return '';
    try {
      return JSON.stringify(row, null, 2);
    } catch {
      return String(row);
    }
  }

  let filteredStates = $derived(
    states.filter((state) => {
      if (driftFilter === 'all') return true;
      return String(state.drift_status || 'unknown').toLowerCase() === driftFilter;
    })
  );

  let driftedCount = $derived(
    states.filter((state) => String(state.drift_status || '').toLowerCase() === 'drifted').length
  );

  const driftFilterOptions = [
    { value: 'all', label: 'All states' },
    { value: 'drifted', label: 'Drifted' },
    { value: 'in_sync', label: 'In sync' }
  ];

  let columns = $derived([
    { key: 'service_id', label: 'Service', icon: ServiceIcon, text: (r) => serviceName(r.service_id) },
    { key: 'environment_id', label: 'Environment', icon: EnvironmentIcon, text: (r) => environmentName(r.environment_id) },
    { key: 'artifact_id', label: 'Artifact', icon: ArtifactIcon, text: (r) => truncateId(r.artifact_id) },
    { key: 'status', label: 'Status', text: (r) => r.status || '-' },
    {
      key: 'drift_status',
      label: 'Drift',
      render: (r) => {
        const driftStatus = String(r.drift_status || 'unknown');
        const variant = driftVariant(driftStatus);
        return `<span class="drift-cell" data-state-action="drift" tabindex="0" title="View drift details"><span class="badge-${variant}">${escapeHtml(driftStatus)}</span></span>`;
      }
    },
    { key: 'deployed_at', label: 'Deployed', render: (r) => escapeHtml(formatTimestamp(r.deployed_at)) }
  ]);

  let selectedDriftVariant = $derived(driftVariant(selectedState?.drift_status));
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1 class="title-with-icon">
        <WarningIcon size={28} strokeWidth={1.75} ariaHidden="true" />
        <span>Environment States</span>
      </h1>
      <span class="count">{driftedCount} drifted / {states.length} total</span>
    </div>
    <a href="/" class="back-link">← Back to Dashboard</a>
  </div>

  <p class="lead">
    Live reconciliation status for every service projected onto an environment. Click a row's
    <strong>Drift</strong> cell to inspect the full state payload.
  </p>

  <div class="filters">
    <div class="filter-field">
      <label for="drift-filter">Drift status</label>
      <Select id="drift-filter" bind:value={driftFilter} options={driftFilterOptions} disabled={loading} />
    </div>
  </div>

  {#if loading}
    <p class="loading">Loading environment states…</p>
  {:else if error}
    <EmptyState iconComponent={WarningIcon} title="Unable to load environment states" message={error} />
  {:else if states.length === 0}
    <EmptyState
      iconComponent={EnvironmentIcon}
      title="No environment states yet"
      message="Environment states will appear here once the relay-backed control plane projects reconciliation status."
    />
  {:else if filteredStates.length === 0}
    <EmptyState
      iconComponent={EnvironmentIcon}
      title="No states match this filter"
      message="Try switching the drift status filter."
    />
  {:else}
    <Table {columns} data={filteredStates} onRowClick={handleRowClick} rowClickable={false} />
  {/if}
</div>

<Modal
  bind:open={driftDialogOpen}
  title="Drift Details"
  titleIcon={WarningIcon}
  size="lg"
  onClose={closeDriftDialog}
>
  {#if selectedState}
    <div class="drift-detail">
      <dl>
        <div>
          <dt>Service</dt>
          <dd>{serviceName(selectedState.service_id)}</dd>
        </div>
        <div>
          <dt>Environment</dt>
          <dd>{environmentName(selectedState.environment_id)}</dd>
        </div>
        <div>
          <dt>Drift Status</dt>
          <dd><span class="badge-{selectedDriftVariant}">{selectedState.drift_status || 'unknown'}</span></dd>
        </div>
        {#if selectedState.status}
          <div>
            <dt>Status</dt>
            <dd>{selectedState.status}</dd>
          </div>
        {/if}
        {#if selectedState.artifact_id}
          <div>
            <dt>Artifact</dt>
            <dd><code>{selectedState.artifact_id}</code></dd>
          </div>
        {/if}
        {#if selectedState.deployed_at}
          <div>
            <dt>Deployed</dt>
            <dd>{formatTimestamp(selectedState.deployed_at)}</dd>
          </div>
        {/if}
      </dl>
      <pre class="drift-json">{serializeState(selectedState)}</pre>
    </div>
  {/if}
</Modal>

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 0.5rem;
    flex-wrap: wrap;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }
  .title-with-icon {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
  }
  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
  .back-link {
    color: var(--primary);
    text-decoration: none;
    font-size: 0.875rem;
  }
  .back-link:hover {
    text-decoration: underline;
  }
  .lead {
    color: var(--text-muted);
    font-size: 0.9rem;
    margin: 0 0 1.5rem;
  }
  .filters {
    display: grid;
    gap: 1rem;
    margin-bottom: 1.5rem;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    max-width: 260px;
  }
  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .filter-field label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }
  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }
  :global(.drift-cell) {
    cursor: pointer;
    display: inline-flex;
    border-radius: 4px;
  }
  :global(.drift-cell:hover),
  :global(.drift-cell:focus-visible) {
    outline: 2px solid color-mix(in srgb, var(--primary) 45%, transparent);
    outline-offset: 2px;
  }
  .drift-detail {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .drift-detail dl {
    display: grid;
    gap: 0.75rem;
    margin: 0;
  }
  .drift-detail dl > div {
    display: grid;
    grid-template-columns: 130px 1fr;
    gap: 0.75rem;
  }
  .drift-detail dt {
    color: var(--text-muted);
    font-size: 0.8rem;
  }
  .drift-detail dd {
    margin: 0;
    color: var(--text-primary);
    font-size: 0.9rem;
    word-break: break-word;
  }
  .drift-json {
    margin: 0;
    padding: 1rem;
    border-radius: 8px;
    border: 1px solid var(--border-color);
    background: var(--hover-bg);
    color: var(--text-primary);
    font-size: 0.8rem;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-word;
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
</style>
