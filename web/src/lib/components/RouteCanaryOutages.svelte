<script>
  import { boot, onStoreRefresh } from '$lib/nostr/boot.js';
  import { routeCanaryRows } from '$lib/stores/operational-views.js';
  import {
    routeCanaryKey, classificationLabel, classificationClass, formatRouteTimestamp,
    instanceStatusLabel, instanceStatusClass
  } from '$lib/route-canaries.js';
  import EmptyState from './EmptyState.svelte';
  import { WarningIcon } from '$lib/icons/domain-icons.js';

  let {
    serviceId = null, environmentId = null, open = null, showServiceNames = false,
    resolveServiceName = null, emptyTitle = 'No route canaries configured',
    emptyMessage = 'No managed routes are currently being probed.', onCount = null
  } = $props();
  let rows = $state([]);
  let loading = $state(true);
  let error = $state('');
  let unavailable = $state(false);

  $effect(() => {
    const scopedService = serviceId, scopedEnvironment = environmentId, scopedOpen = open;
    const refresh = () => load(scopedService, scopedEnvironment, scopedOpen);
    const stop = onStoreRefresh(refresh);
    void boot().then(refresh).catch(err => { error = err?.message || 'Route canary state unavailable'; loading = false; });
    return stop;
  });

  function load(scopedServiceId, scopedEnvironmentId, scopedOpen) {
    rows = routeCanaryRows().filter(row =>
      (!scopedServiceId || row.service_id === scopedServiceId) &&
      (!scopedEnvironmentId || row.environment_id === scopedEnvironmentId) &&
      (scopedOpen === null || scopedOpen === undefined || !scopedOpen || row.open));
    onCount?.(rows.length);
    loading = false;
    error = '';
  }
</script>

{#if loading}
  <p class="muted">Loading route canary state…</p>
{:else if unavailable}
  <EmptyState
    iconComponent={WarningIcon}
    title="Route canary monitoring is not enabled"
    message="Enable the route_canaries feature to surface managed-route outage detection here."
  />
{:else if error}
  <EmptyState
    iconComponent={WarningIcon}
    title="Route canary state unavailable"
    message={error}
  />
{:else if rows.length > 0}
  <div class="route-canary-list">
    {#each rows as row (routeCanaryKey(row))}
      <a href="/route-canaries" class="route-canary-row">
        <div class="route-canary-info">
          {#if showServiceNames}
            <strong>{resolveServiceName ? resolveServiceName(row.service_id) : row.service_id}</strong>
          {/if}
          <code class="route-canary-hostname">{row.hostname}</code>
          <span class="badge-sm {classificationClass(row.classification, row.open, row.consecutive_failures)}">{classificationLabel(row.classification)}</span>
          {#if row.observed_instance_status}
            <span class="badge-sm {instanceStatusClass(row.observed_instance_status)}">Instance: {instanceStatusLabel(row.observed_instance_status)}</span>
          {/if}
          {#if row.open && row.service_healthy_route_broken}
            <span class="contradiction-badge">⚠ Container healthy, route broken</span>
          {/if}
        </div>
        <div class="route-canary-meta">
          <span>{row.perspective || 'unknown'}</span>
          <span>{row.open ? 'Open' : 'Closed'}</span>
          <span>{formatRouteTimestamp(row.last_observed_at)}</span>
        </div>
      </a>
    {/each}
  </div>
{:else}
  <EmptyState
    iconComponent={WarningIcon}
    title={emptyTitle}
    message={emptyMessage}
  />
{/if}

<style>
  .muted { color: var(--text-muted); }
  .route-canary-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .route-canary-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem;
    background: var(--hover-bg);
    border-radius: 4px;
    border: 1px solid var(--border-color);
    text-decoration: none;
    color: inherit;
    transition: background 0.2s;
    gap: 1rem;
    flex-wrap: wrap;
  }
  .route-canary-row:hover {
    background: var(--bg);
    border-color: var(--primary);
  }
  .route-canary-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex-wrap: wrap;
  }
  .route-canary-hostname {
    font-size: 0.8rem;
    color: var(--text-muted);
  }
  .route-canary-meta {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.75rem;
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .badge-sm {
    display: inline-flex;
    align-items: center;
    font-size: 0.65rem;
    font-weight: 600;
    padding: 0.15rem 0.45rem;
    border-radius: 999px;
    text-transform: uppercase;
  }
  .badge-sm.healthy { background: rgba(34,197,94,.15); color: #4ade80; }
  .badge-sm.warning { background: rgba(245,158,11,.15); color: #fbbf24; }
  .badge-sm.degraded { background: rgba(249,115,22,.15); color: #fb923c; }
  .badge-sm.critical { background: rgba(239,68,68,.15); color: #f87171; }
  .badge-sm.unknown { background: rgba(148,163,184,.15); color: #94a3b8; }
  .contradiction-badge {
    font-size: 0.7rem;
    font-weight: 700;
    color: #f97316;
  }
</style>
