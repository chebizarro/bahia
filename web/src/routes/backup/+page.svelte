<script>
  import BackupShell from './BackupShell.svelte';
  import StatusPill from './StatusPill.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { RepositoryIcon, SuccessIcon, WarningIcon } from '$lib/icons/domain-icons.js';
  import {
    backupRepositories,
    backupPolicies,
    backupRecipes,
    backupDefinitions,
    backupRuns,
    backupVerifications,
    backupRestores,
    backupRetentionRuns,
    backupRuntimeObservations,
    loadBackupControlplane
  } from '$lib/stores';
  import {
    BACKUP_SECTIONS,
    failedTerminalCount,
    formatTimestamp,
    pendingRestoreCount,
    repositoryHealth,
    successfulCount,
    terminalReason
  } from '$lib/backup/model.js';

  let loading = $state(true);
  let error = $state(null);

  $effect(() => {
    void loadBackup();
  });

  async function loadBackup() {
    loading = true;
    error = null;
    try {
      await loadBackupControlplane();
    } catch (err) {
      error = err?.message || 'Failed to load backup read models';
    } finally {
      loading = false;
    }
  }

  const unhealthyRepositories = $derived(
    backupRepositories.filter((repository) => ['unhealthy', 'failed', 'error'].includes(repositoryHealth(repository, backupRuntimeObservations).status))
  );
  const pendingApprovals = $derived(pendingRestoreCount(backupRestores));
  const latestObservation = $derived(backupRuntimeObservations[0] || null);
  const recentRuns = $derived(backupRuns.slice(0, 6));
  const cards = $derived([
    { label: 'Repositories', value: backupRepositories.length, detail: `${unhealthyRepositories.length} unhealthy` },
    { label: 'Definitions', value: backupDefinitions.length, detail: `${backupDefinitions.filter((definition) => definition.schedule_enabled).length} scheduled` },
    { label: 'Runs', value: backupRuns.length, detail: `${successfulCount(backupRuns)} succeeded · ${failedTerminalCount(backupRuns)} failed` },
    { label: 'Verifications', value: backupVerifications.length, detail: `${backupVerifications.filter((record) => record.verified).length} verified` },
    { label: 'Restore approvals', value: pendingApprovals, detail: 'waiting for operator decision' },
    { label: 'Retention', value: backupRetentionRuns.length, detail: `${failedTerminalCount(backupRetentionRuns)} failed` }
  ]);
</script>

