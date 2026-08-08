<script>
  import { onMount } from 'svelte';
  import { subscribeToContinuityDashboard } from '$lib/nostr/continuity';
  import SimulationPanel from './SimulationPanel.svelte';
  import TopologyView from './TopologyView.svelte';

  let activeTab = $state('status');
  let statuses = $state([]);
  let assessments = $state([]);
  let requests = $state([]);
  let continuityEvents = $state([]);
  let ready = $state(false);
  let error = $state(null);

  onMount(() => {
    let disposed = false;
    let unsubscribe = null;

    void subscribeToContinuityDashboard({
      onUpdate: (snapshot) => {
        if (disposed) return;
        statuses = snapshot.statuses;
        assessments = snapshot.assessments;
        requests = snapshot.requests;
        continuityEvents = snapshot.events;
        ready = snapshot.ready;
        error = snapshot.error;
      },
      onError: (caught) => {
        if (!disposed) error = caught?.message || 'Continuity live subscription failed';
      }
    }).then((stop) => {
      if (disposed) stop();
      else unsubscribe = stop;
    }).catch((caught) => {
      if (!disposed) error = caught?.message || 'Failed to subscribe to continuity events';
    });

    return () => {
      disposed = true;
      unsubscribe?.();
    };
  });
  const sortedStatuses = $derived([...statuses].sort((left, right) =>
    String(left.service_key || '').localeCompare(String(right.service_key || ''))
  ));
  const activeCount = $derived(statuses.filter((status) => status.operation_state !== 'steady').length);

  function profilePresentation(status) {
    const operationState = String(status?.operation_state || '').toLowerCase();
    const profile = String(status?.active_profile || '').toLowerCase();
    if (operationState === 'recovery_in_progress') {
      return { label: 'RECOVERING', tone: 'recovering' };
    }
    if (profile === 'emergency' || profile === 'offline' || operationState === 'failed') {
      return { label: 'EMERGENCY', tone: 'emergency' };
    }
    if (profile === 'degraded' || operationState === 'failover_in_progress') {
      return { label: 'DEGRADED', tone: 'degraded' };
    }
    return { label: 'NORMAL', tone: 'normal' };
  }

  function operationLabel(value) {
    return String(value || 'unknown').replaceAll('_', ' ');
  }

  function shortKey(value) {
    const text = String(value || '').trim();
    if (!text) return 'Not assigned';
    if (text.length <= 18) return text;
    return `${text.slice(0, 10)}…${text.slice(-6)}`;
  }

  function workerRole(status) {
    if (!status?.active_worker_pubkey) return 'No active worker projected';
    if (status.active_worker_pubkey === status.primary_worker_pubkey) return 'Primary serving traffic';
    return 'Continuity worker active';
  }

  function changedAtLabel(value) {
    if (!value) return 'No change timestamp';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleString();
  }

  function stepProgress(run) {
    const count = Number(run?.step_count || 0);
    const index = Number(run?.step_index || 0);
    if (count <= 0) return 0;
    return Math.max(0, Math.min(100, Math.round((index / count) * 100)));
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <p class="eyebrow">Continuity fabric</p>
      <h1>Continuity dashboard</h1>
      <p class="subtitle">Current service profiles, failover state, and worker activation from continuity read models.</p>
    </div>
    <div class="summary-card" aria-label="Continuity summary">
      <span class="summary-number">{statuses.length}</span>
      <span class="summary-label">Services tracked</span>
      <span class:attention={activeCount > 0} class="summary-note">{activeCount} active operation{activeCount === 1 ? '' : 's'}</span>
    </div>
  </div>

  {#if error}
    <div class="alert" role="status">
      <strong>Continuity status unavailable.</strong>
      <span>{error}</span>
    </div>
  {/if}

  <nav class="tabs" aria-label="Continuity views">
    <button type="button" class:active={activeTab === 'status'} onclick={() => (activeTab = 'status')}>Status</button>
    <button type="button" class:active={activeTab === 'topology'} onclick={() => (activeTab = 'topology')}>Topology</button>
    <button type="button" class:active={activeTab === 'requests'} onclick={() => (activeTab = 'requests')}>Requests ({requests.length})</button>
    <button type="button" class:active={activeTab === 'simulation'} onclick={() => (activeTab = 'simulation')}>Simulation</button>
  </nav>

  {#if activeTab === 'status'}
    {#if !ready && sortedStatuses.length === 0 && !error}
      <section class="empty-card">
        <h2>Loading continuity history</h2>
        <p>The retained relay subscription is processing stored events and will remain open after EOSE.</p>
      </section>
    {:else if sortedStatuses.length === 0 && !error}
      <section class="empty-card">
        <h2>No continuity status projected yet</h2>
        <p>Services will appear here after the continuity status projector records kind 30351/30353 read-model state.</p>
      </section>
    {:else}
      <section class="status-grid" aria-label="Continuity service statuses">
      {#each sortedStatuses as status (status.service_key)}
        {@const presentation = profilePresentation(status)}
        <article class="service-card">
          <div class="card-topline">
            <div>
              <h2>{status.service_key}</h2>
              <p>{operationLabel(status.operation_state)}</p>
            </div>
            <span class={`status-badge ${presentation.tone}`}>{presentation.label}</span>
          </div>

          <div class="profile-row">
            <span>Active profile</span>
            <strong>{String(status.active_profile || 'unknown').toUpperCase()}</strong>
          </div>

          <div class="workers">
            <div class="worker-card primary">
              <span class="worker-label">Primary worker</span>
              <code title={status.primary_worker_pubkey}>{shortKey(status.primary_worker_pubkey)}</code>
            </div>
            <div class="worker-card active">
              <span class="worker-label">Active worker</span>
              <code title={status.active_worker_pubkey}>{shortKey(status.active_worker_pubkey)}</code>
              <span class="role-note">{workerRole(status)}</span>
            </div>
            {#if status.standby_worker_pubkey}
              <div class="worker-card standby">
                <span class="worker-label">Standby worker</span>
                <code title={status.standby_worker_pubkey}>{shortKey(status.standby_worker_pubkey)}</code>
              </div>
            {/if}
          </div>

          {#if status.current_run}
            <div class="run-panel">
              <div class="run-header">
                <span>In-progress step</span>
                <code>{status.current_run.id}</code>
              </div>
              <div class="step-line">
                <strong>{status.current_run.step_action || 'pending step'}</strong>
                <span>{status.current_run.step_index} / {status.current_run.step_count}</span>
              </div>
              <div class="progress-track" aria-hidden="true">
                <span style={`width: ${stepProgress(status.current_run)}%`}></span>
              </div>
            </div>
          {/if}

          {#if status.reason}
            <div class="reason">
              <span>Reason</span>
              <p>{status.reason}</p>
            </div>
          {/if}

          <footer>
            <span>Changed {changedAtLabel(status.changed_at)}</span>
          </footer>
        </article>
      {/each}
      </section>
    {/if}
  {:else if activeTab === 'topology'}
    <TopologyView {assessments} {statuses} />
  {:else if activeTab === 'requests'}
    {#if requests.length === 0}
      <section class="empty-card">
        <h2>No continuity requests observed</h2>
        <p>Failover and recovery requests (kinds 38430 and 38431) will appear here as relay events arrive.</p>
      </section>
    {:else}
      <section class="status-grid" aria-label="Continuity requests">
        {#each requests as request (request.id)}
          <article class="service-card">
            <div class="card-topline">
              <div>
                <h2>{request.service_key || 'Unscoped service'}</h2>
                <p>{request.request_type} request</p>
              </div>
              <span class="status-badge recovering">{request.request_type.toUpperCase()}</span>
            </div>
            {#if request.worker_pubkey}
              <div class="profile-row"><span>Worker</span><code>{shortKey(request.worker_pubkey)}</code></div>
            {/if}
            {#if request.reason}<div class="reason"><span>Reason</span><p>{request.reason}</p></div>{/if}
            <footer><span>Requested {changedAtLabel(request.created_at)}</span></footer>
          </article>
        {/each}
      </section>
    {/if}
  {:else}
    <SimulationPanel baseline={assessments} {statuses} {continuityEvents} />
  {/if}
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }

  .header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
  }

  .eyebrow {
    color: var(--primary);
    font-size: 0.8rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  h1 {
    margin-top: 0.25rem;
    font-size: 2rem;
  }

  .subtitle {
    color: var(--text-muted);
    margin-top: 0.35rem;
    max-width: 720px;
  }

  .summary-card,
  .empty-card,
  .service-card,
  .alert {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 16px;
  }

  .summary-card {
    min-width: 180px;
    padding: 1rem;
    display: grid;
    gap: 0.15rem;
  }

  .summary-number {
    font-size: 2rem;
    font-weight: 800;
  }

  .summary-label,
  .summary-note,
  .card-topline p,
  footer,
  .worker-label,
  .role-note,
  .profile-row span,
  .run-header,
  .reason span {
    color: var(--text-muted);
  }

  .summary-note.attention {
    color: var(--warning);
    font-weight: 700;
  }

  .alert {
    padding: 1rem;
    display: grid;
    gap: 0.25rem;
    border-color: color-mix(in srgb, var(--error) 55%, var(--border-color));
  }

  .alert strong {
    color: var(--error);
  }

  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    border-bottom: 1px solid var(--border-color);
    padding-bottom: 0.75rem;
  }

  .tabs button {
    border: 1px solid var(--border-color);
    border-radius: 999px;
    padding: 0.55rem 0.9rem;
    background: var(--card-bg);
    color: var(--text-primary);
    cursor: pointer;
    font-weight: 700;
  }

  .tabs button.active {
    border-color: var(--primary);
    background: color-mix(in srgb, var(--primary) 15%, transparent);
    color: var(--primary);
  }

  .empty-card {
    padding: 2rem;
  }

  .empty-card p {
    color: var(--text-muted);
    margin-top: 0.5rem;
  }

  .status-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 1rem;
  }

  .service-card {
    padding: 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .card-topline {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
  }

  .card-topline h2 {
    font-size: 1.2rem;
    word-break: break-word;
  }

  .status-badge {
    border-radius: 999px;
    padding: 0.35rem 0.65rem;
    font-size: 0.75rem;
    font-weight: 800;
    letter-spacing: 0.04em;
    border: 1px solid currentColor;
    white-space: nowrap;
  }

  .status-badge.normal {
    color: var(--success);
    background: color-mix(in srgb, var(--success) 15%, transparent);
  }

  .status-badge.degraded {
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 15%, transparent);
  }

  .status-badge.emergency {
    color: var(--error);
    background: color-mix(in srgb, var(--error) 15%, transparent);
  }

  .status-badge.recovering {
    color: var(--primary);
    background: color-mix(in srgb, var(--primary) 15%, transparent);
  }

  .profile-row {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    border-top: 1px solid var(--border-color);
    padding-top: 1rem;
  }

  .workers {
    display: grid;
    gap: 0.75rem;
  }

  .worker-card {
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 0.75rem;
    display: grid;
    gap: 0.25rem;
    background: color-mix(in srgb, var(--hover-bg) 45%, transparent);
  }

  code {
    color: var(--text-primary);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.9rem;
    word-break: break-all;
  }

  .run-panel {
    border: 1px solid color-mix(in srgb, var(--primary) 45%, var(--border-color));
    border-radius: 12px;
    padding: 0.85rem;
    display: grid;
    gap: 0.65rem;
  }

  .run-header,
  .step-line {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: center;
  }

  .progress-track {
    height: 0.5rem;
    border-radius: 999px;
    background: var(--hover-bg);
    overflow: hidden;
  }

  .progress-track span {
    display: block;
    height: 100%;
    background: var(--primary);
    border-radius: inherit;
  }

  .reason {
    border-left: 3px solid var(--warning);
    padding-left: 0.75rem;
  }

  .reason p {
    margin-top: 0.25rem;
  }

  footer {
    border-top: 1px solid var(--border-color);
    padding-top: 0.75rem;
    font-size: 0.85rem;
  }

  @media (max-width: 720px) {
    .header,
    .card-topline,
    .profile-row,
    .run-header,
    .step-line {
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
