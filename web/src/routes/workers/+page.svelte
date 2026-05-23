<script>
  import { workers, loading, loadWorkers } from '$lib/stores';
  import { goto } from '$app/navigation';
  import { StandardIcon } from '$lib/icons/domain-icons.js';
  import { publishCommand, resultContent } from '$lib/stores/public-controlplane.svelte.js';
  import { currentRequesterPubkey } from '$lib/nostr/controlplane-requests.js';
  import {
    inferWorkerStatus,
    workerFormatsLabel,
    workerToolchainsLabel,
    workerVRAMLabel,
    workerPriceLabel,
    workerLastAdvertisementLabel
  } from './list-utils.js';

  const SCHEDULING_STATES = ['active', 'cordoned', 'draining', 'maintenance', 'disabled'];
  const WORKER_KINDS = {
    CORDON_REQUEST: 5997,
    UNCORDON_REQUEST: 5998,
    DRAIN_REQUEST: 5999,
    UNDRAIN_REQUEST: 6000,
    MAINTENANCE_ENTER_REQUEST: 6001,
    MAINTENANCE_EXIT_REQUEST: 6002,
    LABELS_UPDATE_REQUEST: 6003,
    RESULT: 7997
  };

  const WORKER_COMMANDS = {
    CORDON: 'worker.cordon.request',
    UNCORDON: 'worker.uncordon.request',
    DRAIN: 'worker.drain.request',
    UNDRAIN: 'worker.undrain.request',
    MAINTENANCE_ENTER: 'worker.maintenance.enter.request',
    MAINTENANCE_EXIT: 'worker.maintenance.exit.request',
    LABELS_UPDATE: 'worker.labels.update.request'
  };

  const WORKER_ACTIONS = [
    {
      label: 'Cordon',
      command: WORKER_COMMANDS.CORDON,
      kind: WORKER_KINDS.CORDON_REQUEST,
      reasonPrompt: 'Reason for cordoning this worker (optional)',
      allowedFrom: ['active']
    },
    {
      label: 'Uncordon',
      command: WORKER_COMMANDS.UNCORDON,
      kind: WORKER_KINDS.UNCORDON_REQUEST,
      reasonPrompt: 'Reason for uncordoning this worker (optional)',
      allowedFrom: ['cordoned']
    },
    {
      label: 'Drain',
      command: WORKER_COMMANDS.DRAIN,
      kind: WORKER_KINDS.DRAIN_REQUEST,
      reasonPrompt: 'Reason for draining this worker (optional)',
      allowedFrom: ['active', 'cordoned']
    },
    {
      label: 'Cancel drain',
      command: WORKER_COMMANDS.UNDRAIN,
      kind: WORKER_KINDS.UNDRAIN_REQUEST,
      reasonPrompt: 'Reason for canceling drain (optional)',
      allowedFrom: ['draining']
    },
    {
      label: 'Enter maintenance',
      command: WORKER_COMMANDS.MAINTENANCE_ENTER,
      kind: WORKER_KINDS.MAINTENANCE_ENTER_REQUEST,
      reasonPrompt: 'Reason for entering maintenance (optional)',
      allowedFrom: ['active', 'cordoned', 'draining']
    },
    {
      label: 'Exit maintenance',
      command: WORKER_COMMANDS.MAINTENANCE_EXIT,
      kind: WORKER_KINDS.MAINTENANCE_EXIT_REQUEST,
      reasonPrompt: 'Reason for exiting maintenance (optional)',
      allowedFrom: ['maintenance']
    },
    {
      label: 'Edit labels',
      command: WORKER_COMMANDS.LABELS_UPDATE,
      kind: WORKER_KINDS.LABELS_UPDATE_REQUEST,
      labels: true,
      allowedFrom: SCHEDULING_STATES
    }
  ];

  let capabilityFilter = $state('');
  let capabilitySearch = $state('');
  let schedulingFilter = $state('');
  let runtimeFilter = $state('');
  let formatFilter = $state('');
  let acceleratorFilter = $state('');
  let toolchainFilter = $state('');
  let taskFilter = $state('');
  let labelKeyFilter = $state('');
  let labelValueFilter = $state('');
  let onlineOnly = $state(false);
  let pendingCommands = $state({});
  let notice = $state(null);

  $effect(() => {
    void loadWorkers();
  });

  const capabilityOptions = $derived(collectWorkerValues(workers, workerCapabilityValues));
  const runtimeOptions = $derived(collectWorkerValues(workers, workerRuntimeValues));
  const formatOptions = $derived(collectWorkerValues(workers, workerFormatValues));
  const acceleratorOptions = $derived(collectWorkerValues(workers, workerAcceleratorValues));
  const toolchainOptions = $derived(collectWorkerValues(workers, workerToolchainValues));
  const taskOptions = $derived(collectWorkerValues(workers, workerWorkloadValues));

  const filteredWorkers = $derived.by(() => filterWorkerRows(workers, {
    capabilityFilter,
    capabilitySearch,
    schedulingFilter,
    runtimeFilter,
    formatFilter,
    acceleratorFilter,
    toolchainFilter,
    taskFilter,
    labelKeyFilter,
    labelValueFilter,
    onlineOnly
  }));

  function normalizeList(value) {
    if (!Array.isArray(value)) return [];
    return value.map((entry) => String(entry || '').trim()).filter(Boolean);
  }

  function genericCapabilities(worker) {
    return worker?.capabilities && typeof worker.capabilities === 'object' ? worker.capabilities : {};
  }

  function mlCapabilities(worker) {
    return worker?.ml_capabilities && typeof worker.ml_capabilities === 'object' ? worker.ml_capabilities : {};
  }

  function uniqueSorted(values) {
    return Array.from(new Set(values.map((value) => String(value || '').trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b));
  }

  function workerWorkloadValues(worker) {
    const generic = genericCapabilities(worker);
    const mlCap = mlCapabilities(worker);
    return uniqueSorted([
      ...normalizeList(generic.workload_kinds),
      ...normalizeList(mlCap.tasks)
    ]);
  }

  function workerRuntimeValues(worker) {
    const generic = genericCapabilities(worker);
    const mlCap = mlCapabilities(worker);
    return uniqueSorted([
      ...normalizeList(generic.runtimes),
      ...normalizeList(mlCap.runtimes)
    ]);
  }

  function workerFormatValues(worker) {
    const generic = genericCapabilities(worker);
    const mlCap = mlCapabilities(worker);
    return uniqueSorted([
      ...normalizeList(generic.artifact_formats),
      ...normalizeList(mlCap.artifact_formats)
    ]);
  }

  function workerToolchainValues(worker) {
    const generic = genericCapabilities(worker);
    const mlCap = mlCapabilities(worker);
    return uniqueSorted([
      ...normalizeList(generic.toolchains),
      ...normalizeList(mlCap.toolchains)
    ]);
  }

  function workerAcceleratorValues(worker) {
    const generic = genericCapabilities(worker);
    const mlCap = mlCapabilities(worker);
    const accelerators = Array.isArray(worker?.accelerators) ? worker.accelerators : [];
    const hardware = accelerators
      .flatMap((accelerator) => [accelerator?.model, accelerator?.vendor])
      .filter(Boolean);
    return uniqueSorted([
      ...normalizeList(generic.accelerators),
      ...normalizeList(mlCap.accelerators),
      ...hardware
    ]);
  }

  function workerCapabilityValues(worker) {
    return uniqueSorted([
      ...(Array.isArray(worker?.software) ? worker.software : []).map((entry) => entry?.name).filter(Boolean),
      ...workerWorkloadValues(worker),
      ...workerRuntimeValues(worker),
      ...workerFormatValues(worker),
      ...workerAcceleratorValues(worker),
      ...workerToolchainValues(worker)
    ]);
  }

  function collectWorkerValues(sourceWorkers, getter) {
    const values = [];
    for (const worker of sourceWorkers || []) values.push(...getter(worker));
    return uniqueSorted(values);
  }

  function schedulingState(worker) {
    const value = String(worker?.scheduling_state || worker?.schedulingState || '').trim().toLowerCase();
    return SCHEDULING_STATES.includes(value) ? value : 'active';
  }

  function workerLabels(worker) {
    return worker?.labels && typeof worker.labels === 'object' && !Array.isArray(worker.labels) ? worker.labels : {};
  }

  function workerLabelEntries(worker) {
    return Object.entries(workerLabels(worker))
      .filter(([key]) => String(key || '').trim().length > 0)
      .sort(([left], [right]) => left.localeCompare(right));
  }

  function hasLabelMatch(worker, keyFilter, valueFilter) {
    const keyQuery = String(keyFilter || '').trim().toLowerCase();
    const valueQuery = String(valueFilter || '').trim().toLowerCase();
    if (!keyQuery && !valueQuery) return true;

    return workerLabelEntries(worker).some(([key, value]) => {
      const keyMatches = !keyQuery || key.toLowerCase().includes(keyQuery);
      const valueMatches = !valueQuery || String(value || '').toLowerCase().includes(valueQuery);
      return keyMatches && valueMatches;
    });
  }

  function filterWorkerRows(sourceWorkers, filters) {
    const query = String(filters.capabilitySearch || '').trim().toLowerCase();
    const selectedCapability = String(filters.capabilityFilter || '').trim().toLowerCase();

    return (sourceWorkers || []).filter((worker) => {
      const liveness = inferWorkerStatus(worker);
      if (filters.onlineOnly && liveness !== 'online') return false;
      if (filters.schedulingFilter && schedulingState(worker) !== filters.schedulingFilter) return false;

      const capabilities = workerCapabilityValues(worker);
      const normalizedCapabilities = capabilities.map((capability) => capability.toLowerCase());
      if (selectedCapability && !normalizedCapabilities.includes(selectedCapability)) return false;
      if (filters.runtimeFilter && !workerRuntimeValues(worker).includes(filters.runtimeFilter)) return false;
      if (filters.formatFilter && !workerFormatValues(worker).includes(filters.formatFilter)) return false;
      if (filters.acceleratorFilter && !workerAcceleratorValues(worker).includes(filters.acceleratorFilter)) return false;
      if (filters.toolchainFilter && !workerToolchainValues(worker).includes(filters.toolchainFilter)) return false;
      if (filters.taskFilter && !workerWorkloadValues(worker).includes(filters.taskFilter)) return false;
      if (!hasLabelMatch(worker, filters.labelKeyFilter, filters.labelValueFilter)) return false;

      if (!query) return true;

      const labelText = workerLabelEntries(worker).map(([key, value]) => `${key}=${value}`).join(' ');
      const searchable = [
        ...capabilities,
        ...workerRuntimeValues(worker),
        ...workerFormatValues(worker),
        ...workerAcceleratorValues(worker),
        ...workerToolchainValues(worker),
        ...workerWorkloadValues(worker),
        schedulingState(worker),
        labelText,
        worker.name || '',
        worker.description || '',
        worker.architecture || '',
        worker.pubkey || ''
      ].join(' ').toLowerCase();

      return searchable.includes(query);
    });
  }

  function formatList(values) {
    return uniqueSorted(values).join(', ') || '-';
  }

  function workerWorkloadsLabel(worker) {
    return formatList(workerWorkloadValues(worker));
  }

  function workerRuntimesLabel(worker) {
    return formatList(workerRuntimeValues(worker));
  }

  function workerAcceleratorsLabel(worker) {
    return formatList(workerAcceleratorValues(worker));
  }

  function labelsText(worker) {
    const entries = workerLabelEntries(worker);
    return entries.length ? entries.map(([key, value]) => `${key}=${value}`).join('\n') : '';
  }

  function parseLabelsInput(input) {
    const labels = {};
    for (const rawLine of String(input || '').split(/\n/)) {
      const line = rawLine.trim();
      if (!line) continue;
      const separator = line.indexOf('=');
      if (separator <= 0) {
        throw new Error(`Label entry "${line}" must use key=value syntax`);
      }
      const key = line.slice(0, separator).trim();
      const value = line.slice(separator + 1).trim();
      if (!key) throw new Error('Label keys must not be empty');
      labels[key] = value;
    }
    return labels;
  }

  function randomId() {
    const cryptoApi = globalThis.crypto;
    if (cryptoApi?.randomUUID) return cryptoApi.randomUUID();
    if (cryptoApi?.getRandomValues) {
      const bytes = new Uint8Array(16);
      cryptoApi.getRandomValues(bytes);
      return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
    }
    throw new Error('Browser cryptographic random ID generation is unavailable');
  }

  function idempotencyKey(action, worker) {
    return `${action.command}:${worker.pubkey}:${randomId()}`;
  }

  function commandTags(action, worker, key) {
    return [
      ['d', key],
      ['worker', worker.pubkey],
      ['command', action.command]
    ];
  }

  function commandContent(action, worker, key, reason, labels = null) {
    const content = {
      worker_pubkey: worker.pubkey,
      reason: reason || '',
      idempotency_key: key,
      operator_metadata: {
        source: 'web.workers.list',
        requested_by: currentRequesterPubkey() || ''
      }
    };
    if (labels) content.labels = labels;
    return content;
  }

  function actionPendingKey(worker, action) {
    return `${worker.pubkey}:${action.command}`;
  }

  function isActionPending(worker, action) {
    return Boolean(pendingCommands[actionPendingKey(worker, action)]);
  }

  function isWorkerPublishPending(worker) {
    const prefix = `${worker.pubkey}:`;
    return Object.keys(pendingCommands).some((key) => key.startsWith(prefix));
  }

  function setActionPending(worker, action, value) {
    const key = actionPendingKey(worker, action);
    if (value) {
      pendingCommands = { ...pendingCommands, [key]: true };
      return;
    }
    const { [key]: _removed, ...remaining } = pendingCommands;
    pendingCommands = remaining;
  }

  function isWorkerActionAllowed(worker, action) {
    return action.allowedFrom.includes(schedulingState(worker));
  }

  function resetNotice() {
    notice = null;
  }

  function setNotice(type, message) {
    notice = { type, message };
  }

  async function publishWorkerAction(worker, action, reason, labels = null) {
    if (!worker?.pubkey) throw new Error('Worker pubkey is required');
    const key = idempotencyKey(action, worker);
    const result = await publishCommand({
      kind: action.kind,
      tags: commandTags(action, worker, key),
      content: commandContent(action, worker, key, reason, labels),
      resultKinds: [WORKER_KINDS.RESULT]
    });
    return resultContent(result);
  }

  async function handleWorkerAction(event, worker, action) {
    event.stopPropagation();
    if (!isWorkerActionAllowed(worker, action) || isWorkerPublishPending(worker)) return;

    let reason = '';
    let labels = null;
    try {
      if (action.labels) {
        const input = globalThis.prompt?.('Worker labels as key=value lines. Empty input clears labels.', labelsText(worker));
        if (input === null || input === undefined) return;
        labels = parseLabelsInput(input);
        reason = 'Operator label update from Workers page';
      } else if (action.reasonPrompt) {
        const input = globalThis.prompt?.(action.reasonPrompt, '');
        if (input === null || input === undefined) return;
        reason = input.trim();
      }

      setActionPending(worker, action, true);
      resetNotice();
      const content = await publishWorkerAction(worker, action, reason, labels);
      setNotice('success', content.message || `${action.label} command accepted for ${worker.name || worker.pubkey}`);
    } catch (err) {
      setNotice('error', err?.message || `Failed to publish ${action.label} command`);
    } finally {
      setActionPending(worker, action, false);
    }
  }

  async function copySelector(event, worker) {
    event.stopPropagation();
    resetNotice();
    const selector = `worker.pubkey=${worker.pubkey}`;
    try {
      if (!navigator?.clipboard?.writeText) throw new Error('Clipboard API is unavailable');
      await navigator.clipboard.writeText(selector);
      setNotice('success', `Copied selector ${selector}`);
    } catch (err) {
      setNotice('error', err?.message || 'Failed to copy worker selector');
    }
  }
</script>

<div class="page">
  <div class="header">
    <h1>
      <StandardIcon size={28} strokeWidth={1.75} ariaHidden="true" />
      Workers
    </h1>
    <span class="count">{filteredWorkers.length} of {workers.length} workers</span>
  </div>
  <p class="subtitle">Shared execution pool for CI/CD, inference, and scheduled compute workloads.</p>

  {#if notice}
    <div class={`notice ${notice.type}`} role="status">{notice.message}</div>
  {/if}

  <div class="filters">
    <label>
      <span>Search</span>
      <input bind:value={capabilitySearch} type="search" placeholder="Search workers, labels, capabilities…" />
    </label>

    <label>
      <span>Scheduling</span>
      <select bind:value={schedulingFilter}>
        <option value="">All scheduling states</option>
        {#each SCHEDULING_STATES as state}
          <option value={state}>{state}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Capability</span>
      <select bind:value={capabilityFilter}>
        <option value="">All capabilities</option>
        {#each capabilityOptions as capability}
          <option value={capability}>{capability}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Runtime</span>
      <select bind:value={runtimeFilter}>
        <option value="">All runtimes</option>
        {#each runtimeOptions as runtime}
          <option value={runtime}>{runtime}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Task Type / Workload</span>
      <select bind:value={taskFilter}>
        <option value="">All workload kinds</option>
        {#each taskOptions as task}
          <option value={task}>{task}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Accelerator</span>
      <select bind:value={acceleratorFilter}>
        <option value="">All accelerators</option>
        {#each acceleratorOptions as accelerator}
          <option value={accelerator}>{accelerator}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Artifact Format</span>
      <select bind:value={formatFilter}>
        <option value="">All formats</option>
        {#each formatOptions as format}
          <option value={format}>{format}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Toolchain</span>
      <select bind:value={toolchainFilter}>
        <option value="">All toolchains</option>
        {#each toolchainOptions as toolchain}
          <option value={toolchain}>{toolchain}</option>
        {/each}
      </select>
    </label>

    <label>
      <span>Label key</span>
      <input bind:value={labelKeyFilter} type="search" placeholder="role" />
    </label>

    <label>
      <span>Label value</span>
      <input bind:value={labelValueFilter} type="search" placeholder="inference" />
    </label>

    <label class="toggle-filter">
      <input bind:checked={onlineOnly} type="checkbox" />
      <span>Online only</span>
    </label>
  </div>

  {#if loading.workers}
    <p class="loading">Loading...</p>
  {:else}
    <div class="table-container">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Pubkey</th>
            <th>Status</th>
            <th>Labels</th>
            <th>Supported Workloads</th>
            <th>Runtimes</th>
            <th>Accelerators</th>
            <th>Formats</th>
            <th>Toolchains</th>
            <th>VRAM</th>
            <th>Pricing</th>
            <th>Last Advertisement</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {#each filteredWorkers as worker}
            {@const liveness = inferWorkerStatus(worker)}
            {@const scheduling = schedulingState(worker)}
            <tr>
              <td><a class="worker-link" href={`/workers/${encodeURIComponent(worker.pubkey)}`}>{worker.name || '-'}</a></td>
              <td><code>{worker.pubkey?.slice(0, 12)}...</code></td>
              <td>
                <div class="status-stack" aria-label={`Liveness ${liveness}; scheduling ${scheduling}`}>
                  <span class={`status-badge liveness-${liveness}`}>
                    <span class="status-dot" aria-hidden="true"></span>{liveness}
                  </span>
                  <span class={`status-badge scheduling-${scheduling}`}>{scheduling}</span>
                </div>
              </td>
              <td>
                {#if workerLabelEntries(worker).length === 0}
                  <span class="muted">-</span>
                {:else}
                  <div class="label-list">
                    {#each workerLabelEntries(worker) as [key, value]}
                      <span class="label-chip"><strong>{key}</strong>={value}</span>
                    {/each}
                  </div>
                {/if}
              </td>
              <td>{workerWorkloadsLabel(worker)}</td>
              <td>{workerRuntimesLabel(worker)}</td>
              <td>{workerAcceleratorsLabel(worker)}</td>
              <td>{workerFormatsLabel(worker)}</td>
              <td>{workerToolchainsLabel(worker)}</td>
              <td>{workerVRAMLabel(worker)}</td>
              <td>{workerPriceLabel(worker)}</td>
              <td>{workerLastAdvertisementLabel(worker)}</td>
              <td>
                <details class="action-menu">
                  <summary>Actions</summary>
                  <div class="action-menu-panel">
                    <button type="button" onclick={() => goto(`/workers/${encodeURIComponent(worker.pubkey)}`)}>View details</button>
                    {#each WORKER_ACTIONS as action}
                      {@const disabled = isWorkerPublishPending(worker) || !isWorkerActionAllowed(worker, action)}
                      <button
                        type="button"
                        disabled={disabled}
                        title={!isWorkerActionAllowed(worker, action) ? `${action.label} is not valid from ${scheduling}` : ''}
                        onclick={(event) => handleWorkerAction(event, worker, action)}
                      >
                        {isActionPending(worker, action) ? 'Publishing…' : action.label}
                      </button>
                    {/each}
                    <button type="button" onclick={(event) => copySelector(event, worker)}>Copy selector</button>
                  </div>
                </details>
              </td>
            </tr>
          {/each}
          {#if filteredWorkers.length === 0}
            <tr><td colspan="13" class="empty">No workers match the selected filters</td></tr>
          {/if}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .header { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem; }
  h1 { display: inline-flex; align-items: center; gap: 0.5rem; }
  h1 :global(svg) { display: block; flex-shrink: 0; }
  .count { color: var(--text-muted); font-size: 0.875rem; }
  .subtitle { color: var(--text-muted); margin: -0.75rem 0 1.25rem; }
  .loading, .empty { color: var(--text-muted); padding: 2rem; text-align: center; }
  .muted { color: var(--text-muted); }

  .notice {
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 0.5rem;
    padding: 0.75rem 1rem;
    margin-bottom: 1rem;
  }

  .notice.success { border-color: rgba(34, 197, 94, 0.5); color: #86efac; background: rgba(34, 197, 94, 0.08); }
  .notice.error { border-color: rgba(239, 68, 68, 0.5); color: #fca5a5; background: rgba(239, 68, 68, 0.08); }

  .filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
    margin-bottom: 1rem;
  }

  .filters label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .filters select,
  .filters input:not([type='checkbox']) {
    padding: 0.5rem 0.625rem;
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 0.375rem;
    background: var(--surface-bg, #141426);
    color: var(--text-color, #fff);
  }

  .toggle-filter {
    justify-content: end;
    flex-direction: row !important;
    align-items: center;
    padding: 1.35rem 0 0.4rem;
  }

  .table-container { overflow-x: auto; }

  table {
    width: 100%;
    border-collapse: collapse;
  }

  th, td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid var(--border-color, #2a2a4a);
    vertical-align: top;
  }

  th {
    background: var(--card-bg, #1a1a2e);
    font-weight: 600;
    font-size: 0.75rem;
    text-transform: uppercase;
    color: var(--text-muted, #888);
  }

  code {
    background: var(--surface-bg, #141426);
    border-radius: 4px;
    padding: 0.15rem 0.35rem;
  }

  .worker-link {
    color: var(--primary, #8b5cf6);
    font-weight: 600;
    text-decoration: none;
  }

  .worker-link:hover { text-decoration: underline; }

  .status-stack, .label-list {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
  }

  .status-badge, .label-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 999px;
    padding: 0.18rem 0.55rem;
    background: var(--surface-bg, #141426);
    color: var(--text-color, #fff);
    font-size: 0.78rem;
    text-transform: capitalize;
    white-space: nowrap;
  }

  .label-chip {
    text-transform: none;
    color: var(--text-muted);
  }

  .label-chip strong { color: var(--text-color, #fff); }

  .status-dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 999px;
    display: inline-block;
  }

  .liveness-online .status-dot { background: #22c55e; }
  .liveness-stale .status-dot { background: #f59e0b; }
  .liveness-offline .status-dot { background: #ef4444; }

  .scheduling-active { border-color: rgba(34, 197, 94, 0.45); color: #86efac; }
  .scheduling-cordoned { border-color: rgba(245, 158, 11, 0.5); color: #fcd34d; }
  .scheduling-draining { border-color: rgba(59, 130, 246, 0.5); color: #93c5fd; }
  .scheduling-maintenance { border-color: rgba(168, 85, 247, 0.5); color: #d8b4fe; }
  .scheduling-disabled { border-color: rgba(239, 68, 68, 0.5); color: #fca5a5; }

  .action-menu { position: relative; min-width: 8rem; }
  .action-menu summary { cursor: pointer; color: var(--primary, #8b5cf6); }
  .action-menu-panel {
    position: absolute;
    right: 0;
    z-index: 10;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    min-width: 12rem;
    margin-top: 0.35rem;
    padding: 0.5rem;
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 0.5rem;
    background: var(--card-bg, #1a1a2e);
    box-shadow: 0 12px 30px rgba(0, 0, 0, 0.35);
  }

  .action-menu-panel button {
    border: 0;
    border-radius: 0.375rem;
    background: transparent;
    color: var(--text-color, #fff);
    cursor: pointer;
    padding: 0.45rem 0.55rem;
    text-align: left;
  }

  .action-menu-panel button:hover:not(:disabled) { background: var(--hover-bg, #252540); }
  .action-menu-panel button:disabled { color: var(--text-muted, #888); cursor: not-allowed; }
</style>
