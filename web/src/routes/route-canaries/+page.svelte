<script>
  import api from '$lib/api/client.js';
  import {
    routeCanaryKey,
    classificationLabel,
    classificationClass,
    transitionLabel,
    transitionClass,
    buildRouteCanarySummary,
    formatRouteTimestamp,
    instanceStatusLabel,
    instanceStatusClass,
    isNotFoundError
  } from '$lib/route-canaries.js';

  let rows = $state([]);
  let loading = $state(true);
  let error = $state('');
  let unavailable = $state(false);
  let openFilter = $state('');
  let search = $state('');
  let selected = $state(null);
  let detail = $state(null);
  let events = $state([]);
  let detailLoading = $state(false);
  let detailError = $state('');

  let summary = $derived(buildRouteCanarySummary(rows));
  let filteredRows = $derived.by(() => {
    const needle = search.trim().toLowerCase();
    return rows.filter((row) => {
      if (openFilter === 'open' && !row.open) return false;
      if (openFilter === 'closed' && row.open) return false;
      if (!needle) return true;
      return [row.hostname, row.classification, row.perspective, row.service_id, row.environment_id]
        .some((value) => String(value || '').toLowerCase().includes(needle));
    });
  });

  $effect(() => { void loadRows(); });

  async function loadRows() {
    loading = true;
    error = '';
    unavailable = false;
    try {
      rows = await api.listRouteCanaries();
      if (selected) {
        selected = rows.find((row) => routeCanaryKey(row) === routeCanaryKey(selected)) || null;
      }
    } catch (err) {
      rows = [];
      if (isNotFoundError(err)) {
        // Tier-2 gated: the route canary endpoints are not registered at all
        // when the feature is disabled, so treat that as "nothing to show"
        // rather than an error wall.
        unavailable = true;
      } else {
        error = err?.message || 'Failed to load route canary state';
      }
    } finally {
      loading = false;
    }
  }

  async function selectRoute(row) {
    selected = row;
    detail = null;
    events = [];
    detailLoading = true;
    detailError = '';
    const key = routeCanaryKey(row);
    try {
      const [nextDetail, nextEvents] = await Promise.all([
        api.getRouteCanary(row.service_id, row.environment_id, row.hostname, row.deployment_unit_id),
        api.listRouteCanaryEvents(row.service_id, row.environment_id, row.hostname, undefined, row.deployment_unit_id)
      ]);
      if (!selected || routeCanaryKey(selected) !== key) return;
      detail = nextDetail;
      events = nextEvents;
    } catch (err) {
      if (selected && routeCanaryKey(selected) === key) detailError = err?.message || 'Failed to load route canary detail';
    } finally {
      if (selected && routeCanaryKey(selected) === key) detailLoading = false;
    }
  }
</script>

<svelte:head><title>Route Canaries · Bahia</title></svelte:head>

