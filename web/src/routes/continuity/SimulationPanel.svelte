<script lang="ts">
  import { simulateWorkerFailure } from '$lib/api/continuity';
  import type { ContinuityAssessmentDTO, ContinuityServiceStatusDTO } from '$lib/types/continuity';

  let {
    baseline = [],
    statuses = []
  }: {
    baseline?: ContinuityAssessmentDTO[];
    statuses?: ContinuityServiceStatusDTO[];
  } = $props();

  let workerPubKey = $state('');
  let simulated = $state<ContinuityAssessmentDTO[]>([]);
  let loading = $state(false);
  let error = $state('');
  let simulatedWorker = $state('');

  const workerOptions = $derived(uniqueWorkers(statuses));
  const baselineByService = $derived(new Map(baseline.map((assessment) => [assessment.service_key, assessment])));
  const comparisonRows = $derived(
    simulated.map((after) => ({
      serviceKey: after.service_key,
      before: baselineByService.get(after.service_key),
      after
    }))
  );
  const unsatisfiedRows = $derived(
    comparisonRows.filter((row) => String(row.after.survivability).toLowerCase() === 'unsatisfied')
  );

  function uniqueWorkers(values: ContinuityServiceStatusDTO[]): string[] {
    const keys = new Set<string>();
    for (const status of values) {
      for (const key of [status.primary_worker_pubkey, status.active_worker_pubkey, status.standby_worker_pubkey]) {
        const trimmed = String(key || '').trim();
        if (trimmed) keys.add(trimmed);
      }
    }
    return [...keys].sort((left, right) => left.localeCompare(right));
  }

  function shortKey(value: string | undefined): string {
    const text = String(value || '').trim();
    if (!text) return 'unknown';
    if (text.length <= 18) return text;
    return `${text.slice(0, 10)}…${text.slice(-6)}`;
  }

  function survivabilityLabel(value: string | undefined): string {
    return String(value || 'unknown').replaceAll('_', ' ');
  }

  function survivabilityRank(value: string | undefined): number {
    switch (String(value || '').toLowerCase()) {
      case 'survivable':
        return 3;
      case 'degraded_only':
        return 2;
      case 'emergency_only':
        return 1;
      default:
        return 0;
    }
  }

  function changeLabel(before: ContinuityAssessmentDTO | undefined, after: ContinuityAssessmentDTO): string {
    const beforeRank = survivabilityRank(before?.survivability);
    const afterRank = survivabilityRank(after.survivability);
    if (afterRank < beforeRank) return 'worse';
    if (afterRank > beforeRank) return 'improved';
    return 'unchanged';
  }

  async function runSimulation() {
    const key = workerPubKey.trim();
    if (!key) {
      error = 'Choose or enter a worker pubkey before simulating.';
      return;
    }

    loading = true;
    error = '';
    try {
      simulated = await simulateWorkerFailure(key);
      simulatedWorker = key;
    } catch (caught) {
      simulated = [];
      simulatedWorker = '';
      error = caught instanceof Error ? caught.message : 'Simulation failed';
    } finally {
      loading = false;
    }
  }
</script>

<section class="simulation" aria-label="Failure simulation">
  <div class="panel">
    <div>
      <p class="eyebrow">What-if analysis</p>
      <h2>Simulate worker failure</h2>
      <p class="hint">Select an observed worker or paste a pubkey to compare current survivability with graph output after failure.</p>
    </div>

    <div class="controls">
      <label>
        Worker pubkey
        <input list="continuity-workers" bind:value={workerPubKey} placeholder="npub or hex pubkey" />
      </label>
      <datalist id="continuity-workers">
        {#each workerOptions as worker}
          <option value={worker}>{shortKey(worker)}</option>
        {/each}
      </datalist>
      <button type="button" onclick={runSimulation} disabled={loading}>{loading ? 'Simulating…' : 'Simulate'}</button>
    </div>
  </div>

  {#if error}
    <div class="alert error" role="status">{error}</div>
  {/if}

  {#if simulated.length === 0 && !error}
    <div class="empty-card">
      <h3>No simulation run yet</h3>
      <p>Run a worker failure simulation to see before/after survivability changes.</p>
    </div>
  {:else if simulated.length > 0}
    <div class="results">
      <h3>Failure result for <code>{shortKey(simulatedWorker)}</code></h3>

      {#if unsatisfiedRows.length > 0}
        <div class="alert warning" role="status">
          <strong>{unsatisfiedRows.length} service{unsatisfiedRows.length === 1 ? '' : 's'} become unsatisfied.</strong>
          <span>{unsatisfiedRows.map((row) => row.serviceKey).join(', ')}</span>
        </div>
      {/if}

      <div class="comparison-grid">
        {#each comparisonRows as row (row.serviceKey)}
          {@const change = changeLabel(row.before, row.after)}
          <article class={`comparison-card ${change}`}>
            <header>
              <h4>{row.serviceKey}</h4>
              <span>{change}</span>
            </header>
            <div class="before-after">
              <div>
                <span>Before</span>
                <strong>{survivabilityLabel(row.before?.survivability)}</strong>
              </div>
              <div>
                <span>After</span>
                <strong>{survivabilityLabel(row.after.survivability)}</strong>
              </div>
            </div>
          </article>
        {/each}
      </div>
    </div>
  {/if}
</section>

<style>
  .simulation {
    display: grid;
    gap: 1rem;
  }

  .panel,
  .empty-card,
  .results,
  .alert,
  .comparison-card {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 16px;
  }

  .panel {
    padding: 1.25rem;
    display: grid;
    gap: 1rem;
  }

  .eyebrow,
  .hint,
  label,
  .empty-card p,
  .before-after span {
    color: var(--text-muted);
  }

  .eyebrow {
    font-size: 0.72rem;
    font-weight: 800;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .hint {
    margin-top: 0.35rem;
  }

  .controls {
    display: grid;
    grid-template-columns: minmax(220px, 1fr) auto;
    gap: 0.75rem;
    align-items: end;
  }

  label {
    display: grid;
    gap: 0.35rem;
    font-weight: 700;
  }

  input {
    width: 100%;
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 0.7rem 0.8rem;
    background: var(--bg-primary);
    color: var(--text-primary);
  }

  button {
    border: 0;
    border-radius: 10px;
    padding: 0.75rem 1rem;
    background: var(--primary);
    color: white;
    font-weight: 800;
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.65;
    cursor: not-allowed;
  }

  .empty-card,
  .results,
  .alert {
    padding: 1rem;
  }

  .alert {
    display: grid;
    gap: 0.25rem;
  }

  .alert.error {
    color: var(--error);
    border-color: color-mix(in srgb, var(--error) 55%, var(--border-color));
  }

  .alert.warning {
    color: var(--warning);
    border-color: color-mix(in srgb, var(--warning) 55%, var(--border-color));
  }

  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  }

  .comparison-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 0.75rem;
    margin-top: 1rem;
  }

  .comparison-card {
    padding: 1rem;
    display: grid;
    gap: 0.75rem;
  }

  .comparison-card.worse {
    border-color: color-mix(in srgb, var(--error) 55%, var(--border-color));
  }

  .comparison-card.improved {
    border-color: color-mix(in srgb, var(--success) 55%, var(--border-color));
  }

  .comparison-card header,
  .before-after {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
  }

  .comparison-card header span {
    font-weight: 800;
    text-transform: uppercase;
  }

  .before-after div {
    display: grid;
    gap: 0.2rem;
  }

  @media (max-width: 720px) {
    .controls,
    .comparison-card header,
    .before-after {
      display: flex;
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