<BackupShell title="Backup Dashboard" subtitle="Fleet backup posture from Nostr read models: registry, health, recent work, restore eligibility, approvals, and evidence.">
  {#if loading}
    <p class="loading">Loading backup posture...</p>
  {:else if error}
    <EmptyState iconComponent={WarningIcon} title="Backup read models unavailable" message={error} />
  {:else if backupRepositories.length + backupPolicies.length + backupRecipes.length + backupDefinitions.length + backupRuns.length === 0}
    <EmptyState iconComponent={RepositoryIcon} title="No backup read models yet" message="Backup repositories, policies, recipes, definitions, and runs will appear after the control plane projects them." />
  {:else}
    <section class="cards" aria-label="Backup posture summary">
      {#each cards as card}
        <article class="card">
          <span>{card.label}</span>
          <strong>{card.value}</strong>
          <small>{card.detail}</small>
        </article>
      {/each}
    </section>

    <section class="grid">
      <article class="panel">
        <div class="panel-title">
          <h2>Repository health</h2>
          <a href="/backup/repositories">View repositories</a>
        </div>
        {#if backupRepositories.length === 0}
          <p class="muted">No repositories projected.</p>
        {:else}
          <div class="stack">
            {#each backupRepositories.slice(0, 6) as repository}
              {@const health = repositoryHealth(repository, backupRuntimeObservations)}
              <a class="row" href={`/backup/repositories/${encodeURIComponent(repository.id)}`}>
                <div><strong>{repository.name || repository.id}</strong><span>{repository.backend || 'backend not advertised'}</span></div>
                <StatusPill value={health.status} />
              </a>
            {/each}
          </div>
        {/if}
      </article>

      <article class="panel urgent" class:clear={pendingApprovals === 0}>
        <div class="panel-title">
          <h2>Restore approval queue</h2>
          <a href="/backup/restores">Review restores</a>
        </div>
        {#if pendingApprovals === 0}
          <p class="ok"><SuccessIcon size={18} strokeWidth={1.75} ariaHidden="true" /> No restores are waiting for approval.</p>
        {:else}
          <p class="approval-count">{pendingApprovals} restore request{pendingApprovals === 1 ? '' : 's'} waiting for approval</p>
          <div class="stack">
            {#each backupRestores.filter((restore) => restore.pending_approval || restore.approval_status === 'pending').slice(0, 5) as restore}
              <a class="row" href={`/backup/restores/${encodeURIComponent(restore.id)}`}>
                <div><strong>{restore.restore_target_ref || restore.snapshot_id || restore.id}</strong><span>{formatTimestamp(restore.created_at)}</span></div>
                <StatusPill value="pending" label="Pending" />
              </a>
            {/each}
          </div>
        {/if}
      </article>
    </section>

    <section class="grid">
      <article class="panel wide">
        <div class="panel-title">
          <h2>Recent backup runs</h2>
          <a href="/backup/runs">View runs</a>
        </div>
        {#if recentRuns.length === 0}
          <p class="muted">No backup runs projected.</p>
        {:else}
          <div class="table-wrap">
            <table>
              <thead><tr><th>Run</th><th>Status</th><th>Target</th><th>Restore eligibility</th><th>Terminal reason</th><th>Created</th></tr></thead>
              <tbody>
                {#each recentRuns as run}
                  <tr>
                    <td><a href={`/backup/runs/${encodeURIComponent(run.id)}`}>{run.id.slice(0, 8)}…</a></td>
                    <td><StatusPill value={run.status} /></td>
                    <td>{run.target_ref || '-'}</td>
                    <td><StatusPill value={run.restore_eligibility} label={run.restore_eligible ? 'Eligible' : run.restore_eligibility} /></td>
                    <td>{terminalReason(run)}</td>
                    <td>{formatTimestamp(run.created_at)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </article>

      <article class="panel">
        <div class="panel-title"><h2>Runtime observation</h2></div>
        {#if latestObservation}
          <dl class="details">
            <div><dt>Generated</dt><dd>{formatTimestamp(latestObservation.generated_at)}</dd></div>
            <div><dt>Run counts</dt><dd>{JSON.stringify(latestObservation.counts?.runs || {})}</dd></div>
            <div><dt>Restore counts</dt><dd>{JSON.stringify(latestObservation.counts?.restores || {})}</dd></div>
            <div><dt>Retention counts</dt><dd>{JSON.stringify(latestObservation.counts?.retention || {})}</dd></div>
          </dl>
        {:else}
          <p class="muted">No fleet runtime observation projected.</p>
        {/if}
      </article>
    </section>

    <section class="section-links" aria-label="Backup pages">
      {#each BACKUP_SECTIONS as section}
        <a href={`/backup/${section.id}`}><strong>{section.label}</strong><span>{section.description}</span></a>
      {/each}
    </section>
  {/if}
</BackupShell>

<style>
  .loading, .muted { color: var(--text-muted); padding: 1rem 0; }
  .cards { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); }
  .card, .panel, .section-links a { border: 1px solid var(--border-color); border-radius: 0.75rem; background: var(--card-bg); }
  .card { display: flex; flex-direction: column; gap: 0.2rem; padding: 1rem; }
  .card span, .card small, .row span, .section-links span { color: var(--text-muted); }
  .card strong { font-size: 2rem; line-height: 1; }
  .grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }
  .panel { padding: 1rem; min-width: 0; }
  .panel.wide { grid-column: span 2; }
  .panel-title { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-bottom: 0.75rem; }
  .panel-title a, td a { color: var(--primary); text-decoration: none; }
  .panel-title a:hover, td a:hover { text-decoration: underline; }
  .stack { display: flex; flex-direction: column; gap: 0.5rem; }
  .row { display: flex; justify-content: space-between; gap: 1rem; border: 1px solid var(--border-color); border-radius: 0.55rem; color: var(--text-primary); padding: 0.75rem; text-decoration: none; }
  .row div { display: flex; flex-direction: column; min-width: 0; }
  .urgent { border-color: rgba(245, 158, 11, 0.45); }
  .urgent.clear { border-color: rgba(16, 185, 129, 0.35); }
  .ok { display: flex; align-items: center; gap: 0.4rem; color: #86efac; }
  .approval-count { color: #fcd34d; margin-bottom: 0.75rem; }
  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; }
  th, td { border-bottom: 1px solid var(--border-color); padding: 0.7rem 0.75rem; text-align: left; vertical-align: top; }
  th { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; }
  .details { display: grid; gap: 0.6rem; }
  .details div { border-bottom: 1px solid var(--border-color); padding-bottom: 0.55rem; }
  dt { color: var(--text-muted); font-size: 0.78rem; }
  dd { word-break: break-word; }
  .section-links { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fit, minmax(230px, 1fr)); }
  .section-links a { display: flex; flex-direction: column; gap: 0.25rem; color: var(--text-primary); padding: 0.9rem; text-decoration: none; }
  .section-links a:hover { border-color: var(--primary); }
  @media (max-width: 780px) { .panel.wide { grid-column: span 1; } }
</style>