<div class="page">
  <header class="hero">
    <div>
      <p class="eyebrow">Managed route outage detection</p>
      <h1>Route Canaries</h1>
      <p>Every managed route probed from the edge and the LAN. Surface outages, warnings, and the healthy-container-broken-route contradiction.</p>
    </div>
    <button class="secondary" type="button" onclick={loadRows} disabled={loading}>Refresh</button>
  </header>

  <section class="summary-grid" aria-label="Route canary summary">
    <article><span>Total routes</span><strong>{summary.total}</strong><em>monitored</em></article>
    <article class="critical"><span>Open outages</span><strong>{summary.open}</strong><em>active failures</em></article>
    <article class="healthy-broken"><span>Healthy container, broken route</span><strong>{summary.healthyContainerBrokenRoute}</strong><em>routing layer failure</em></article>
    <article class="warning"><span>Warnings</span><strong>{summary.warnings}</strong><em>TLS expiry or non-discriminating health paths</em></article>
  </section>

  <section class="panel controls">
    <label>Search <input bind:value={search} placeholder="hostname, classification, perspective" /></label>
    <label>Status
      <select bind:value={openFilter}>
        <option value="">All</option>
        <option value="open">Open only</option>
        <option value="closed">Closed only</option>
      </select>
    </label>
  </section>

  {#if loading}
    <p class="muted state">Loading route canary state…</p>
  {:else if unavailable}
    <section class="panel state" role="status">
      <strong>Route canary monitoring is not enabled.</strong>
      <p class="muted">Enable the <code>route_canaries</code> feature to surface managed-route outage detection here.</p>
    </section>
  {:else if error}
    <section class="panel state error" role="alert"><p>{error}</p><button type="button" onclick={loadRows}>Retry</button></section>
  {:else if filteredRows.length === 0}
    <p class="muted state">No route canary states match the current filters.</p>
  {:else}
    <section class="route-grid" aria-label="Route canary states">
      {#each filteredRows as row (routeCanaryKey(row))}
        <button class:selected={selected && routeCanaryKey(selected) === routeCanaryKey(row)} class="route-card" type="button" onclick={() => selectRoute(row)}>
          <header><strong>{row.hostname}</strong><span class={`badge ${classificationClass(row.classification, row.open, row.consecutive_failures)}`}>{classificationLabel(row.classification)}</span></header>
          <p>{row.perspective || 'unknown'} · {row.open ? 'Open' : 'Closed'}</p>
          {#if row.open && row.service_healthy_route_broken}
            <p class="contradiction">⚠ Container healthy, route broken</p>
          {/if}
          <dl>
            <div><dt>Consecutive failures</dt><dd>{row.consecutive_failures}</dd></div>
            <div><dt>Consecutive successes</dt><dd>{row.consecutive_successes}</dd></div>
            <div><dt>Instance status</dt><dd><span class={`badge ${instanceStatusClass(row.observed_instance_status)}`}>{instanceStatusLabel(row.observed_instance_status) || 'unknown'}</span></dd></div>
            <div><dt>Last observed</dt><dd>{formatRouteTimestamp(row.last_observed_at)}</dd></div>
          </dl>
          {#if row.failure_reason}<p class="reason">{row.failure_reason}</p>{/if}
        </button>
      {/each}
    </section>
  {/if}

  {#if selected}
    <section class="panel detail-panel">
      <header class="section-heading"><div><h2>{selected.hostname}</h2><p>{selected.service_id} / {selected.environment_id} · {selected.perspective || 'unknown'}</p></div></header>
      {#if detailLoading}<p class="muted">Loading detail and recent events…</p>
      {:else if detailError}<p class="error">{detailError}</p>
      {:else if detail}
        <dl class="detail-grid">
          <div><dt>Classification</dt><dd><span class={`badge ${classificationClass(detail.classification, detail.open, detail.consecutive_failures)}`}>{classificationLabel(detail.classification)}</span></dd></div>
          <div><dt>Open</dt><dd>{detail.open ? 'Yes' : 'No'}</dd></div>
          <div><dt>Perspective</dt><dd>{detail.perspective || 'unknown'}</dd></div>
          <div><dt>Consecutive failures</dt><dd>{detail.consecutive_failures}</dd></div>
          <div><dt>Consecutive successes</dt><dd>{detail.consecutive_successes}</dd></div>
          <div><dt>Instance status</dt><dd><span class={`badge ${instanceStatusClass(detail.observed_instance_status)}`}>{instanceStatusLabel(detail.observed_instance_status) || 'unknown'}</span></dd></div>
          <div><dt>Service healthy, route broken</dt><dd>{detail.service_healthy_route_broken ? 'Yes' : 'No'}</dd></div>
          <div><dt>Opened at</dt><dd>{formatRouteTimestamp(detail.opened_at)}</dd></div>
          <div><dt>Last observed</dt><dd>{formatRouteTimestamp(detail.last_observed_at)}</dd></div>
          <div><dt>Last recovered</dt><dd>{formatRouteTimestamp(detail.last_recovered_at)}</dd></div>
          {#if detail.tls_not_after}<div><dt>TLS not after</dt><dd>{formatRouteTimestamp(detail.tls_not_after)}</dd></div>{/if}
        </dl>
        {#if detail.failure_reason}<p class="reason">{detail.failure_reason}</p>{/if}

        <div class="events-section">
          <h3>Recent events</h3>
          {#if events.length === 0}
            <p class="muted">No events recorded.</p>
          {:else}
            <div class="events-list">
              {#each events as event}
                <article>
                  <header><span class={`badge ${transitionClass(event.transition)}`}>{transitionLabel(event.transition)}</span><time>{formatRouteTimestamp(event.observed_at)}</time></header>
                  <p>{classificationLabel(event.classification)}{event.reason ? ` — ${event.reason}` : ''}</p>
                </article>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .page { display: grid; gap: 1.5rem; padding: 2rem; }
  .hero, .section-heading, .route-card header { align-items: flex-start; display: flex; justify-content: space-between; gap: 1rem; }
  .hero h1 { margin: 0.15rem 0; }
  .hero p { color: var(--text-muted); margin: 0; }
  .eyebrow { color: var(--primary) !important; font-size: 0.72rem; font-weight: 700; letter-spacing: 0.12em; text-transform: uppercase; }
  button { cursor: pointer; }
  button:disabled { cursor: not-allowed; opacity: 0.55; }
  .secondary, .state button { background: transparent; border: 1px solid var(--border-color); border-radius: 7px; color: var(--text); padding: 0.65rem 0.9rem; }
  .summary-grid { display: grid; gap: 0.8rem; grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .summary-grid article, .panel, .route-card { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 10px; }
  .summary-grid article { border-top: 3px solid var(--primary); display: grid; gap: 0.25rem; padding: 1rem; }
  .summary-grid article.critical { border-top-color: #ef4444; } .summary-grid article.warning { border-top-color: #f59e0b; } .summary-grid article.healthy-broken { border-top-color: #f97316; }
  .summary-grid span, .summary-grid em { color: var(--text-muted); font-size: 0.74rem; font-style: normal; } .summary-grid strong { font-size: 1.7rem; }
  .panel { padding: 1.1rem; }
  .controls { align-items: end; display: flex; gap: 1rem; }
  label { color: var(--text-muted); display: grid; font-size: 0.78rem; gap: 0.35rem; }
  input, select { background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; color: var(--text); min-width: 210px; padding: 0.55rem; }
  .route-grid { display: grid; gap: 0.9rem; grid-template-columns: repeat(auto-fill, minmax(315px, 1fr)); }
  .route-card { color: var(--text); padding: 1rem; text-align: left; width: 100%; }
  .route-card.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .route-card > p { color: var(--text-muted); font-size: 0.82rem; }
  .contradiction { color: #f97316 !important; font-weight: 700; }
  dl { display: grid; gap: 0.55rem; grid-template-columns: 1fr 1fr; margin: 0.85rem 0; }
  dl div { display: grid; gap: 0.15rem; } dt { color: var(--text-muted); font-size: 0.7rem; text-transform: uppercase; } dd { margin: 0; overflow-wrap: anywhere; }
  .badge { border-radius: 999px; font-size: 0.68rem; font-weight: 700; padding: 0.25rem 0.5rem; text-transform: uppercase; }
  .badge.healthy { background: rgba(34,197,94,.15); color: #4ade80; } .badge.warning { background: rgba(245,158,11,.15); color: #fbbf24; } .badge.degraded { background: rgba(249,115,22,.15); color: #fb923c; } .badge.critical { background: rgba(239,68,68,.15); color: #f87171; } .badge.unknown { background: rgba(148,163,184,.15); color: #94a3b8; }
  .reason, .error { color: #f87171; } .muted { color: var(--text-muted); } .state { padding: 2rem; text-align: center; }
  .detail-panel { display: grid; gap: 1.2rem; } .section-heading h2, h3 { margin: 0; } .section-heading p { color: var(--text-muted); font-size: .75rem; overflow-wrap: anywhere; }
  .detail-grid { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); margin: 0; }
  .events-section { border-top: 1px solid var(--border-color); padding-top: 1rem; }
  .events-list { display: grid; gap: .65rem; margin-top: .75rem; } .events-list article { border: 1px solid var(--border-color); border-radius: 7px; padding: .75rem; } .events-list header { align-items: flex-start; display: flex; justify-content: space-between; gap: 1rem; } .events-list p { margin-bottom: 0; } time { color: var(--text-muted); font-size: .72rem; }
  @media (max-width: 1100px) { .summary-grid { grid-template-columns: repeat(2, 1fr); } }
  @media (max-width: 700px) { .page { padding: 1rem; } .summary-grid { grid-template-columns: 1fr; } .controls, .hero { align-items: stretch; flex-direction: column; } input, select { min-width: 0; width: 100%; } }
</style>