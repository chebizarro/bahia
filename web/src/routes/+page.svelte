<script>
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import CreateServiceDialog from './services/CreateServiceDialog.svelte';
  import OperationalActivity from './OperationalActivity.svelte';
  import {
    DeploymentIcon,
    EnvironmentIcon,
    InfoIcon,
    PaymentIcon,
    ServiceIcon,
    StandardIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';
  import { requestPaymentHistoryRecords } from '$lib/stores/payments.svelte.js';
  import { kindLabel } from '$lib/nostr/kind-labels.js';
  import { services, environments, states, workers, driftedStates, events, deploymentIntents, controlplaneConnection, discoveryState, operations } from '$lib/stores';
  import { formatDashboardSats, normalizePaymentHistory, summarizeRecentSpend } from './dashboard-cost-summary.js';
  import { summarizeWorkerActivity } from './workers/list-utils.js';
  import { summarizeDriftCause, shortHash } from './dashboard-drift-summary.js';

  // Pending deployments state
  let pendingDeployments = $state([]);
  let pendingLoading = $state(false);
  let pendingError = $state(null);
  let pendingCacheLoadedAt = null;
  let pendingPairFailures = 0;

  // Dashboard cost summary state
  let costSummary = $state({ totalSats: 0, paymentCount: 0, workerCount: 0, latestPaymentAt: '' });
  let costSummaryLoading = $state(false);
  let costSummaryError = $state(null);
  let costSummaryPartialFailures = $state(0);
  let costSummaryLoadSequence = 0;
  let lastCostSummaryWorkerKey = null;
  let timeColumnLabel = $state('Time (local)');
  let selectedActivityEvent = $state(null);
  let activityEventDialogOpen = $state(false);
  let selectedDriftState = $state(null);
  let driftDialogOpen = $state(false);
  // Create-service dialog opens in place on the dashboard (no navigation).
  let createServiceOpen = $state(false);

  // Cache configuration
  const PENDING_CACHE_KEY = 'bahia_dashboard_pending_deployments';
  const PENDING_CACHE_TTL_MS = 30000; // 30 seconds
  const COST_HISTORY_LIMIT_PER_WORKER = 25;



  // Helper: determine badge variant for event type
  function eventBadgeVariant(type) {
    if (type.startsWith('deployment.')) return 'info';
    if (type.startsWith('drift.')) return 'warning';
    if (type.startsWith('service.')) return 'success';
    if (type.startsWith('worker.')) return 'default';
    return 'default';
  }

  // Helper: escape HTML to prevent XSS (pure string replacement)
  function escapeHtml(text) {
    return String(text ?? '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function dashboardStateActions(row) {
    if (String(row?.drift_status || '').toLowerCase() !== 'drifted') return '-';

    const links = [];
    if (row.environment_id) {
      const envHref = `/environments/${encodeURIComponent(row.environment_id)}`;
      links.push(`<a class="row-action-link" href="${envHref}">Review environment</a>`);
    }

    if (row.service_id) {
      const serviceHref = `/services/${encodeURIComponent(row.service_id)}`;
      links.push(`<a class="row-action-link secondary" href="${serviceHref}">Open service</a>`);
    }

    return links.length > 0 ? `<div class="row-actions">${links.join('')}</div>` : '-';
  }

  function firstPresentString(...values) {
    const value = values.find((candidate) => candidate !== null && candidate !== undefined && String(candidate).trim() !== '');
    return value === undefined ? '' : String(value);
  }

  function getLocalTimeZoneLabel(date = new Date()) {
    try {
      return Intl.DateTimeFormat(undefined, { timeZoneName: 'short' })
        .formatToParts(date)
        .find((part) => part.type === 'timeZoneName')?.value || 'local';
    } catch {
      return 'local';
    }
  }

  function formatLocalTime(timestamp) {
    if (!timestamp) return '-';
    const date = new Date(timestamp);
    if (Number.isNaN(date.getTime())) return '-';
    return date.toLocaleTimeString([], {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit'
    });
  }

  function formatLocalDateTime(timestamp) {
    if (!timestamp) return '-';
    const date = new Date(timestamp);
    if (Number.isNaN(date.getTime())) return '-';
    return date.toLocaleString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      timeZoneName: 'short'
    });
  }

  function formatUtcDateTime(timestamp) {
    if (!timestamp) return '-';
    const date = new Date(timestamp);
    if (Number.isNaN(date.getTime())) return '-';
    return date.toLocaleString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      timeZone: 'UTC',
      timeZoneName: 'short'
    });
  }

  function truncateLabel(value, max = 24) {
    const text = String(value || '');
    if (!text) return '';
    return text.length <= max ? text : `${text.slice(0, max - 1)}…`;
  }

  function fallbackIdLabel(id) {
    const text = String(id || '');
    return text ? `${text.slice(0, 8)}...` : '-';
  }

  function dashboardWorkerPubkey(worker) {
    return firstPresentString(worker?.pubkey, worker?.worker_pubkey, worker?.id);
  }

  function pluralize(count, singular, plural = `${singular}s`) {
    return count === 1 ? singular : plural;
  }

  function isDashboardSyncing(status, bootstrapComplete, discoveryLoading) {
    return discoveryLoading || (!bootstrapComplete && ['discovering', 'connecting', 'syncing', 'bootstrapping', 'idle'].includes(status));
  }

  function formatMetricValue(value, syncing) {
    return syncing && Number(value) === 0 ? '...' : value;
  }

  function emptyCostSummary(workerCount = 0) {
    return { totalSats: 0, paymentCount: 0, workerCount, latestPaymentAt: '' };
  }

  function eventData(row) {
    return row?.data && typeof row.data === 'object' && !Array.isArray(row.data) ? row.data : {};
  }

  function lookupService(serviceId) {
    return services.find((candidate) => candidate.id === serviceId) || null;
  }

  function lookupEnvironment(environmentId) {
    return environments.find((candidate) => candidate.id === environmentId) || null;
  }

  function serviceDisplayNameById(serviceId) {
    const service = lookupService(serviceId);
    return firstPresentString(service?.name, service?.artifact_repo, serviceId);
  }

  function environmentDisplayNameById(environmentId) {
    const environment = lookupEnvironment(environmentId);
    return firstPresentString(environment?.name, environment?.slug, environmentId);
  }

  function entityTooltip(label, name, id) {
    const displayName = firstPresentString(name, id);
    return id ? `${label}: ${displayName} (${id})` : `${label}: ${displayName}`;
  }

  function renderDashboardEntityLink(basePath, id, label, title) {
    if (!id) return '-';

    const href = `${basePath}/${encodeURIComponent(id)}`;
    return `<a class="dashboard-entity-link" href="${href}" title="${escapeHtml(title)}">${escapeHtml(label)}</a>`;
  }

  function dashboardStateServiceCell(row) {
    const serviceId = firstPresentString(row?.service_id);
    const serviceName = serviceDisplayNameById(serviceId);
    return renderDashboardEntityLink('/services', serviceId, truncateLabel(serviceName), entityTooltip('Service', serviceName, serviceId));
  }

  function dashboardStateEnvironmentCell(row) {
    const environmentId = firstPresentString(row?.environment_id);
    const environmentName = environmentDisplayNameById(environmentId);
    return renderDashboardEntityLink('/environments', environmentId, truncateLabel(environmentName), entityTooltip('Environment', environmentName, environmentId));
  }

  function resolveActivityReferences(row) {
    const type = String(row?.type || '');
    const data = eventData(row);
    const entityId = firstPresentString(row?.entity_id);
    const serviceId = firstPresentString(
      data.service_id,
      data.serviceId,
      type.startsWith('service.') ? entityId : ''
    );
    const environmentId = firstPresentString(
      data.environment_id,
      data.environmentId,
      type.startsWith('environment.') ? entityId : ''
    );

    return {
      entityId,
      serviceId,
      serviceName: firstPresentString(
        data.service_name,
        data.serviceName,
        serviceId ? serviceDisplayNameById(serviceId) : ''
      ),
      environmentId,
      environmentName: firstPresentString(
        data.environment_name,
        data.environmentName,
        environmentId ? environmentDisplayNameById(environmentId) : ''
      ),
      deploymentId: firstPresentString(
        data.deployment_id,
        data.deploymentId,
        data.intent_id,
        data.intentId,
        data.deployment_intent_id,
        data.deploymentIntentId,
        type.startsWith('deployment.') && !serviceId && !environmentId ? entityId : ''
      )
    };
  }

  function describeActivityEvent(row) {
    const refs = resolveActivityReferences(row);
    const data = eventData(row);
    const lines = [
      row?.type ? `Type: ${row.type}` : '',
      row?.time ? `Local: ${formatLocalDateTime(row.time)}` : '',
      row?.time ? `UTC: ${formatUtcDateTime(row.time)}` : '',
      refs.serviceId ? entityTooltip('Service', refs.serviceName || serviceDisplayNameById(refs.serviceId), refs.serviceId) : '',
      refs.environmentId ? entityTooltip('Environment', refs.environmentName || environmentDisplayNameById(refs.environmentId), refs.environmentId) : '',
      refs.deploymentId ? `Deployment: ${refs.deploymentId}` : '',
      data.status ? `Status: ${data.status}` : '',
      data.requested_by ? `Requested by: ${data.requested_by}` : ''
    ].filter(Boolean);

    const payloadSummary = JSON.stringify(data);
    if (payloadSummary && payloadSummary !== '{}') {
      lines.push(`Data: ${payloadSummary}`);
    }

    return lines.join('\n');
  }

  // Map raw `nostr.kind.<number>` activity types to a friendly kind name so the
  // Event column reads as a human label instead of an internal kind string.
  function activityEventLabel(row) {
    const type = String(row?.type || 'unknown');
    const match = type.match(/^nostr\.kind\.(\d+)$/);
    if (match) return kindLabel(Number(match[1]));
    if ((type === 'unknown' || type === '') && row?.kind) return kindLabel(row.kind);
    return type;
  }

  function renderActivityEventTrigger(row) {
    const type = row?.type || 'unknown';
    const label = activityEventLabel(row);
    const variant = eventBadgeVariant(type);
    return `<button type="button" class="activity-event-trigger" data-dashboard-action="event" title="${escapeHtml(describeActivityEvent(row))}" aria-label="${escapeHtml(`View details for ${label}`)}"><span class="badge ${variant}">${escapeHtml(label)}</span></button>`;
  }

  function addActivityEntityLink(links, seenHrefs, label, basePath, id, displayName = '') {
    if (!id) return;

    const entityId = String(id);
    const href = `${basePath}/${encodeURIComponent(entityId)}`;
    if (seenHrefs.has(href)) return;

    seenHrefs.add(href);
    const title = entityTooltip(label, displayName || entityId, entityId);
    const valueMarkup = displayName
      ? `<span class="entity-name">${escapeHtml(truncateLabel(displayName, 18))}</span>`
      : `<code>${escapeHtml(fallbackIdLabel(entityId))}</code>`;

    links.push(
      `<a class="activity-entity-link" href="${href}" title="${escapeHtml(title)}" aria-label="${escapeHtml(title)}">` +
        `<span class="entity-kind">${escapeHtml(label)}</span> ${valueMarkup}` +
      `</a>`
    );
  }

  function dashboardActivityEntityLinks(row) {
    const refs = resolveActivityReferences(row);
    const links = [];
    const seenHrefs = new Set();

    addActivityEntityLink(links, seenHrefs, 'Deployment', '/deployments', refs.deploymentId);
    addActivityEntityLink(links, seenHrefs, 'Service', '/services', refs.serviceId, refs.serviceName || serviceDisplayNameById(refs.serviceId));
    addActivityEntityLink(links, seenHrefs, 'Environment', '/environments', refs.environmentId, refs.environmentName || environmentDisplayNameById(refs.environmentId));

    if (links.length > 0) {
      return `<div class="activity-entity-links">${links.join('')}</div>`;
    }

    return refs.entityId ? `<code>${escapeHtml(fallbackIdLabel(refs.entityId))}</code>` : '-';
  }

  function openActivityEventDialog(row) {
    if (!row) return;
    selectedActivityEvent = row;
    activityEventDialogOpen = true;
  }

  function openDriftDialog(row) {
    if (!row) return;
    selectedDriftState = row;
    driftDialogOpen = true;
  }

  function handleEnvironmentStateRowClick(row, event) {
    if (event?.target?.closest?.('[data-dashboard-action="drift"]')) {
      openDriftDialog(row);
    }
  }

  function serializeDriftState(row) {
    if (!row) return '';
    try {
      return JSON.stringify(row, null, 2);
    } catch {
      return String(row);
    }
  }

  function handleRecentActivityRowClick(row, event) {
    if (event.target?.closest?.('a')) return;
    const trigger = event.target?.closest?.('[data-dashboard-action="event"]');
    if (!trigger) return;
    openActivityEventDialog(row);
  }

  function serializeActivityEvent(row) {
    if (!row) return '';
    const refs = resolveActivityReferences(row);

    return JSON.stringify({
      id: row.id,
      type: row.type,
      local_time: formatLocalDateTime(row.time),
      utc_time: formatUtcDateTime(row.time),
      entity_id: row.entity_id,
      service_id: refs.serviceId || null,
      service_name: refs.serviceName || null,
      environment_id: refs.environmentId || null,
      environment_name: refs.environmentName || null,
      deployment_id: refs.deploymentId || null,
      data: eventData(row),
      nostr_event: row.nostr_event || null
    }, null, 2);
  }

  // Helper: bounded concurrency for async operations
  async function withBoundedConcurrency(tasks, limit) {
    const results = [];
    const executing = [];
    
    for (const task of tasks) {
      const promise = task().then(result => {
        executing.splice(executing.indexOf(promise), 1);
        return result;
      });
      
      results.push(promise);
      executing.push(promise);
      
      if (executing.length >= limit) {
        await Promise.race(executing);
      }
    }
    
    return Promise.all(results);
  }

  // Helper: get cached pending count if fresh
  function getCachedPendingCount() {
    if (typeof sessionStorage === 'undefined') return null;
    
    try {
      const cached = sessionStorage.getItem(PENDING_CACHE_KEY);
      if (!cached) return null;
      
      const { count, timestamp } = JSON.parse(cached);
      const age = Date.now() - timestamp;
      
      if (age < PENDING_CACHE_TTL_MS) {
        pendingCacheLoadedAt = timestamp;
        return count;
      }
    } catch (err) {
      // Invalid cache, ignore
    }
    
    return null;
  }

  // Helper: cache pending count
  function cachePendingCount(count) {
    if (typeof sessionStorage === 'undefined') return;
    
    try {
      const timestamp = Date.now();
      sessionStorage.setItem(PENDING_CACHE_KEY, JSON.stringify({ count, timestamp }));
      pendingCacheLoadedAt = timestamp;
    } catch (err) {
      // Cache storage failed, ignore
    }
  }

  // Load dashboard cost summary
  async function loadDashboardCostSummary(workerPubkeys) {
    const sequence = ++costSummaryLoadSequence;
    costSummaryError = null;
    costSummaryPartialFailures = 0;

    if (workerPubkeys.length === 0) {
      costSummary = emptyCostSummary(workerPubkeys.length);
      costSummaryLoading = false;
      return;
    }

    costSummaryLoading = true;

    try {
      const paymentGroups = await withBoundedConcurrency(
        workerPubkeys.map((worker) => async () => {
          try {
            return {
              payments: normalizePaymentHistory(
                await requestPaymentHistoryRecords({ worker, limit: COST_HISTORY_LIMIT_PER_WORKER })
              ),
              error: null
            };
          } catch (err) {
            return { payments: [], error: err };
          }
        }),
        4
      );

      if (sequence !== costSummaryLoadSequence) return;

      const failedWorkers = paymentGroups.filter((group) => group.error).length;
      costSummaryPartialFailures = failedWorkers;
      costSummary = summarizeRecentSpend(paymentGroups.flatMap((group) => group.payments), { workerCount: workerPubkeys.length });
      if (failedWorkers === workerPubkeys.length) {
        costSummaryError = 'Failed to load payment history';
      }
    } catch (err) {
      if (sequence !== costSummaryLoadSequence) return;
      console.error('Failed to load dashboard cost summary:', err);
      costSummaryError = err.message || 'Failed to load payment history';
      costSummary = emptyCostSummary(workerPubkeys.length);
    } finally {
      if (sequence === costSummaryLoadSequence) {
        costSummaryLoading = false;
      }
    }
  }

  async function loadPendingDeployments() {
    // Try to display cached count first
    const cachedCount = getCachedPendingCount();
    if (cachedCount !== null) {
      pendingDeployments = new Array(cachedCount); // Placeholder
    }

    pendingLoading = true;
    pendingError = null;
    pendingPairFailures = 0;

    try {
      // Use relay-backed deployment intent read models; do not fall back to REST list calls.
      pendingDeployments = (deploymentIntents || []).filter(intent => {
        const status = String(intent.approval_status || '').toLowerCase();
        return status === 'pending';
      });
      pendingPairFailures = 0;

      // Cache the fresh result
      cachePendingCount(pendingDeployments.length);

    } catch (err) {
      console.error('Failed to load pending deployments:', err);
      pendingError = err.message;
      pendingDeployments = [];
    } finally {
      pendingLoading = false;
    }
  }

  $effect(() => {
    timeColumnLabel = `Time (${getLocalTimeZoneLabel()})`;
  });

  $effect(() => {
    deploymentIntents.map((intent) => `${intent.id}:${intent.approval_status}:${intent.created_at}`).join('|');
    queueMicrotask(() => loadPendingDeployments());
  });

  $effect(() => {
    const workerPubkeys = Array.from(new Set(workers.map(dashboardWorkerPubkey).filter(Boolean))).sort();
    const workerKey = workerPubkeys.join('|');

    queueMicrotask(() => {
      if (workerKey === lastCostSummaryWorkerKey) return;
      lastCostSummaryWorkerKey = workerKey;
      void loadDashboardCostSummary(workerPubkeys);
    });
  });

  let stateColumns = $derived([
    { key: 'service_id', label: 'Service', render: dashboardStateServiceCell },
    { key: 'environment_id', label: 'Environment', render: dashboardStateEnvironmentCell },
    { key: 'drift_status', label: 'Drift', render: (r) => {
      const driftStatus = String(r.drift_status || 'unknown');
      const variant = driftStatus === 'in_sync' ? 'success' : driftStatus === 'drifted' ? 'error' : 'default';
      return `<span class="drift-cell" data-dashboard-action="drift" tabindex="0" title="View drift details"><span class="badge-${variant}">${escapeHtml(driftStatus)}</span></span>`;
    }},
    { key: 'actions', label: 'Actions', render: dashboardStateActions }
  ]);
  let eventColumns = $derived([
    {
      key: 'time',
      label: timeColumnLabel,
      render: (r) => `<span class="activity-time" title="${escapeHtml(formatUtcDateTime(r.time))}">${escapeHtml(formatLocalTime(r.time))}</span>`
    },
    { key: 'type', label: 'Event', render: renderActivityEventTrigger },
    { key: 'entity_id', label: 'Entity', render: dashboardActivityEntityLinks }
  ]);
  let pendingCount = $derived(pendingDeployments.length);
  let workerActivity = $derived(summarizeWorkerActivity(workers));
  let dashboardSyncing = $derived(
    isDashboardSyncing(controlplaneConnection.status, controlplaneConnection.bootstrapComplete, discoveryState.loading)
  );
  let dashboardHasSnapshotData = $derived(
    services.length > 0 ||
    environments.length > 0 ||
    states.length > 0 ||
    workers.length > 0 ||
    events.length > 0 ||
    deploymentIntents.length > 0
  );
  let dashboardSyncMessage = $derived.by(() => {
    if (!dashboardSyncing) return '';
    if (discoveryState.loading && controlplaneConnection.status === 'idle') {
      return dashboardHasSnapshotData
        ? 'Warming the dashboard from browser cache while bootstrap discovery completes…'
        : 'Loading cached controlplane snapshot…';
    }
    if (controlplaneConnection.status === 'discovering') return 'Discovering relays for the dashboard snapshot…';
    if (controlplaneConnection.status === 'connecting') return 'Connecting to relays…';
    return dashboardHasSnapshotData
      ? 'Streaming relay snapshot… partial results are already live.'
      : 'Streaming relay snapshot… results will appear as they arrive.';
  });
  let servicesCardValue = $derived(formatMetricValue(services.length, dashboardSyncing));
  let environmentsCardValue = $derived(formatMetricValue(environments.length, dashboardSyncing));
  let workerCardValue = $derived(workerActivity.live);
  let workerCardSubtitle = $derived(
    workerActivity.catalog === 0
      ? (dashboardSyncing ? 'Waiting for worker ads…' : 'No workers yet')
      : `${workerActivity.recent} recent / ${workerActivity.catalog} catalog`
  );
  let workerCardDisplayValue = $derived(formatMetricValue(workerCardValue, dashboardSyncing));
  let driftedCardValue = $derived(formatMetricValue(driftedStates().length, dashboardSyncing));
  let pendingSubtitle = $derived(pendingError
    ? 'Unable to load'
    : pendingCount > 0
      ? 'Needs review'
      : dashboardSyncing
        ? 'Streaming approvals'
        : 'All clear');
  let costSummaryValue = $derived(costSummaryLoading ? '...' : formatDashboardSats(costSummary.totalSats));
  let costSummarySubtitle = $derived(costSummaryError
    ? 'Unable to load payment history'
    : costSummaryLoading
      ? 'Loading payment history'
      : costSummary.paymentCount > 0
        ? `${costSummary.paymentCount} recent ${pluralize(costSummary.paymentCount, 'payment')}${costSummaryPartialFailures > 0 ? `; ${costSummaryPartialFailures} ${pluralize(costSummaryPartialFailures, 'worker')} unavailable` : ''}`
        : workers.length === 0
          ? 'No workers yet'
          : costSummaryPartialFailures > 0
            ? `${costSummaryPartialFailures} ${pluralize(costSummaryPartialFailures, 'worker')} unavailable`
            : 'No recent spend');
  let costSummaryStatus = $derived(costSummaryError ? 'error' : costSummary.paymentCount > 0 ? 'warning' : 'success');
  let dashboardOperations = $derived(operations.filter((operation) =>
    ['deployment', 'service', 'environment', 'action', 'observation', 'remediation', 'hive-ci'].includes(operation.domain)
  ));
