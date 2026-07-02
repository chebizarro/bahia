<script lang="ts">
  import type { ContinuityAssessmentDTO, ContinuityServiceStatusDTO } from '$lib/types/continuity';

  let {
    assessments = [],
    statuses = []
  }: {
    assessments?: ContinuityAssessmentDTO[];
    statuses?: ContinuityServiceStatusDTO[];
  } = $props();

  const sortedAssessments = $derived(
    [...assessments].sort((left, right) => String(left.service_key || '').localeCompare(String(right.service_key || '')))
  );
  const statusByService = $derived(new Map(statuses.map((status) => [status.service_key, status])));

  function survivabilityLabel(value: string): string {
    return String(value || 'unknown').replaceAll('_', ' ');
  }

  function survivabilityTone(value: string): string {
    const normalized = String(value || '').toLowerCase();
    if (normalized === 'survivable') return 'survivable';
    if (normalized === 'degraded_only') return 'degraded';
    if (normalized === 'emergency_only') return 'emergency';
    return 'unsatisfied';
  }

  function shortKey(value: string | undefined): string {
    const text = String(value || '').trim();
    if (!text) return 'Not assigned';
    if (text.length <= 18) return text;
    return `${text.slice(0, 10)}…${text.slice(-6)}`;
  }
</script>

<section class="topology" aria-label="Continuity topology">
  {#if sortedAssessments.length === 0}
    <div class="empty-card">
      <h2>No event-derived topology assessments yet</h2>
      <p>Topology is derived locally from Nostr continuity events. Service assessments will appear after continuity profile, policy, standby, heartbeat, or worker-state events are observed.</p>
    </div>
  {:else}
    <div class="topology-grid">
      {#each sortedAssessments as assessment (assessment.service_key)}
        {@const status = statusByService.get(assessment.service_key)}
        {@const tone = survivabilityTone(assessment.survivability)}
        <article class="topology-card">
          <header>
            <div>
              <p class="eyebrow">Service</p>
              <h2>{assessment.service_key}</h2>
            </div>
            <span class={`survivability ${tone}`}>{survivabilityLabel(assessment.survivability)}</span>
          </header>

          <div class="map-row">
            <div class="node service-node">{assessment.service_key}</div>
            <div class="links" aria-hidden="true">
              <span></span>
              <span></span>
              <span></span>
            </div>
            <div class="worker-stack">
              <div class="node worker primary">
                <strong>Primary</strong>
                <code title={status?.primary_worker_pubkey}>{shortKey(status?.primary_worker_pubkey)}</code>
              </div>
              <div class="node worker active">
                <strong>Active</strong>
                <code title={status?.active_worker_pubkey}>{shortKey(status?.active_worker_pubkey)}</code>
              </div>
              <div class="node worker standby">
                <strong>Standby</strong>
                <code title={status?.standby_worker_pubkey}>{shortKey(status?.standby_worker_pubkey)}</code>
              </div>
            </div>
          </div>

          <dl class="signals">
            <div>
              <dt>Failover recipe</dt>
              <dd class:ok={assessment.has_failover_recipe}>{assessment.has_failover_recipe ? '✓ covered' : '✗ missing'}</dd>
            </div>
            <div>
              <dt>Recovery recipe</dt>
              <dd class:ok={assessment.has_recovery_recipe}>{assessment.has_recovery_recipe ? '✓ covered' : '✗ missing'}</dd>
            </div>
            <div>
              <dt>Standbys</dt>
              <dd>{assessment.standby_count}</dd>
            </div>
            <div>
              <dt>Replication</dt>
              <dd class:ok={assessment.replication_configured}>{assessment.replication_configured ? 'configured' : 'not configured'}</dd>
            </div>
            <div>
              <dt>Heartbeat</dt>
              <dd class:ok={assessment.heartbeat_active}>{assessment.heartbeat_active ? 'active' : 'inactive'}</dd>
            </div>
          </dl>
        </article>
      {/each}
    </div>
  {/if}
</section>

<style>
  .topology-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
    gap: 1rem;
  }

  .empty-card,
  .topology-card {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 16px;
  }

  .empty-card {
    padding: 2rem;
  }

  .empty-card p,
  .eyebrow,
  dt {
    color: var(--text-muted);
  }

  .topology-card {
    padding: 1.25rem;
    display: grid;
    gap: 1rem;
  }

  header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
  }

  h2 {
    font-size: 1.2rem;
    word-break: break-word;
  }

  .eyebrow {
    font-size: 0.72rem;
    font-weight: 800;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .survivability {
    border: 1px solid currentColor;
    border-radius: 999px;
    padding: 0.35rem 0.65rem;
    align-self: flex-start;
    font-size: 0.75rem;
    font-weight: 800;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .survivability.survivable {
    color: var(--success);
    background: color-mix(in srgb, var(--success) 15%, transparent);
  }

  .survivability.degraded {
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 15%, transparent);
  }

  .survivability.emergency {
    color: orange;
    background: color-mix(in srgb, orange 15%, transparent);
  }

  .survivability.unsatisfied {
    color: var(--error);
    background: color-mix(in srgb, var(--error) 15%, transparent);
  }

  .map-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 52px minmax(0, 1.3fr);
    gap: 0.5rem;
    align-items: center;
  }

  .node {
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 0.75rem;
    background: color-mix(in srgb, var(--hover-bg) 45%, transparent);
    word-break: break-word;
  }

  .service-node {
    border-color: color-mix(in srgb, var(--primary) 45%, var(--border-color));
    font-weight: 800;
  }

  .links {
    display: grid;
    gap: 1.8rem;
  }

  .links span {
    height: 1px;
    background: var(--border-color);
    position: relative;
  }

  .links span::after {
    content: '›';
    position: absolute;
    right: -0.25rem;
    top: -0.75rem;
    color: var(--text-muted);
  }

  .worker-stack {
    display: grid;
    gap: 0.5rem;
  }

  .worker {
    display: grid;
    gap: 0.2rem;
  }

  .worker.primary {
    border-left: 3px solid var(--primary);
  }

  .worker.active {
    border-left: 3px solid var(--success);
  }

  .worker.standby {
    border-left: 3px solid var(--warning);
  }

  code {
    color: var(--text-primary);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.86rem;
  }

  .signals {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(135px, 1fr));
    gap: 0.75rem;
    margin: 0;
  }

  .signals div {
    border-top: 1px solid var(--border-color);
    padding-top: 0.7rem;
  }

  dt {
    font-size: 0.8rem;
  }

  dd {
    margin: 0.2rem 0 0;
    color: var(--error);
    font-weight: 700;
  }

  dd.ok {
    color: var(--success);
  }

  @media (max-width: 720px) {
    header,
    .map-row {
      display: flex;
      flex-direction: column;
      align-items: stretch;
    }

    .links {
      display: none;
    }
  }
</style>
