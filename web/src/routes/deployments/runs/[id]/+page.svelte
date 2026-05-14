<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { deploymentRuns, loadDeploymentRuns } from '$lib/stores';
  import { loadDeploymentRunLogs } from '$lib/stores/deployment-run-logs.svelte.js';
  import { DeploymentIcon, UnknownIcon, WarningIcon } from '$lib/icons/domain-icons.js';

  let run = $state(null);
  let stdoutLogs = $state('');
  let stderrLogs = $state('');
  let loading = $state(true);
  let logsLoading = $state(true);
  let error = $state(null);
  let logsError = $state(null);
  let activeTab = $state('stdout');
  let runId = $derived(page.params.id);

  let isCompleted = $derived(Boolean(run && ['succeeded', 'failed', 'cancelled', 'timeout'].includes(String(run.status || '').toLowerCase())));
  let progressPercent = $derived(calculateProgress(run?.status));
  let durationLabel = $derived(formatDuration(run));

  $effect(() => {
    const id = runId;
    if (!id) return;
    void loadRun(id);
  });

  async function loadRun(id = runId) {
    loading = true;
    logsLoading = true;
    error = null;
    logsError = null;

    try {
      await loadDeploymentRuns();
      run = deploymentRuns.find((candidate) => candidate.id === id) || null;
      if (!run) {
        throw new Error('Deployment run not found');
      }
      await loadLogs(id);
    } catch (err) {
      error = err.message || 'Failed to load deployment run';
    } finally {
      loading = false;
      logsLoading = false;
    }
  }

  async function loadLogs(id = runId) {
    logsError = null;

    if (!run || !isCompleted) {
      stdoutLogs = '';
      stderrLogs = '';
      return;
    }

    logsLoading = true;
    stdoutLogs = '';
    stderrLogs = '';
    try {
      const logs = await loadDeploymentRunLogs(id, { tail: 100, stream: 'merged' });
      stdoutLogs = logs?.stdout || '';
      stderrLogs = logs?.stderr || '';
    } catch (err) {
      logsError = err.message || 'Failed to load stored run logs';
    } finally {
      logsLoading = false;
    }
  }

  function calculateProgress(status) {
    switch (String(status || '').toLowerCase()) {
      case 'queued':
        return 5;
      case 'running':
        return 50;
      case 'succeeded':
      case 'failed':
      case 'cancelled':
      case 'timeout':
        return 100;
      default:
        return 0;
    }
  }

  function formatDate(value) {
    if (!value) return '-';
    return new Date(value).toLocaleString();
  }

  function formatDuration(runValue) {
    if (!runValue?.started_at) return '-';
    const start = new Date(runValue.started_at).getTime();
    const end = runValue?.finished_at ? new Date(runValue.finished_at).getTime() : Date.now();
    const ms = Math.max(0, end - start);
    const seconds = Math.floor(ms / 1000);
    const mins = Math.floor(seconds / 60);
    const secs = seconds % 60;
    return mins > 0 ? `${mins}m ${secs}s` : `${secs}s`;
  }

  function statusColor(status) {
    const normalized = String(status || '').toLowerCase();
    const colors = {
      queued: '#888',
      running: '#3b82f6',
      succeeded: '#10b981',
      failed: '#ef4444',
      cancelled: '#6b7280',
      timeout: '#f59e0b'
    };
    return colors[normalized] || '#888';
  }
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading deployment run...</p>
  {:else if error}
    <div class="error-state">
      <p class="error inline-icon"><WarningIcon size={18} stroke={1.75} aria-hidden="true" /> <span>{error}</span></p>
      <LoadingButton variant="secondary" onclick={() => goto('/deployments')}>
        Back to Deployments
      </LoadingButton>
    </div>
  {:else if run}
    <div class="header">
      <div>
        <a href="/deployments" class="back-link">← Back to Deployments</a>
        <h1>Deployment Run</h1>
        <p class="run-id"><code>{run.id}</code></p>
      </div>
      <LoadingButton variant="secondary" onclick={() => loadRun()}>
        Refresh
      </LoadingButton>
    </div>

    <div class="cards-grid">
      <Card>
        <div class="detail-section">
          <h3>Status</h3>
          <p class="detail-value" style="color: {statusColor(run.status)}">{run.status || 'Unknown'}</p>
        </div>
      </Card>
      <Card>
        <div class="detail-section">
          <h3>Progress</h3>
          <p class="detail-value">{progressPercent}%</p>
        </div>
      </Card>
      <Card>
        <div class="detail-section">
          <h3>Duration</h3>
          <p class="detail-value">{durationLabel}</p>
        </div>
      </Card>
      <Card>
        <div class="detail-section">
          <h3>Exit Code</h3>
          <p class="detail-value">{run.exit_code ?? '-'}</p>
        </div>
      </Card>
    </div>

    <Card>
      <h2>Run Details</h2>
      <div class="details-grid">
        <div class="detail-item"><span class="detail-label">Intent ID</span><span class="detail-value"><code>{run.deployment_intent_id || '-'}</code></span></div>
        <div class="detail-item"><span class="detail-label">Worker Pubkey</span><span class="detail-value">{run.worker_pubkey || '-'}</span></div>
        <div class="detail-item"><span class="detail-label">Started At</span><span class="detail-value">{formatDate(run.started_at)}</span></div>
        <div class="detail-item"><span class="detail-label">Finished At</span><span class="detail-value">{formatDate(run.finished_at)}</span></div>
      </div>
    </Card>

    <Card>
      <h2>Run Logs</h2>
      <p class="transport-note">Transport: public relay run projection + encrypted Nostr request/result fetch for stored stdout/stderr snapshots.</p>

      {#if !isCompleted}
        <EmptyState
          iconComponent={DeploymentIcon}
          title="Run still in progress"
          message="Stored run logs are available after completion. Live streaming currently uses the service/environment SSE endpoint."
        />
      {:else}
        <div class="tabs">
          <button class="tab" class:active={activeTab === 'stdout'} onclick={() => activeTab = 'stdout'}>stdout</button>
          <button class="tab" class:active={activeTab === 'stderr'} onclick={() => activeTab = 'stderr'}>stderr</button>
        </div>

        {#if logsLoading}
          <p class="loading">Loading run logs...</p>
        {:else if logsError}
          <p class="error inline-icon"><WarningIcon size={18} stroke={1.75} aria-hidden="true" /> <span>{logsError}</span></p>
        {:else}
          <pre class="logs">{activeTab === 'stdout' ? (stdoutLogs || '(no stdout logs)') : (stderrLogs || '(no stderr logs)')}</pre>
        {/if}
      {/if}
    </Card>
  {:else}
    <EmptyState
      iconComponent={UnknownIcon}
      title="Run not found"
      message="The requested deployment run does not exist"
    />
  {/if}
</div>

<style>
  .header { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 1.5rem; }
  .back-link { display: inline-block; color: var(--primary); text-decoration: none; font-size: 0.875rem; margin-bottom: 0.5rem; }
  .back-link:hover { text-decoration: underline; }
  .run-id { margin: 0.5rem 0 0; font-size: 0.875rem; color: var(--text-muted); }
  .cards-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 1rem; margin-bottom: 1.5rem; }
  .detail-section h3, .detail-label { font-size: 0.75rem; text-transform: uppercase; color: var(--text-muted); margin: 0 0 0.5rem; font-weight: 600; }
  .detail-value { font-size: 1.125rem; font-weight: 600; color: var(--text-primary); margin: 0; }
  .details-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 1rem; }
  .detail-item { display: flex; flex-direction: column; gap: 0.25rem; }
  .transport-note { margin: 0 0 1rem; color: var(--text-muted); font-size: 0.875rem; }
  .tabs { display: flex; gap: 0.5rem; margin-bottom: 0.75rem; }
  .tab { background: transparent; border: 1px solid var(--border-color); color: var(--text-muted); border-radius: 6px; padding: 0.4rem 0.7rem; cursor: pointer; }
  .tab.active { color: var(--text-primary); border-color: var(--primary); }
  .logs { margin: 0; background: #111827; color: #d1d5db; border: 1px solid var(--border-color); border-radius: 8px; padding: 0.75rem; max-height: 420px; overflow: auto; white-space: pre-wrap; font-size: 0.85rem; }
  .loading { color: var(--text-muted); }
  .error { color: var(--error); }
  .inline-icon { display: inline-flex; align-items: center; justify-content: center; gap: 0.4rem; }
  .error-state { padding: 2rem; text-align: center; }
</style>