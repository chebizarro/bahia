<script>
  import { FLEET_CAPACITY_LANES } from './page-model.js';

  let { nodes = [], onCleanup = () => {} } = $props();

  function nodesForLane(lane) {
    return nodes.filter((node) => (node.lane || node.capacity) === lane.key);
  }
</script>

<div class="weather-map" aria-label="Fleet weather topology pressure map">
  {#each FLEET_CAPACITY_LANES as lane}
    {@const laneNodes = nodesForLane(lane)}
    <section class={`lane lane-${lane.key}`} aria-label={`${lane.label}: ${laneNodes.length} workers`}>
      <header>
        <div>
          <h3>{lane.label}</h3>
          <p>{lane.description}</p>
        </div>
        <strong>{laneNodes.length}</strong>
      </header>

      <div class="lane-body">
        {#if laneNodes.length === 0}
          <p class="empty">No workers in this lane.</p>
        {:else}
          {#each laneNodes as node}
            <article class={`worker-card pressure-${node.pressure}`}>
              <div class="worker-card-header">
                <div>
                  <a href={`/workers/${encodeURIComponent(node.id)}`}>{node.name}</a>
                  <code>{node.id.slice(0, 12)}…</code>
                </div>
                <span class={`status liveness-${node.liveness}`}>{node.liveness}</span>
              </div>

              <div class="signal">
                <span>{node.dominantSignal.label}</span>
                <strong>{node.dominantSignal.value}</strong>
                <em>{node.dominantSignal.level}</em>
              </div>

              <dl>
                <div><dt>Capacity</dt><dd>{node.capacity}</dd></div>
                <div><dt>Pressure</dt><dd>{node.pressure}</dd></div>
                <div><dt>Action</dt><dd>{node.recommendedAction}</dd></div>
                <div><dt>Assignments</dt><dd>{node.assignmentCount}</dd></div>
              </dl>

              {#if node.telemetryIndicators.length > 0}
                <div class="telemetry">
                  {#each node.telemetryIndicators.slice(0, 4) as [label, value]}
                    <span><strong>{label}</strong> {value}</span>
                  {/each}
                </div>
              {:else}
                <p class="missing">Telemetry missing</p>
              {/if}

              {#if node.cleanup}
                <p class="cleanup-active">Cleanup {node.cleanup.status}{node.cleanup.loom_job_id ? ` · ${node.cleanup.loom_job_id}` : ''}</p>
              {:else if node.recommendedAction === 'cleanup_recommended' || node.capacity === 'cleanup_only'}
                <button type="button" onclick={() => onCleanup(node.worker)}>Open cleanup</button>
              {/if}
            </article>
          {/each}
        {/if}
      </div>
    </section>
  {/each}
</div>

<style>
  .weather-map {
    display: grid;
    grid-template-columns: repeat(4, minmax(240px, 1fr));
    gap: 1rem;
  }

  .lane {
    min-width: 0;
    border: 1px solid var(--border-color);
    border-radius: 18px;
    background: var(--card-bg, rgba(15, 23, 42, 0.65));
    overflow: hidden;
  }

  .lane header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    padding: 1rem;
    border-bottom: 1px solid var(--border-color);
  }

  .lane h3 { margin: 0; font-size: 1rem; }
  .lane p { margin: 0.25rem 0 0; color: var(--text-muted); font-size: 0.82rem; line-height: 1.35; }
  .lane header strong { font-size: 1.8rem; }

  .lane-blocked header { background: linear-gradient(135deg, rgba(127, 29, 29, 0.42), transparent); }
  .lane-cleanup_only header { background: linear-gradient(135deg, rgba(154, 52, 18, 0.42), transparent); }
  .lane-reduced header { background: linear-gradient(135deg, rgba(113, 63, 18, 0.42), transparent); }
  .lane-open header { background: linear-gradient(135deg, rgba(20, 83, 45, 0.35), transparent); }
  .lane-no_telemetry header { background: linear-gradient(135deg, rgba(51, 65, 85, 0.42), transparent); }

  .lane-body {
    display: grid;
    gap: 0.75rem;
    padding: 0.75rem;
  }

  .worker-card {
    border: 1px solid color-mix(in srgb, var(--border-color) 80%, transparent);
    border-radius: 14px;
    padding: 0.85rem;
    background: rgba(2, 6, 23, 0.28);
  }

  .worker-card-header {
    display: flex;
    justify-content: space-between;
    gap: 0.75rem;
    align-items: flex-start;
  }

  .worker-card a {
    color: var(--text-primary);
    font-weight: 800;
    text-decoration: none;
  }

  .worker-card a:hover { text-decoration: underline; }
  code { display: block; margin-top: 0.2rem; color: var(--text-muted); font-size: 0.75rem; }

  .status {
    border: 1px solid var(--border-color);
    border-radius: 999px;
    padding: 0.18rem 0.45rem;
    font-size: 0.72rem;
    text-transform: uppercase;
  }

  .liveness-online { border-color: rgba(34, 197, 94, 0.55); color: #bbf7d0; }
  .liveness-stale { border-color: rgba(234, 179, 8, 0.55); color: #fde68a; }
  .liveness-offline { border-color: rgba(239, 68, 68, 0.55); color: #fecaca; }

  .signal {
    margin: 0.8rem 0;
    border-radius: 12px;
    padding: 0.75rem;
    background: rgba(15, 23, 42, 0.72);
  }

  .signal span,
  .signal em { display: block; color: var(--text-muted); font-size: 0.75rem; font-style: normal; text-transform: uppercase; }
  .signal strong { display: block; margin: 0.15rem 0; }

  dl {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.5rem;
    margin: 0;
  }

  dl div {
    border-top: 1px solid var(--border-color);
    padding-top: 0.45rem;
  }

  dt { color: var(--text-muted); font-size: 0.72rem; text-transform: uppercase; }
  dd { margin: 0.15rem 0 0; font-weight: 700; overflow-wrap: anywhere; }

  .telemetry {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
    margin-top: 0.75rem;
  }

  .telemetry span {
    border: 1px solid var(--border-color);
    border-radius: 999px;
    padding: 0.2rem 0.45rem;
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  .missing,
  .empty {
    color: var(--text-muted);
    font-size: 0.85rem;
  }

  .cleanup-active {
    color: #bfdbfe;
    font-weight: 700;
  }

  button {
    margin-top: 0.75rem;
    border: 1px solid var(--primary, #818cf8);
    border-radius: 10px;
    background: transparent;
    color: var(--text-primary);
    padding: 0.55rem 0.75rem;
    cursor: pointer;
    font-weight: 800;
  }

  @media (max-width: 1200px) {
    .weather-map { grid-template-columns: repeat(2, minmax(240px, 1fr)); }
  }

  @media (max-width: 700px) {
    .weather-map { grid-template-columns: 1fr; }
  }
</style>
