<script>
  import { page } from '$app/state';
  import Table from '$lib/components/Table.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { StandardIcon } from '$lib/icons/domain-icons.js';
  import { workers, loadWorkers } from '$lib/stores';
  import { publishCommand, resultContent } from '$lib/stores/public-controlplane.svelte.js';
  import { currentRequesterPubkey } from '$lib/nostr/controlplane-requests.js';
  import {
    WORKER_CORDON_REQUEST,
    WORKER_UNCORDON_REQUEST,
    WORKER_DRAIN_REQUEST,
    WORKER_UNDRAIN_REQUEST,
    WORKER_MAINTENANCE_ENTER,
    WORKER_MAINTENANCE_EXIT,
    WORKER_LABELS_UPDATE,
    WORKER_RESULT
  } from '$lib/nostr/kinds.gen.js';
  import { inferWorkerStatus } from '../list-utils.js';

  const SCHEDULING_STATES = ['active', 'cordoned', 'draining', 'maintenance', 'disabled'];
  const WORKER_KINDS = {
    CORDON_REQUEST: WORKER_CORDON_REQUEST,
    UNCORDON_REQUEST: WORKER_UNCORDON_REQUEST,
    DRAIN_REQUEST: WORKER_DRAIN_REQUEST,
    UNDRAIN_REQUEST: WORKER_UNDRAIN_REQUEST,
    MAINTENANCE_ENTER_REQUEST: WORKER_MAINTENANCE_ENTER,
    MAINTENANCE_EXIT_REQUEST: WORKER_MAINTENANCE_EXIT,
    LABELS_UPDATE_REQUEST: WORKER_LABELS_UPDATE,
    RESULT: WORKER_RESULT
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
  const LABEL_ACTION = WORKER_ACTIONS.find((action) => action.labels);
  const QUICK_ACTIONS = WORKER_ACTIONS.filter((action) => !action.labels);

  let worker = $state(null);
  let loading = $state(true);
  let error = $state(null);
  let notice = $state(null);
  let pendingCommands = $state({});
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

  function normalizeList(value) {
    if (!Array.isArray(value)) return [];
    return value.map((entry) => String(entry || '').trim()).filter(Boolean);
  }

  function uniqueSorted(values) {
    return Array.from(new Set(values.map((value) => String(value || '').trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b));
  }

  function genericCapabilities(sourceWorker) {
    return sourceWorker?.capabilities && typeof sourceWorker.capabilities === 'object' ? sourceWorker.capabilities : {};
  }

  function mlCapabilitiesFor(sourceWorker) {
    return sourceWorker?.ml_capabilities && typeof sourceWorker.ml_capabilities === 'object' ? sourceWorker.ml_capabilities : {};
  }

  function workerWorkloadValues(sourceWorker) {
    const generic = genericCapabilities(sourceWorker);
    const ml = mlCapabilitiesFor(sourceWorker);
    return uniqueSorted([
      ...normalizeList(generic.workload_kinds),
      ...normalizeList(ml.tasks)
    ]);
  }

  function workerRuntimeValues(sourceWorker) {
    const generic = genericCapabilities(sourceWorker);
    const ml = mlCapabilitiesFor(sourceWorker);
    return uniqueSorted([
      ...normalizeList(generic.runtimes),
      ...normalizeList(ml.runtimes)
    ]);
  }

  function workerFormatValues(sourceWorker) {
    const generic = genericCapabilities(sourceWorker);
    const ml = mlCapabilitiesFor(sourceWorker);
    return uniqueSorted([
      ...normalizeList(generic.artifact_formats),
      ...normalizeList(ml.artifact_formats)
    ]);
  }

  function workerToolchainValues(sourceWorker) {
    const generic = genericCapabilities(sourceWorker);
    const ml = mlCapabilitiesFor(sourceWorker);
    return uniqueSorted([
      ...normalizeList(generic.toolchains),
      ...normalizeList(ml.toolchains)
    ]);
  }

  function workerAcceleratorValues(sourceWorker) {
    const generic = genericCapabilities(sourceWorker);
    const ml = mlCapabilitiesFor(sourceWorker);
    const hardware = (sourceWorker?.accelerators || [])
      .flatMap((accelerator) => [accelerator?.model, accelerator?.vendor])
      .filter(Boolean);
    return uniqueSorted([
      ...normalizeList(generic.accelerators),
      ...normalizeList(ml.accelerators),
      ...hardware
    ]);
  }

  function workerFeatureValues(sourceWorker) {
    return uniqueSorted([
      ...normalizeList(genericCapabilities(sourceWorker).features),
      ...normalizeList(mlCapabilitiesFor(sourceWorker).features)
    ]);
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
    return uniqueSorted(values || []).join(', ') || 'Not advertised';
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

  function schedulingState(sourceWorker) {
    const value = String(sourceWorker?.scheduling_state || sourceWorker?.schedulingState || '').trim().toLowerCase();
    return SCHEDULING_STATES.includes(value) ? value : 'active';
  }

  function schedulingNote(sourceWorker, drainStatus) {
    return sourceWorker?.scheduling_note || sourceWorker?.scheduling_reason || drainStatus?.reason || 'No scheduling note recorded';
  }

  function acceptingNewWork(sourceWorker) {
    return schedulingState(sourceWorker) === 'active' && inferWorkerStatus(sourceWorker) === 'online';
  }

  function workerLabels(sourceWorker) {
    return sourceWorker?.labels && typeof sourceWorker.labels === 'object' && !Array.isArray(sourceWorker.labels) ? sourceWorker.labels : {};
  }

  function workerLabelEntries(sourceWorker) {
    return Object.entries(workerLabels(sourceWorker))
      .filter(([key]) => String(key || '').trim().length > 0)
      .sort(([left], [right]) => left.localeCompare(right));
  }

  function labelsText(sourceWorker) {
    const entries = workerLabelEntries(sourceWorker);
    return entries.length ? entries.map(([key, value]) => `${key}=${value}`).join('\n') : '';
  }

  function selectorRows(sourceWorker) {
    if (!sourceWorker?.pubkey) return [];
    const selectors = [{ label: 'Worker pin', selector: `worker.pubkey=${sourceWorker.pubkey}` }];
    for (const [key, value] of workerLabelEntries(sourceWorker)) {
      selectors.push({ label: `Label ${key}`, selector: `${key}=${value}` });
    }
    return selectors;
  }

  function parseLabelsInput(input) {
    const labels = {};
    for (const rawLine of String(input || '').split(/\n/)) {
      const line = rawLine.trim();
      if (!line) continue;
      const separator = line.indexOf('=');
      if (separator <= 0) throw new Error(`Label entry "${line}" must use key=value syntax`);
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

  function idempotencyKey(action, sourceWorker) {
    return `${action.command}:${sourceWorker.pubkey}:${randomId()}`;
  }

  function commandTags(action, sourceWorker, key) {
    return [
      ['d', key],
      ['worker', sourceWorker.pubkey],
      ['command', action.command]
    ];
  }

  function commandContent(action, sourceWorker, key, reason, labels = null) {
    const requestedBy = currentRequesterPubkey();
    const operatorMetadata = { source: 'web.workers.detail' };
    if (requestedBy) operatorMetadata.requested_by = requestedBy;
    const content = {
      worker_pubkey: sourceWorker.pubkey,
      reason: reason || '',
      idempotency_key: key,
      operator_metadata: operatorMetadata
    };
    if (labels) content.labels = labels;
    return content;
  }

  function actionPendingKey(sourceWorker, action) {
    return `${sourceWorker.pubkey}:${action.command}`;
  }

  function isActionPending(sourceWorker, action) {
    return Boolean(pendingCommands[actionPendingKey(sourceWorker, action)]);
  }

  function isAnyActionPending(sourceWorker) {
    const prefix = `${sourceWorker.pubkey}:`;
    return Object.keys(pendingCommands).some((key) => key.startsWith(prefix));
  }

  function setActionPending(sourceWorker, action, value) {
    const key = actionPendingKey(sourceWorker, action);
    if (value) {
      pendingCommands = { ...pendingCommands, [key]: true };
      return;
    }
    const { [key]: _removed, ...remaining } = pendingCommands;
    pendingCommands = remaining;
  }

  function isWorkerActionAllowed(sourceWorker, action) {
    return action.allowedFrom.includes(schedulingState(sourceWorker));
  }

  function setNotice(type, message) {
    notice = { type, message };
  }

  async function publishWorkerAction(sourceWorker, action, reason, labels = null) {
    if (!sourceWorker?.pubkey) throw new Error('Worker pubkey is required');
    const key = idempotencyKey(action, sourceWorker);
    const result = await publishCommand({
      kind: action.kind,
      tags: commandTags(action, sourceWorker, key),
      content: commandContent(action, sourceWorker, key, reason, labels),
      resultKinds: [WORKER_KINDS.RESULT]
    });
    return resultContent(result);
  }

  async function handleWorkerAction(action) {
    const targetWorker = worker;
    if (!targetWorker || !action || !isWorkerActionAllowed(targetWorker, action) || isAnyActionPending(targetWorker)) return;

    let reason = '';
    let labels = null;
    try {
      if (action.labels) {
        const input = globalThis.prompt?.('Worker labels as key=value lines. Empty input clears labels.', labelsText(targetWorker));
        if (input === null || input === undefined) return;
        labels = parseLabelsInput(input);
        reason = 'Operator label update from worker detail page';
      } else if (action.reasonPrompt) {
        const input = globalThis.prompt?.(action.reasonPrompt, '');
        if (input === null || input === undefined) return;
        reason = input.trim();
      }

      setActionPending(targetWorker, action, true);
      notice = null;
      const content = await publishWorkerAction(targetWorker, action, reason, labels);
      setNotice('success', content.message || `${action.label} command accepted for ${targetWorker.name || targetWorker.pubkey}`);
      await loadWorker(encodeURIComponent(targetWorker.pubkey));
    } catch (err) {
      setNotice('error', err?.message || `Failed to publish ${action.label} command`);
    } finally {
      setActionPending(targetWorker, action, false);
    }
  }

  async function copySelector(selector) {
    notice = null;
    try {
      if (!navigator?.clipboard?.writeText) throw new Error('Clipboard API is unavailable');
      await navigator.clipboard.writeText(selector);
      setNotice('success', `Copied selector ${selector}`);
    } catch (err) {
      setNotice('error', err?.message || 'Failed to copy selector');
    }
  }

  function assignmentState(sourceWorker) {
    return sourceWorker?.assignment_state || sourceWorker?.worker_assignment_state || sourceWorker?.assignments_state || {};
  }

  function drainStatus(sourceWorker) {
    return sourceWorker?.drain_status || sourceWorker?.worker_drain_status || {};
  }

  function activeAssignments(sourceWorker) {
    const state = assignmentState(sourceWorker);
    if (Array.isArray(state.active_assignments)) return state.active_assignments;
    if (Array.isArray(sourceWorker?.active_assignments)) return sourceWorker.active_assignments;
    if (Array.isArray(sourceWorker?.assignments)) return sourceWorker.assignments;
    return [];
  }

  function drainRemainingAssignments(sourceWorker) {
    const status = drainStatus(sourceWorker);
    if (Array.isArray(status.remaining_assignments)) return status.remaining_assignments;
    return activeAssignments(sourceWorker);
  }

  function pinnedBlockers(sourceWorker) {
    const status = drainStatus(sourceWorker);
    if (Array.isArray(status.pinned_blockers)) return status.pinned_blockers;
    return activeAssignments(sourceWorker).filter((assignment) => assignment?.pinned || assignment?.movable === false);
  }

  function formatBool(value, trueLabel = 'Yes', falseLabel = 'No') {
    return value ? trueLabel : falseLabel;
  }

  function assignmentDisplayName(assignment) {
    return assignment.workload_id || assignment.workload || assignment.id || 'Unknown workload';
  }

  function assignmentRows(assignments) {
    return (assignments || []).map((assignment) => {
      const pinned = Boolean(assignment.pinned);
      const movable = assignment.movable !== false && !pinned;
      return {
        type: assignment.type || assignment.workload_type || 'workload',
        workload: assignmentDisplayName(assignment),
        status: assignment.status || 'active',
        movement: pinned ? 'Pinned' : movable ? 'Movable' : 'Blocked',
        drain_blocker: pinned || !movable ? 'Blocks drain' : 'Clear',
        started_at: formatTimestamp(assignment.started_at),
        updated_at: formatTimestamp(assignment.updated_at)
      };
    });
  }

  let livenessStatus = $derived(worker ? inferWorkerStatus(worker) : 'offline');
  let scheduling = $derived(worker ? schedulingState(worker) : 'active');
  let drain = $derived(worker ? drainStatus(worker) : {});
  let assignments = $derived(worker ? activeAssignments(worker) : []);
  let remainingDrainAssignments = $derived(worker ? drainRemainingAssignments(worker) : []);
  let blockers = $derived(worker ? pinnedBlockers(worker) : []);
  let labels = $derived(worker ? workerLabelEntries(worker) : []);
  let selectors = $derived(worker ? selectorRows(worker) : []);
  let acceptsWork = $derived(worker ? acceptingNewWork(worker) : false);

  let overviewCards = $derived(worker ? [
    { label: 'Liveness', value: livenessStatus },
    { label: 'Scheduling', value: scheduling },
    { label: 'Accepting New Work', value: formatBool(acceptsWork) },
    { label: 'Active Assignments', value: assignments.length }
  ] : []);

  let schedulingRows = $derived(worker ? [
    { label: 'Scheduling state', value: scheduling },
    { label: 'Note / reason', value: schedulingNote(worker, drain) },
    { label: 'Queue depth', value: worker.current_queue_depth ?? 0 },
    { label: 'Max concurrency', value: worker.max_concurrent_jobs ?? 0 },
    { label: 'Accepting new work', value: formatBool(acceptsWork) },
    { label: 'Drain remaining assignments', value: remainingDrainAssignments.length },
    { label: 'Pinned drain blockers', value: blockers.length }
  ] : []);

  let capabilityRows = $derived(worker ? [
    { label: 'Workload kinds', value: formatList(workerWorkloadValues(worker)) },
    { label: 'Runtimes', value: formatList(workerRuntimeValues(worker)) },
    { label: 'Toolchains', value: formatList(workerToolchainValues(worker)) },
    { label: 'Artifact formats', value: formatList(workerFormatValues(worker)) },
    { label: 'Accelerators', value: formatList(workerAcceleratorValues(worker)) },
    { label: 'Features', value: formatList(workerFeatureValues(worker)) },
    { label: 'Cached artifacts', value: formatList(mlCapabilitiesFor(worker).cached_artifacts) }
  ] : []);

  let resourceRows = $derived.by(() => {
    if (!worker) return [];
    const resources = worker.resources || {};
    const accelerators = worker.accelerators || [];
    const acceleratorCount = accelerators.reduce((sum, accelerator) => sum + (accelerator.count || 1), 0);
    const acceleratorMemory = accelerators.reduce((sum, accelerator) => sum + (accelerator.memory_gb || 0) * (accelerator.count || 1), 0);
    return [
      { label: 'CPU cores', value: resources.cpu_cores || 'Not advertised' },
      { label: 'Memory', value: resources.memory_gb ? `${resources.memory_gb} GB` : 'Not advertised' },
      { label: 'Disk', value: resources.disk_gb ? `${resources.disk_gb} GB` : 'Not advertised' },
      { label: 'GPUs / accelerators', value: acceleratorCount || 'Not advertised' },
      { label: 'Accelerator memory', value: acceleratorMemory ? `${acceleratorMemory} GB` : 'Not advertised' },
      { label: 'Architecture', value: worker.architecture || 'Not advertised' }
    ];
  });

  let capacityRows = $derived(worker ? [
    { label: 'Minimum duration', value: formatDuration(worker.min_duration_secs) },
    { label: 'Maximum duration', value: formatDuration(worker.max_duration_secs) },
    { label: 'Geohash', value: worker.geohash || 'Not advertised' },
    { label: 'Runtime target', value: formatRuntimeTarget(worker.runtime_target) }
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

  let assignmentColumns = $derived([
    { key: 'type', label: 'Type' },
    { key: 'workload', label: 'Workload' },
    { key: 'status', label: 'Status' },
    { key: 'movement', label: 'Movement' },
    { key: 'drain_blocker', label: 'Drain' },
    { key: 'started_at', label: 'Started' },
    { key: 'updated_at', label: 'Updated' }
  ]);

  let assignmentData = $derived(assignmentRows(assignments));
  let drainBlockerData = $derived(assignmentRows(blockers));

  let selectorColumns = $derived([
    { key: 'label', label: 'Selector Type' },
    { key: 'selector', label: 'Selector' }
  ]);

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
    { label: 'Last advertisement', value: formatTimestamp(worker.last_advertisement_at) },
    { label: 'Drain started', value: formatTimestamp(drain?.drain_started_at) },
    { label: 'Last migration attempt', value: formatTimestamp(drain?.last_migration_attempt_at) }
  ] : []);
</script>

<div class="page">
  <a href="/workers" class="back">← Workers</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <ErrorState message={error} />
  {:else if worker}
    <div class="summary-header">
      <div class="summary-main">
        <h1>
          <StandardIcon size={28} strokeWidth={1.75} ariaHidden="true" />
          {worker.name || `Worker ${worker.pubkey?.slice(0, 12)}...`}
        </h1>
        <code class="summary-pubkey">{worker.pubkey}</code>
        {#if worker.description}
          <p class="description">{worker.description}</p>
        {/if}
        <div class="status-stack" aria-label={`Liveness ${livenessStatus}; scheduling ${scheduling}`}>
          <span class={`status-badge liveness-${livenessStatus}`}>
            <span class="status-dot" aria-hidden="true"></span>{livenessStatus}
          </span>
          <span class={`status-badge scheduling-${scheduling}`}>{scheduling}</span>
        </div>
      </div>
      <div class="quick-actions" aria-label="Worker quick actions">
        {#each QUICK_ACTIONS as action}
          {@const disabled = isAnyActionPending(worker) || !isWorkerActionAllowed(worker, action)}
          <button
            type="button"
            disabled={disabled}
            title={!isWorkerActionAllowed(worker, action) ? `${action.label} is not valid from ${scheduling}` : ''}
            onclick={() => handleWorkerAction(action)}
          >
            {isActionPending(worker, action) ? 'Publishing…' : action.label}
          </button>
        {/each}
      </div>
    </div>

    {#if notice}
      <div class={`notice ${notice.type}`} role="status">{notice.message}</div>
    {/if}

    <div class="info-grid">
      {#each overviewCards as card}
        <div class="metric-card">
          <span class="metric-label">{card.label}</span>
          <strong>{card.value}</strong>
        </div>
      {/each}
    </div>

    <section>
      <div class="section-heading">
        <h2>Scheduling</h2>
      </div>
      <dl class="detail-list">
        {#each schedulingRows as row}
          <div>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        {/each}
      </dl>
      {#if drain?.last_migration_attempt_reason}
        <p class="section-note">Last migration attempt: {drain.last_migration_attempt_reason}</p>
      {/if}
      {#if scheduling === 'draining'}
        <div class="drain-safety">
          <span class:positive={drain.safe_to_enter_maintenance} class:negative={!drain.safe_to_enter_maintenance}>
            Safe for maintenance: {formatBool(drain.safe_to_enter_maintenance)}
          </span>
          <span class:positive={drain.safe_to_disable} class:negative={!drain.safe_to_disable}>
            Safe to disable: {formatBool(drain.safe_to_disable)}
          </span>
        </div>
      {/if}
    </section>

    <section>
      <h2>Capabilities</h2>
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
      <dl class="detail-list">
        {#each resourceRows as row}
          <div>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        {/each}
      </dl>
      {#if acceleratorData.length > 0}
        <div class="subsection">
          <h3>Accelerator inventory</h3>
          <Table columns={acceleratorColumns} data={acceleratorData} />
        </div>
      {:else}
        <EmptyState message="No accelerators advertised" />
      {/if}
    </section>

    <section>
      <div class="section-heading">
        <h2>Labels & Placement</h2>
        <button type="button" class="secondary-action" disabled={isAnyActionPending(worker)} onclick={() => handleWorkerAction(LABEL_ACTION)}>Edit labels</button>
      </div>
      {#if labels.length > 0}
        <div class="label-list">
          {#each labels as [key, value]}
            <span class="label-chip"><strong>{key}</strong>={value}</span>
          {/each}
        </div>
      {:else}
        <EmptyState message="No worker labels advertised" />
      {/if}
      <div class="subsection">
        <h3>Example selectors</h3>
        <Table columns={selectorColumns} data={selectors} />
        <div class="selector-actions">
          {#each selectors as selector}
            <button type="button" class="secondary-action" onclick={() => copySelector(selector.selector)}>Copy {selector.label}</button>
          {/each}
        </div>
      </div>
    </section>

    <section>
      <h2>Active Assignments</h2>
      {#if assignmentData.length > 0}
        <Table columns={assignmentColumns} data={assignmentData} />
      {:else}
        <EmptyState message="No active assignments on this worker" />
      {/if}
      {#if drainBlockerData.length > 0}
        <div class="subsection warning-panel">
          <h3>Drain blockers</h3>
          <p>Pinned or non-movable assignments must clear before the worker can finish draining.</p>
          <Table columns={assignmentColumns} data={drainBlockerData} />
        </div>
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
      <h2>Execution Details</h2>
      <dl class="detail-list">
        {#each capacityRows as row}
          <div>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        {/each}
      </dl>
      {#if hasRuntimeTarget(worker.runtime_target)}
        <p class="runtime-target">{formatRuntimeTarget(worker.runtime_target)}</p>
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
  .page { max-width: 1100px; }

  .back {
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.875rem;
    display: inline-block;
    margin-bottom: 1rem;
  }
  .back:hover { color: var(--text-primary); }

  .summary-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1.5rem;
    margin-bottom: 1.5rem;
  }

  .summary-main {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.65rem;
  }

  .summary-main h1 {
    margin: 0;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .summary-main h1 :global(svg) {
    display: block;
    flex-shrink: 0;
  }
  .summary-pubkey {
    display: inline-block;
    max-width: 100%;
    font-family: 'Monaco', 'Courier New', monospace;
    font-size: 0.875rem;
    word-break: break-all;
    background: var(--hover-bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 0.45rem 0.6rem;
  }
  .description {
    margin: 0;
    color: var(--text-muted);
  }

  .quick-actions, .selector-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.5rem;
  }

  button {
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 0.45rem;
    background: var(--surface-bg, #141426);
    color: var(--text-color, #fff);
    cursor: pointer;
    padding: 0.5rem 0.7rem;
  }

  button:hover:not(:disabled) { background: var(--hover-bg, #252540); }
  button:disabled { color: var(--text-muted, #888); cursor: not-allowed; opacity: 0.7; }
  .secondary-action { color: var(--primary, #8b5cf6); }

  .notice {
    border: 1px solid var(--border-color, #2a2a4a);
    border-radius: 0.5rem;
    padding: 0.75rem 1rem;
    margin-bottom: 1rem;
  }
  .notice.success { border-color: rgba(34, 197, 94, 0.5); color: #86efac; background: rgba(34, 197, 94, 0.08); }
  .notice.error { border-color: rgba(239, 68, 68, 0.5); color: #fca5a5; background: rgba(239, 68, 68, 0.08); }

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
    margin: 0 0 1rem;
  }

  .section-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }

  .section-heading h2 { margin-bottom: 0; }

  .subsection {
    margin-top: 1.25rem;
  }

  .subsection h3 {
    margin: 0 0 0.75rem;
    color: var(--text-muted);
    font-size: 0.9rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .section-note {
    color: var(--text-muted);
    margin: 1rem 0 0;
  }

  .runtime-target {
    background: var(--hover-bg);
    padding: 1rem;
    border-radius: 4px;
    overflow-x: auto;
    margin: 1rem 0 0;
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
    padding: 0.25rem 0.6rem;
    background: var(--surface-bg, #141426);
    color: var(--text-color, #fff);
    font-size: 0.8rem;
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

  .drain-safety {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    margin-top: 1rem;
  }

  .positive { color: #86efac; }
  .negative { color: #fca5a5; }

  .warning-panel {
    border: 1px solid rgba(245, 158, 11, 0.45);
    border-radius: 8px;
    padding: 1rem;
    background: rgba(245, 158, 11, 0.06);
  }

  .warning-panel p {
    color: var(--text-muted);
    margin: 0 0 0.75rem;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  @media (max-width: 800px) {
    .summary-header { flex-direction: column; }
    .quick-actions, .selector-actions { justify-content: flex-start; }
    .section-heading { align-items: flex-start; flex-direction: column; }
  }
</style>
