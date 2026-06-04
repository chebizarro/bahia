<script>
  import { untrack } from 'svelte';
  import { StandardIcon } from '$lib/icons/domain-icons.js';
  import { workers, workerAssignments, workerCleanupExecutions, loading, loadWorkers } from '$lib/stores';
  import CleanupRequestDialog from '../workers/CleanupRequestDialog.svelte';
  import FleetWeatherMap from './FleetWeatherMap.svelte';
  import {
    activeCleanupByWorker,
    buildFleetHealthSummary,
    buildFleetWeatherNodes,
    cleanupExecutionsForWorker,
    formatFleetTimestamp,
    sortCleanupExecutions
  } from './page-model.js';

  let initialized = $state(false);
  let selectedWorker = $state(null);
  let cleanupDialogOpen = $state(false);
  let statusFilter = $state('');

  $effect(() => {
    if (initialized) return;
    initialized = true;
    void untrack(() => loadWorkers());
  });

  let summary = $derived(buildFleetHealthSummary(workers, workerCleanupExecutions, workerAssignments));
  let weatherNodes = $derived(buildFleetWeatherNodes(workers, workerCleanupExecutions, workerAssignments));
  let activeCleanupMap = $derived(activeCleanupByWorker(workerCleanupExecutions));
  let cleanupHistory = $derived(sortCleanupExecutions(workerCleanupExecutions).filter((execution) => !statusFilter || execution.status === statusFilter));
  let actionItems = $derived(weatherNodes.filter((node) => node.capacity === 'blocked' || node.capacity === 'cleanup_only' || node.recommendedAction === 'cleanup_recommended' || !node.telemetryPresent));
  let selectedActiveCleanup = $derived(selectedWorker ? activeCleanupMap.get(selectedWorker.pubkey) || null : null);

  function openCleanup(worker) {
    selectedWorker = worker;
    cleanupDialogOpen = true;
  }

  function closeCleanup() {
    cleanupDialogOpen = false;
  }

  function workerName(pubkey) {
    return workers.find((worker) => worker.pubkey === pubkey)?.name || pubkey?.slice(0, 12) || 'worker';
  }
</script>

<svelte:head>
  <title>Fleet Health · Bahia</title>
</svelte:head>

