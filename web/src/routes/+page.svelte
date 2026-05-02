<script>
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import { api } from '$lib/api/client.js';
  import { services, environments, states, workers, driftedStates, events, loading } from '$lib/stores';
  import { formatDashboardSats, normalizePaymentHistory, summarizeRecentSpend } from './dashboard-cost-summary.js';

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

  function dashboardWorkerPubkey(worker) {
    return firstPresentString(worker?.pubkey, worker?.worker_pubkey, worker?.id);
  }

  function pluralize(count, singular, plural = `${singular}s`) {
    return count === 1 ? singular : plural;
  }

  function emptyCostSummary(workerCount = 0) {
    return { totalSats: 0, paymentCount: 0, workerCount, latestPaymentAt: '' };
  }

  function eventData(row) {
    return row?.data && typeof row.data === 'object' && !Array.isArray(row.data) ? row.data : {};
  }

  function addActivityEntityLink(links, seenHrefs, label, basePath, id) {
    if (!id) return;

    const entityId = String(id);
    const href = `${basePath}/${encodeURIComponent(entityId)}`;
    if (seenHrefs.has(href)) return;

    seenHrefs.add(href);
    links.push(
      `<a class="activity-entity-link" href="${href}" title="${escapeHtml(`${label} ${entityId}`)}" aria-label="${escapeHtml(`${label} ${entityId}`)}">` +
        `<span class="entity-kind">${escapeHtml(label)}</span> <code>${escapeHtml(entityId.slice(0, 8))}...</code>` +
      `</a>`
    );
  }

  function dashboardActivityEntityLinks(row) {
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
    const deploymentId = firstPresentString(
      data.deployment_id,
      data.deploymentId,
      data.intent_id,
      data.intentId,
      data.deployment_intent_id,
      data.deploymentIntentId,
      type.startsWith('deployment.') && !serviceId && !environmentId ? entityId : ''
    );

    const links = [];
    const seenHrefs = new Set();
    addActivityEntityLink(links, seenHrefs, 'Deployment', '/deployments', deploymentId);
    addActivityEntityLink(links, seenHrefs, 'Service', '/services', serviceId);
    addActivityEntityLink(links, seenHrefs, 'Environment', '/environments', environmentId);

    if (links.length > 0) {
      return `<div class="activity-entity-links">${links.join('')}</div>`;
    }

    return entityId ? `<code>${escapeHtml(entityId.slice(0, 8))}...</code>` : '-';
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

    if (!api || workerPubkeys.length === 0) {
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
                await api.getPaymentHistory({ worker, limit: COST_HISTORY_LIMIT_PER_WORKER })
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
    if (!api) {
      pendingDeployments = [];
      return;
    }

    // Try to display cached count first
    const cachedCount = getCachedPendingCount();
    if (cachedCount !== null) {
      pendingDeployments = new Array(cachedCount); // Placeholder
    }

    pendingLoading = true;
    pendingError = null;
    pendingPairFailures = 0;

    try {
      // Reuse global stores if available, fallback to API calls
      let servicesList = services;
      let envsList = environments;

      if (servicesList.length === 0 || envsList.length === 0) {
        const [fetchedServices, fetchedEnvs] = await Promise.all([
          servicesList.length === 0 ? api.listServices().catch(() => []) : servicesList,
          envsList.length === 0 ? api.listEnvironments().catch(() => []) : envsList
        ]);
        servicesList = fetchedServices;
        envsList = fetchedEnvs;
      }

      // Skip intent calls if either list is empty
      if (servicesList.length === 0 || envsList.length === 0) {
        pendingDeployments = [];
        cachePendingCount(0);
        return;
      }

      // Build tasks for all service/environment pairs
      const intentTasks = [];
      const intentMap = new Map(); // dedupe by intent.id

      for (const service of servicesList) {
        for (const env of envsList) {
          intentTasks.push(async () => {
            try {
              const intents = await api.listIntents(service.id, env.id);
              if (Array.isArray(intents)) {
                intents.forEach(intent => {
                  if (intent?.id) {
                    intentMap.set(intent.id, intent);
                  }
                });
              }
            } catch (err) {
              // Track per-pair failures instead of logging each
              pendingPairFailures++;
            }
          });
        }
      }

      // Fetch with bounded concurrency (limit 6)
      await withBoundedConcurrency(intentTasks, 6);

      // Filter for pending approvals
      pendingDeployments = Array.from(intentMap.values()).filter(intent => {
        const status = String(intent.approval_status || '').toLowerCase();
        return status === 'pending';
      });

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
    { key: 'service_id', label: 'Service', render: (r) => `<code>${escapeHtml(r.service_id?.slice(0, 8) || '')}...</code>` },
    { key: 'environment_id', label: 'Environment', render: (r) => `<code>${escapeHtml(r.environment_id?.slice(0, 8) || '')}...</code>` },
    { key: 'drift_status', label: 'Drift', render: (r) => {
      const driftStatus = String(r.drift_status || 'unknown');
      const variant = driftStatus === 'in_sync' ? 'success' : driftStatus === 'drifted' ? 'error' : 'default';
      return `<span class="badge-${variant}">${escapeHtml(driftStatus)}</span>`;
    }},
    { key: 'actions', label: 'Actions', render: dashboardStateActions }
  ]);
  let eventColumns = $derived([
    { key: 'time', label: 'Time', render: (r) => r.time?.slice(11, 19) || '-' },
    { key: 'type', label: 'Event', render: (r) => {
      const type = r.type || '';
      const variant = eventBadgeVariant(type);
      const escaped = escapeHtml(type);
      return `<span class="badge ${variant}">${escaped}</span>`;
    }},
    { key: 'entity_id', label: 'Entity', render: dashboardActivityEntityLinks }
  ]);
  let pendingCount = $derived(pendingDeployments.length);
  let pendingSubtitle = $derived(pendingError
    ? 'Unable to load'
    : pendingCount > 0
      ? 'Needs review'
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
</script>

<div class="dashboard">
  <h1>Dashboard</h1>
  
  <div class="quick-actions">
    <a href="/services" class="action-link">+ Create Service</a>
    <a href="/deployments" class="action-link">View Deployments</a>
  </div>

  <div class="stats">
    <Card title="Services" value={services.length} subtitle="Total registered" />
    <Card title="Environments" value={environments.length} subtitle="Configured" />
    <Card title="Workers" value={workers.length} subtitle="Available" />
    <a href="#environment-states" class="card-link drift-card-link" aria-label="Review drifted environment states">
      <Card 
        title="Drifted" 
        value={driftedStates().length} 
        subtitle={driftedStates().length > 0 ? 'Review drifted rows' : 'All clear'}
        status={driftedStates().length > 0 ? 'error' : 'success'}
      >
        <span class="card-action">Review states</span>
      </Card>
    </a>
    <a href="/deployments/pending" class="card-link">
      <Card
        title="Pending Approvals"
        value={pendingLoading ? '...' : pendingCount}
        subtitle={pendingSubtitle}
        status={pendingError ? 'error' : pendingCount > 0 ? 'warning' : 'success'}
      />
    </a>
    <a href="/payments" class="card-link" aria-label="Review payment history cost summary">
      <Card
        title="Recent Spend"
        value={costSummaryValue}
        subtitle={costSummarySubtitle}
        status={costSummaryStatus}
      />
    </a>
  </div>

  <div class="sections">
    <section id="environment-states">
      <h2>Environment States</h2>
      <Table columns={stateColumns} data={states.slice(0, 10)} />
    </section>

    <section>
      <h2>Recent Activity</h2>
      <Table columns={eventColumns} data={events.slice(0, 10)} />
      {#if events.length === 0}
        <p class="hint">Events will appear here in real-time from the relay-backed control plane</p>
      {/if}
    </section>
  </div>
</div>

<style>
  .dashboard h1 {
    margin-bottom: 1rem;
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
    border-radius: 6px;
    text-decoration: none;
    font-size: 0.875rem;
    font-weight: 500;
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
    text-decoration: none;
    color: inherit;
  }
  .drift-card-link :global(.card) {
    height: 100%;
    transition: border-color 0.2s, transform 0.2s;
  }
  .drift-card-link:hover :global(.card),
  .drift-card-link:focus-visible :global(.card) {
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
  .hint {
    color: var(--text-muted);
    font-size: 0.875rem;
    text-align: center;
    padding: 2rem;
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
  :global(.badge.info) {
    background: #1e3a5f;
    color: #93c5fd;
  }
  :global(.badge.warning) {
    background: #78350f;
    color: #fcd34d;
  }
  :global(.badge.success) {
    background: #065f46;
    color: #6ee7b7;
  }
  :global(code) {
    font-family: 'SF Mono', Monaco, monospace;
    font-size: 0.8em;
  }
</style>