</script>

<div class="dashboard" data-testid="dashboard-root">
  <h1>Dashboard</h1>

  {#if dashboardSyncing}
    <div class="dashboard-sync-banner" role="status" aria-live="polite">
      <span class="dashboard-sync-spinner" aria-hidden="true"></span>
      <span>{dashboardSyncMessage}</span>
    </div>
  {/if}
  
  <div class="quick-actions">
    <button type="button" class="action-link" onclick={() => (createServiceOpen = true)}>+ Create Service</button>
    <a href="/deployments" class="action-link">Deployment History</a>
  </div>

  <div class="stats">
    <a href="/services" class="card-link">
      <Card title="Services" titleIcon={ServiceIcon} value={servicesCardValue} subtitle={dashboardSyncing && services.length === 0 ? 'Waiting for relay snapshot' : 'Total registered'}>
        <span class="card-action">View services</span>
      </Card>
    </a>
    <a href="/environments" class="card-link">
      <Card title="Environments" titleIcon={EnvironmentIcon} value={environmentsCardValue} subtitle={dashboardSyncing && environments.length === 0 ? 'Waiting for relay snapshot' : 'Configured'}>
        <span class="card-action">View environments</span>
      </Card>
    </a>
    <a href="/workers" class="card-link">
      <Card title="Workers" titleIcon={StandardIcon} value={workerCardDisplayValue} subtitle={workerCardSubtitle}>
        <span class="card-action">View workers</span>
      </Card>
    </a>
    <a href="/environment-states" class="card-link">
      <Card 
        title="Drifted" 
        titleIcon={WarningIcon}
        value={driftedCardValue} 
        subtitle={driftedStates().length > 0 ? 'Review drifted rows' : dashboardSyncing ? 'Waiting for state snapshot' : 'All clear'}
        status={driftedStates().length > 0 ? 'error' : 'success'}
      >
        <span class="card-action">Review states</span>
      </Card>
    </a>
    <a href="/deployments/pending" class="card-link">
      <Card
        title="Pending Approvals"
        titleIcon={DeploymentIcon}
        value={pendingLoading ? '...' : pendingCount}
        subtitle={pendingSubtitle}
        status={pendingError ? 'error' : pendingCount > 0 ? 'warning' : 'success'}
      >
        <span class="card-action">Review approvals</span>
      </Card>
    </a>
    <a href="/payments" class="card-link">
      <Card
        title="Recent Spend"
        titleIcon={PaymentIcon}
        value={costSummaryValue}
        subtitle={costSummarySubtitle}
        status={costSummaryStatus}
      >
        <span class="card-action">Open payments</span>
      </Card>
    </a>
  </div>

  <OperationalActivity items={dashboardOperations} title="Live operations" limit={10} />

  <div class="sections">
    <section id="environment-states">
      <h2 class="section-title-with-icon">
        <WarningIcon size={20} strokeWidth={1.75} ariaHidden="true" />
        Environment States
      </h2>
      <Table columns={stateColumns} data={states.slice(0, 10)} onRowClick={handleEnvironmentStateRowClick} rowClickable={false} />
      {#if dashboardSyncing}
        <p class="section-streaming-hint">Snapshot still streaming from relays. Additional states will appear as they arrive.</p>
      {/if}
      {#if !dashboardSyncing && states.length === 0}
        <p class="hint">No environment states have arrived from the relay-backed control plane yet.</p>
      {/if}
    </section>

    <section>
      <h2>Recent Activity</h2>
      <Table columns={eventColumns} data={events.slice(0, 10)} onRowClick={handleRecentActivityRowClick} rowClickable={false} />
      {#if dashboardSyncing}
        <p class="section-streaming-hint">Recent activity is loading live from the relay snapshot.</p>
      {/if}
      {#if !dashboardSyncing && events.length === 0}
        <p class="hint">Events will appear here in real-time from the relay-backed control plane</p>
      {/if}
    </section>
  </div>
</div>

<Modal
  bind:open={activityEventDialogOpen}
  title={selectedActivityEvent ? `${selectedActivityEvent.type} · Event detail` : 'Event detail'}
  titleIcon={InfoIcon}
  size="lg"
  onClose={() => {
    activityEventDialogOpen = false;
    selectedActivityEvent = null;
  }}
>
  {#if selectedActivityEvent}
    <div class="dashboard-detail-dialog">
      <dl>
        <div>
          <dt>Local Time</dt>
          <dd>{formatLocalDateTime(selectedActivityEvent.time)}</dd>
        </div>
        <div>
          <dt>UTC Time</dt>
          <dd>{formatUtcDateTime(selectedActivityEvent.time)}</dd>
        </div>
        <div>
          <dt>Entity ID</dt>
          <dd><code>{firstPresentString(selectedActivityEvent.entity_id, '—')}</code></dd>
        </div>
      </dl>
      <pre class="dashboard-event-json">{serializeActivityEvent(selectedActivityEvent)}</pre>
    </div>
  {/if}
</Modal>

<Modal
  bind:open={driftDialogOpen}
  title="Drift Details"
  titleIcon={WarningIcon}
  size="lg"
  onClose={() => {
    driftDialogOpen = false;
    selectedDriftState = null;
  }}
>
  {#if selectedDriftState}
    {@const drift = summarizeDriftCause(selectedDriftState)}
    {@const statusVariant = drift.status === 'in_sync' ? 'success' : drift.status === 'drifted' ? 'error' : 'default'}
    <div class="dashboard-detail-dialog">
      <div class="drift-cause drift-cause-{drift.severity}">
        <span class="badge-{statusVariant} drift-cause-badge">{firstPresentString(selectedDriftState.drift_status, 'unknown')}</span>
        <div class="drift-cause-text">
          <strong>{drift.headline}</strong>
          <p>{drift.detail}</p>
        </div>
      </div>

      <dl>
        <div>
          <dt>Service</dt>
          <dd>{serviceDisplayNameById(firstPresentString(selectedDriftState.service_id))}</dd>
        </div>
        <div>
          <dt>Environment</dt>
          <dd>{environmentDisplayNameById(firstPresentString(selectedDriftState.environment_id))}</dd>
        </div>
        {#if drift.desiredHash}
          <div>
            <dt>Desired hash</dt>
            <dd><code title={drift.desiredHash}>{shortHash(drift.desiredHash)}</code></dd>
          </div>
        {/if}
        {#if drift.observedHash}
          <div>
            <dt>Observed hash</dt>
            <dd>
              <code title={drift.observedHash}>{shortHash(drift.observedHash)}</code>
              {#if drift.hashesMatch === false}
                <span class="drift-hash-flag mismatch">≠ desired</span>
              {:else if drift.hashesMatch === true}
                <span class="drift-hash-flag match">matches desired</span>
              {/if}
            </dd>
          </div>
        {:else if drift.status === 'drifted'}
          <div>
            <dt>Observed hash</dt>
            <dd class="drift-muted">No observation reported</dd>
          </div>
        {/if}
        {#if selectedDriftState.renderer}
          <div>
            <dt>Renderer</dt>
            <dd>{selectedDriftState.renderer}</dd>
          </div>
        {/if}
        {#if selectedDriftState.target}
          <div>
            <dt>Target</dt>
            <dd>{selectedDriftState.target}</dd>
          </div>
        {/if}
        {#if selectedDriftState.desired_artifact_id}
          <div>
            <dt>Desired artifact</dt>
            <dd><code>{selectedDriftState.desired_artifact_id}</code></dd>
          </div>
        {/if}
        {#if selectedDriftState.desired_intent_id}
          <div>
            <dt>Desired intent</dt>
            <dd><code>{selectedDriftState.desired_intent_id}</code></dd>
          </div>
        {/if}
        {#if selectedDriftState.last_successful_run_id}
          <div>
            <dt>Last successful run</dt>
            <dd><code>{selectedDriftState.last_successful_run_id}</code></dd>
          </div>
        {/if}
        {#if selectedDriftState.current_observation_id}
          <div>
            <dt>Current observation</dt>
            <dd><code>{selectedDriftState.current_observation_id}</code></dd>
          </div>
        {/if}
        {#if selectedDriftState.last_reconciled_at}
          <div>
            <dt>Last reconciled</dt>
            <dd>{formatLocalDateTime(selectedDriftState.last_reconciled_at)}</dd>
          </div>
        {/if}
        {#if selectedDriftState.updated_at}
          <div>
            <dt>Updated</dt>
            <dd>{formatLocalDateTime(selectedDriftState.updated_at)}</dd>
          </div>
        {/if}
      </dl>

      <details class="drift-raw">
        <summary>Raw state JSON</summary>
        <pre class="dashboard-event-json">{serializeDriftState(selectedDriftState)}</pre>
      </details>
    </div>
  {/if}
</Modal>

<CreateServiceDialog bind:open={createServiceOpen} />

<style>
  .dashboard h1 {
    margin-bottom: 1rem;
  }
  .dashboard-sync-banner {
    display: inline-flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 1rem;
    padding: 0.75rem 1rem;
    border: 1px solid color-mix(in srgb, var(--primary) 28%, var(--border-color));
    border-radius: 999px;
    background: color-mix(in srgb, var(--primary) 10%, var(--card-bg));
    color: var(--text-primary);
    font-size: 0.9rem;
  }
  .dashboard-sync-spinner {
    width: 0.9rem;
    height: 0.9rem;
    border: 2px solid color-mix(in srgb, var(--primary) 22%, transparent);
    border-top-color: var(--primary);
    border-radius: 999px;
    animation: dashboard-spin 0.75s linear infinite;
    flex-shrink: 0;
  }
  .quick-actions {
    display: flex;
    gap: 0.75rem;
    margin-bottom: 1.5rem;
  }
  .action-link {
    padding: 0.5rem 1rem;
    background: var(--primary);
    color: white;
    border: none;
    border-radius: 6px;
    text-decoration: none;
    font-family: inherit;
    font-size: 0.875rem;
    font-weight: 500;
    cursor: pointer;
    transition: opacity 0.2s;
  }
  .action-link:hover {
    opacity: 0.9;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }
  .card-link {
    display: block;
    text-decoration: none;
    color: inherit;
  }
  .card-link :global(.card) {
    height: 100%;
    transition: border-color 0.2s, transform 0.2s;
  }
  .card-link:hover :global(.card),
  .card-link:focus-visible :global(.card) {
    border-color: var(--primary);
    transform: translateY(-1px);
  }
  .card-action {
    display: inline-block;
    margin-top: 0.75rem;
    color: var(--primary);
    font-size: 0.75rem;
    font-weight: 600;
  }
  .sections {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
    gap: 2rem;
  }
  section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }
  section h2 {
    font-size: 1rem;
    margin-bottom: 1rem;
    color: var(--text-muted);
  }
  .section-title-with-icon {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    line-height: 1.2;
  }
  .section-title-with-icon :global(svg) {
    display: block;
    flex-shrink: 0;
  }
  .hint {
    color: var(--text-muted);
    font-size: 0.875rem;
    text-align: center;
    padding: 2rem;
  }
  .section-streaming-hint {
    margin-top: 1rem;
    color: var(--text-muted);
    font-size: 0.875rem;
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
  :global(.badge) {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-weight: 500;
    font-size: 0.75rem;
  }
  :global(.row-actions) {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  :global(.row-action-link) {
    color: var(--primary);
    font-size: 0.75rem;
    font-weight: 600;
    text-decoration: none;
    white-space: nowrap;
  }
  :global(.row-action-link:hover),
  :global(.row-action-link:focus-visible) {
    text-decoration: underline;
  }
  :global(.row-action-link.secondary) {
    color: var(--text-muted);
  }
  :global(.activity-entity-links) {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
  }
  :global(.activity-entity-link) {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    color: var(--primary);
    font-size: 0.75rem;
    font-weight: 600;
    text-decoration: none;
    white-space: nowrap;
  }
  :global(.activity-entity-link:hover),
  :global(.activity-entity-link:focus-visible) {
    text-decoration: underline;
  }
  :global(.entity-kind) {
    color: var(--text-muted);
    font-weight: 500;
  }
  :global(.entity-name) {
    color: var(--primary);
    font-weight: 600;
  }
  :global(.dashboard-entity-link) {
    color: var(--primary);
    font-size: 0.85rem;
    font-weight: 600;
    text-align: left;
    text-decoration: none;
  }
  :global(.dashboard-entity-link:hover),
  :global(.dashboard-entity-link:focus-visible) {
    text-decoration: underline;
  }
  :global(.activity-time) {
    border-bottom: 1px dotted var(--text-muted);
    cursor: help;
  }
  :global(.activity-event-trigger) {
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    font: inherit;
  }
  :global(.activity-event-trigger:hover .badge),
  :global(.activity-event-trigger:focus-visible .badge) {
    filter: brightness(1.1);
  }
  .dashboard-detail-dialog {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .dashboard-detail-dialog dl {
    display: grid;
    gap: 0.75rem;
    margin: 0;
  }
  .dashboard-detail-dialog dl > div {
    display: grid;
    grid-template-columns: 130px 1fr;
    gap: 0.75rem;
  }
  .dashboard-detail-dialog dt {
    color: var(--text-muted);
    font-size: 0.8rem;
  }
  .dashboard-detail-dialog dd {
    margin: 0;
    color: var(--text-primary);
    font-size: 0.9rem;
    word-break: break-word;
  }
  .dashboard-event-json {
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
  .drift-cause {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    padding: 0.85rem 1rem;
    border-radius: 8px;
    border: 1px solid var(--border-color);
    background: var(--hover-bg);
  }
  .drift-cause-error {
    border-color: color-mix(in srgb, var(--error) 45%, var(--border-color));
    background: color-mix(in srgb, var(--error) 10%, var(--card-bg));
  }
  .drift-cause-warning {
    border-color: color-mix(in srgb, var(--warning, #f59e0b) 45%, var(--border-color));
    background: color-mix(in srgb, var(--warning, #f59e0b) 10%, var(--card-bg));
  }
  .drift-cause-success {
    border-color: color-mix(in srgb, var(--success) 45%, var(--border-color));
    background: color-mix(in srgb, var(--success) 10%, var(--card-bg));
  }
  .drift-cause-badge {
    flex-shrink: 0;
    margin-top: 0.1rem;
  }
  .drift-cause-text {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .drift-cause-text strong {
    color: var(--text-primary);
    font-size: 0.95rem;
  }
  .drift-cause-text p {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.85rem;
    line-height: 1.4;
  }
  .drift-hash-flag {
    margin-left: 0.5rem;
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.02em;
  }
  .drift-hash-flag.mismatch {
    color: var(--error);
  }
  .drift-hash-flag.match {
    color: var(--success);
  }
  .drift-muted {
    color: var(--text-muted);
    font-style: italic;
  }
  .drift-raw > summary {
    cursor: pointer;
    color: var(--text-muted);
    font-size: 0.8rem;
    padding: 0.25rem 0;
    user-select: none;
  }
  .drift-raw > summary:hover {
    color: var(--text-primary);
  }
  .drift-raw[open] > summary {
    margin-bottom: 0.5rem;
  }
  :global(.badge.info) {
    background: color-mix(in srgb, var(--primary) 16%, transparent);
    color: var(--primary);
  }
  :global(.badge.warning) {
    background: color-mix(in srgb, var(--warning, #f59e0b) 18%, transparent);
    color: var(--warning, #f59e0b);
  }
  :global(.badge.success) {
    background: color-mix(in srgb, var(--success) 16%, transparent);
    color: var(--success);
  }
  :global(.badge.default) {
    background: color-mix(in srgb, var(--text-muted) 16%, transparent);
    color: var(--text-muted);
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
  :global(code) {
    font-family: 'SF Mono', Monaco, monospace;
    font-size: 0.8em;
  }
  @keyframes dashboard-spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