<div class="page">
  <header class="hero">
    <div>
      <p class="eyebrow">Resource pressure</p>
      <h1>
        <StandardIcon size={30} strokeWidth={1.75} ariaHidden="true" />
        Fleet Health
      </h1>
      <p class="subtitle">Fleet weather map, resource-pressure admission posture, and cleanup orchestration status.</p>
    </div>
    <a class="secondary-link" href="/workers">Worker catalog</a>
  </header>

  <section class="summary-grid" aria-label="Fleet health summary">
    <article><span>Total workers</span><strong>{summary.total}</strong><em>{summary.activity.live} live · {summary.activity.recent} recent</em></article>
    <article class="blocked"><span>Blocked</span><strong>{summary.capacity.blocked}</strong><em>deployment admission denies normal work</em></article>
    <article class="cleanup"><span>Cleanup only</span><strong>{summary.capacity.cleanup_only}</strong><em>{summary.recommended.cleanup_recommended} cleanup recommendations</em></article>
    <article class="watch"><span>Pressure watch</span><strong>{summary.pressure.warning + summary.pressure.critical}</strong><em>{summary.telemetry.missing} missing telemetry</em></article>
    <article><span>Cleanup active</span><strong>{summary.cleanup.active}</strong><em>{summary.cleanup.completed} completed · {summary.cleanup.failed} failed</em></article>
  </section>

  <section class="panel">
    <div class="section-heading">
      <div>
        <h2>Fleet weather map</h2>
        <p>Workers are grouped by admission posture so pressure hotspots are visible before deployment.</p>
      </div>
    </div>
    {#if loading.workers && weatherNodes.length === 0}
      <p class="muted">Loading fleet read models from Nostr…</p>
    {:else}
      <FleetWeatherMap nodes={weatherNodes} onCleanup={openCleanup} />
    {/if}
  </section>

  <div class="dashboard-grid">
    <section class="panel">
      <div class="section-heading">
        <div>
          <h2>Cleanup status and history</h2>
          <p>Durable cleanup lifecycle projected from Bahia cleanup orchestration events.</p>
        </div>
        <label>
          <span>Status</span>
          <select bind:value={statusFilter}>
            <option value="">All</option>
            <option value="requested">requested</option>
            <option value="dispatched">dispatched</option>
            <option value="running">running</option>
            <option value="completed">completed</option>
            <option value="failed">failed</option>
          </select>
        </label>
      </div>

      {#if cleanupHistory.length === 0}
        <p class="muted">No cleanup execution state has been published yet.</p>
      {:else}
        <div class="cleanup-list">
          {#each cleanupHistory as execution}
            <article class={`cleanup-row status-${execution.status}`}>
              <header>
                <div>
                  <strong>{workerName(execution.worker_pubkey)}</strong>
                  <span>{execution.cleanup_mode || 'cleanup'} · {execution.status || 'unknown'}</span>
                </div>
                <a href={`/workers/${encodeURIComponent(execution.worker_pubkey)}`}>Worker</a>
              </header>
              <p>{execution.reason || 'No operator reason recorded'}</p>
              <dl>
                <div><dt>Loom job</dt><dd>{execution.loom_job_id || 'not dispatched'}</dd></div>
                <div><dt>Target free</dt><dd>{execution.target_free_gb ? `${execution.target_free_gb} GB` : 'not specified'}</dd></div>
                <div><dt>Started</dt><dd>{formatFleetTimestamp(execution.started_at)}</dd></div>
                <div><dt>Completed</dt><dd>{formatFleetTimestamp(execution.completed_at)}</dd></div>
              </dl>
              {#if execution.protected_refs?.length}
                <div class="chips">
                  {#each execution.protected_refs as ref}
                    <span>{ref}</span>
                  {/each}
                </div>
              {/if}
              {#if execution.error}
                <p class="error">{execution.error}</p>
              {/if}
            </article>
          {/each}
        </div>
      {/if}
    </section>

    <aside class="panel action-rail">
      <h2>Action rail</h2>
      <p class="muted">Prioritized remediation and observability gaps.</p>
      {#if actionItems.length === 0}
        <p class="clear">No resource-pressure action items.</p>
      {:else}
        <div class="action-list">
          {#each actionItems as node}
            <article>
              <header>
                <strong>{node.name}</strong>
                <span>{node.capacity}</span>
              </header>
              <p>{node.telemetryPresent ? `${node.dominantSignal.label}: ${node.dominantSignal.value}` : 'Telemetry missing from latest worker advertisement.'}</p>
              <div class="row-actions">
                <a href={`/workers/${encodeURIComponent(node.id)}`}>Inspect</a>
                {#if !node.cleanup && (node.capacity === 'cleanup_only' || node.recommendedAction === 'cleanup_recommended')}
                  <button type="button" onclick={() => openCleanup(node.worker)}>Cleanup</button>
                {/if}
              </div>
              {#if cleanupExecutionsForWorker(workerCleanupExecutions, node.id)[0]}
                {@const lastCleanup = cleanupExecutionsForWorker(workerCleanupExecutions, node.id)[0]}
                <small>Last cleanup {lastCleanup.status} · {formatFleetTimestamp(lastCleanup.updated_at || lastCleanup.started_at)}</small>
              {/if}
            </article>
          {/each}
        </div>
      {/if}
    </aside>
  </div>
</div>

<CleanupRequestDialog
  open={cleanupDialogOpen}
  worker={selectedWorker}
  activeCleanup={selectedActiveCleanup}
  source="web.fleet-health"
  onClose={closeCleanup}
  onSubmitted={() => { cleanupDialogOpen = false; }}
/>

<style>
  .page {
    display: grid;
    gap: 1.5rem;
    padding: 2rem;
  }

  .hero,
  .section-heading,
  .cleanup-row header,
  .action-list header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }

  .eyebrow {
    margin: 0 0 0.25rem;
    color: var(--primary, #818cf8);
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    font-weight: 900;
  }

  h1,
  h2,
  p { margin-top: 0; }

  h1 {
    display: flex;
    align-items: center;
    gap: 0.65rem;
    margin-bottom: 0.35rem;
  }

  .subtitle,
  .muted,
  .section-heading p,
  .cleanup-row p,
  .action-list p {
    color: var(--text-muted);
  }

  .secondary-link,
  .cleanup-row a,
  .action-list a,
  .action-list button {
    border: 1px solid var(--border-color);
    border-radius: 10px;
    color: var(--text-primary);
    background: transparent;
    padding: 0.55rem 0.8rem;
    text-decoration: none;
    cursor: pointer;
    font-weight: 800;
  }

  .summary-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: 1rem;
  }

  .summary-grid article,
  .panel {
    border: 1px solid var(--border-color);
    border-radius: 18px;
    background: var(--card-bg, rgba(15, 23, 42, 0.7));
  }

  .summary-grid article {
    padding: 1rem;
  }

  .summary-grid span,
  .summary-grid em {
    display: block;
    color: var(--text-muted);
    font-style: normal;
  }

  .summary-grid strong {
    display: block;
    margin: 0.25rem 0;
    font-size: 2rem;
  }

  .summary-grid .blocked { border-color: rgba(239, 68, 68, 0.55); }
  .summary-grid .cleanup { border-color: rgba(249, 115, 22, 0.55); }
  .summary-grid .watch { border-color: rgba(234, 179, 8, 0.55); }

  .panel {
    padding: 1.1rem;
  }

  .dashboard-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.8fr) minmax(320px, 0.9fr);
    gap: 1rem;
    align-items: start;
  }

  label {
    display: grid;
    gap: 0.25rem;
    color: var(--text-muted);
    font-size: 0.82rem;
  }

  select {
    border: 1px solid var(--border-color);
    border-radius: 8px;
    background: var(--input-bg, #0f172a);
    color: var(--text-primary);
    padding: 0.45rem 0.65rem;
  }

  .cleanup-list,
  .action-list {
    display: grid;
    gap: 0.75rem;
  }

  .cleanup-row,
  .action-list article {
    border: 1px solid var(--border-color);
    border-radius: 14px;
    padding: 0.9rem;
    background: rgba(2, 6, 23, 0.25);
  }

  .cleanup-row header span,
  .action-list header span {
    display: block;
    color: var(--text-muted);
    margin-top: 0.2rem;
  }

  dl {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 0.65rem;
    margin: 0.75rem 0 0;
  }

  dt { color: var(--text-muted); font-size: 0.72rem; text-transform: uppercase; }
  dd { margin: 0.15rem 0 0; overflow-wrap: anywhere; }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
    margin-top: 0.75rem;
  }

  .chips span {
    border: 1px solid rgba(59, 130, 246, 0.45);
    border-radius: 999px;
    color: #bfdbfe;
    padding: 0.2rem 0.45rem;
    font-size: 0.75rem;
  }

  .error { color: #fecaca; }
  .clear { color: #bbf7d0; font-weight: 800; }

  .row-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }

  .action-list small {
    display: block;
    color: var(--text-muted);
    margin-top: 0.65rem;
  }

  @media (max-width: 980px) {
    .page { padding: 1rem; }
    .dashboard-grid { grid-template-columns: 1fr; }
    .hero,
    .section-heading { flex-direction: column; }
  }
</style>
