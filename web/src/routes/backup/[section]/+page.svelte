<script>
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import BackupMutationPanel from '../BackupMutationPanel.svelte';
  import BackupShell from '../BackupShell.svelte';
  import StatusPill from '../StatusPill.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { toast } from '$lib/components/toast.js';
  import { RepositoryIcon, WarningIcon } from '$lib/icons/domain-icons.js';
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
    approveBackupRestore,
    probeBackupRepository,
    rejectBackupRestore,
    requestBackupRetention,
    requestBackupRestore,
    requestBackupRun,
    requestBackupVerification,
    resultContent
  } from '$lib/stores/public-controlplane.svelte.js';
  import {
    backupContext,
    cellText,
    filterByStatus,
    listColumns,
    repositoryHealth,
    rowsForSection,
    searchRows,
    sectionConfig,
    statusTone,
    titleize,
    uniqueStatuses
  } from '$lib/backup/model.js';

  let loading = $state(true);
  let error = $state(null);
  let query = $state('');
  let statusFilter = $state('all');
  let notice = $state(null);
  let pendingActions = $state({});

  const section = $derived(page.params.section || '');
  const config = $derived(sectionConfig(section));
  const stores = $derived({ backupRepositories, backupPolicies, backupRecipes, backupDefinitions, backupRuns, backupVerifications, backupRestores, backupRetentionRuns, backupRuntimeObservations });
  const context = $derived(backupContext(stores));
  const rows = $derived(config ? rowsForSection(section, stores) : []);
  const columns = $derived(config ? listColumns(section) : []);
  const statuses = $derived(uniqueStatuses(rows));
  const filteredRows = $derived(filterByStatus(searchRows(rows, query), statusFilter));

  $effect(() => {
    const currentSection = section;
    statusFilter = 'all';
    query = '';
    if (currentSection) void loadBackup();
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

  function rowHref(row) {
    return `/backup/${section}/${encodeURIComponent(row.id || row.backup_run_id)}`;
  }

  function actionKey(row, action) {
    return `${row.id}:${action}`;
  }

  function setPending(row, action, value) {
    const key = actionKey(row, action);
    if (value) {
      pendingActions = { ...pendingActions, [key]: true };
      return;
    }
    const { [key]: removed, ...rest } = pendingActions;
    pendingActions = rest;
  }

  function isPending(row, action) {
    return Boolean(pendingActions[actionKey(row, action)]);
  }

  function isStatusCell(key) {
    return ['status', 'approval_status', 'verification_status', 'restore_eligible', 'health', 'schedule_state'].includes(key);
  }

  function statusCellValue(key, row) {
    if (key === 'health') return repositoryHealth(row, context.runtimeObservations).status;
    if (key === 'restore_eligible') return row.restore_eligible ? 'eligible' : row.restore_eligibility || 'not_eligible';
    if (key === 'schedule_state') return row.schedule_enabled ? 'enabled' : 'disabled';
    return row[key];
  }

  function shouldShowActions(row) {
    return ['repositories', 'recipes', 'definitions', 'runs'].includes(section) || (section === 'restores' && (row.pending_approval || row.approval_status === 'pending'));
  }

  async function runProbe(row, event) {
    event.stopPropagation();
    setPending(row, 'probe', true);
    notice = null;
    try {
      const result = await probeBackupRepository(row);
      const content = resultContent(result);
      notice = { type: 'success', message: content.message || `Repository probe queued for ${row.name || row.id}` };
      toast.success(notice.message);
    } catch (err) {
      notice = { type: 'error', message: err?.message || 'Failed to queue repository probe' };
      toast.error(notice.message);
    } finally {
      setPending(row, 'probe', false);
    }
  }

  async function decideRestore(row, approved, event) {
    event.stopPropagation();
    const action = approved ? 'approve' : 'reject';
    const message = globalThis.prompt?.(approved ? 'Approval message for this restore' : 'Rejection reason for this restore', '') ?? null;
    if (message === null) return;
    setPending(row, action, true);
    notice = null;
    try {
      const result = await (approved ? approveBackupRestore(row, message) : rejectBackupRestore(row, message));
      const content = resultContent(result);
      notice = { type: 'success', message: content.message || `Restore ${action} command accepted` };
      toast.success(notice.message);
    } catch (err) {
      notice = { type: 'error', message: err?.message || `Failed to ${action} restore` };
      toast.error(notice.message);
    } finally {
      setPending(row, action, false);
    }
  }

  async function publishBackupRowAction(row, action, event, publisher, fallbackMessage) {
    event.stopPropagation();
    setPending(row, action, true);
    notice = null;
    try {
      const result = await publisher();
      const content = resultContent(result);
      notice = { type: 'success', message: content.message || fallbackMessage };
      toast.success(notice.message);
    } catch (err) {
      notice = { type: 'error', message: err?.message || `Failed to publish ${action} command` };
      toast.error(notice.message);
    } finally {
      setPending(row, action, false);
    }
  }

  function runBackupNow(row, event) {
    return publishBackupRowAction(row, 'run', event, () => requestBackupRun(row), `Backup run requested for ${row.name || row.recipe_name || row.id}`);
  }

  function verifyBackupRun(row, event) {
    return publishBackupRowAction(row, 'verify', event, () => requestBackupVerification(row), `Backup verification requested for ${row.id}`);
  }

  function requestRestore(row, event) {
    const defaultTarget = row.restore_target_ref || row.target_ref || '';
    const target = globalThis.prompt?.('Restore target ref', defaultTarget) ?? null;
    if (target === null) return;
    return publishBackupRowAction(row, 'restore', event, () => requestBackupRestore(row, target), `Backup restore requested for ${row.id}`);
  }

  function enforceRetention(row, event) {
    return publishBackupRowAction(row, 'retention', event, () => requestBackupRetention(row), `Retention enforcement requested for ${row.name || row.id}`);
  }
</script>

<BackupShell title={config ? config.label : 'Backup section'} subtitle={config?.description || 'Unknown backup section'}>
  {#if !config}
    <EmptyState iconComponent={WarningIcon} title="Unknown backup section" message={`No backup page is registered for ${section}.`} />
  {:else if loading}
    <p class="loading">Loading {config.label.toLowerCase()}...</p>
  {:else if error}
    <EmptyState iconComponent={WarningIcon} title={`Unable to load ${config.label.toLowerCase()}`} message={error} />
  {:else}
    <BackupMutationPanel
      {section}
      repositories={backupRepositories}
      policies={backupPolicies}
      recipes={backupRecipes}
      onAccepted={loadBackup}
    />

    <div class="toolbar">
      <label>
        <span>Search</span>
        <input bind:value={query} type="search" aria-label={`Search ${config.label.toLowerCase()}`} />
      </label>
      {#if statuses.length > 0}
        <label>
          <span>Status</span>
          <select bind:value={statusFilter}>
            <option value="all">All states</option>
            {#each statuses as status}
              <option value={status}>{titleize(status)}</option>
            {/each}
          </select>
        </label>
      {/if}
      <span class="count">{filteredRows.length} of {rows.length} {config.label.toLowerCase()}</span>
    </div>

    {#if notice}
      <div class={`notice ${notice.type}`} role="status">{notice.message}</div>
    {/if}

    {#if rows.length === 0}
      <EmptyState iconComponent={RepositoryIcon} title={`No ${config.label.toLowerCase()} projected`} message={`${config.singular} read models will appear here after Bahia publishes them.`} />
    {:else if filteredRows.length === 0}
      <EmptyState iconComponent={WarningIcon} title="No rows match the filters" message="Adjust the search or state filter." />
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              {#each columns as [, label]}
                <th>{label}</th>
              {/each}
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each filteredRows as row}
              <tr onclick={() => goto(rowHref(row))}>
                {#each columns as [key]}
                  <td>
                    {#if isStatusCell(key)}
                      <StatusPill value={statusCellValue(key, row)} label={cellText(section, key, row, context)} />
                    {:else if key === 'id' || key === 'backup_run_id'}
                      <code>{cellText(section, key, row, context)}</code>
                    {:else}
                      {cellText(section, key, row, context)}
                    {/if}
                  </td>
                {/each}
                <td class="actions" onclick={(event) => event.stopPropagation()}>
                  <button type="button" onclick={() => goto(rowHref(row))}>Details</button>
                  {#if shouldShowActions(row)}
                    {#if section === 'repositories'}
                      <button type="button" disabled={isPending(row, 'probe')} onclick={(event) => runProbe(row, event)}>{isPending(row, 'probe') ? 'Queued…' : 'Probe'}</button>
                    {:else if section === 'recipes'}
                      <button type="button" disabled={isPending(row, 'run')} onclick={(event) => runBackupNow(row, event)}>{isPending(row, 'run') ? 'Requesting…' : 'Run now'}</button>
                    {:else if section === 'definitions'}
                      <button type="button" disabled={isPending(row, 'run')} onclick={(event) => runBackupNow(row, event)}>{isPending(row, 'run') ? 'Requesting…' : 'Run now'}</button>
                      <button type="button" disabled={isPending(row, 'retention')} onclick={(event) => enforceRetention(row, event)}>{isPending(row, 'retention') ? 'Requesting…' : 'Enforce retention'}</button>
                    {:else if section === 'runs'}
                      <button type="button" disabled={isPending(row, 'verify')} onclick={(event) => verifyBackupRun(row, event)}>{isPending(row, 'verify') ? 'Requesting…' : 'Verify'}</button>
                      <button type="button" disabled={isPending(row, 'restore')} onclick={(event) => requestRestore(row, event)}>{isPending(row, 'restore') ? 'Requesting…' : 'Request restore'}</button>
                    {:else if section === 'restores'}
                      <button type="button" class="approve" disabled={isPending(row, 'approve') || isPending(row, 'reject')} onclick={(event) => decideRestore(row, true, event)}>{isPending(row, 'approve') ? 'Approving…' : 'Approve'}</button>
                      <button type="button" class="reject" disabled={isPending(row, 'approve') || isPending(row, 'reject')} onclick={(event) => decideRestore(row, false, event)}>{isPending(row, 'reject') ? 'Rejecting…' : 'Reject'}</button>
                    {/if}
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</BackupShell>

<style>
  .loading { color: var(--text-muted); padding: 2rem; text-align: center; }
  .toolbar { display: grid; grid-template-columns: minmax(220px, 1fr) minmax(160px, 240px) auto; align-items: end; gap: 0.75rem; }
  label { display: flex; flex-direction: column; gap: 0.3rem; color: var(--text-muted); font-size: 0.85rem; }
  input, select { border: 1px solid var(--border-color); border-radius: 0.45rem; background: var(--card-bg); color: var(--text-primary); padding: 0.55rem 0.65rem; }
  .count { color: var(--text-muted); font-size: 0.9rem; padding-bottom: 0.55rem; white-space: nowrap; }
  .notice { border: 1px solid var(--border-color); border-radius: 0.55rem; padding: 0.75rem 1rem; }
  .notice.success { border-color: rgba(16, 185, 129, 0.5); color: #86efac; background: rgba(16, 185, 129, 0.08); }
  .notice.error { border-color: rgba(239, 68, 68, 0.5); color: #fca5a5; background: rgba(239, 68, 68, 0.08); }
  .table-wrap { overflow-x: auto; border: 1px solid var(--border-color); border-radius: 0.75rem; }
  table { width: 100%; border-collapse: collapse; background: var(--card-bg); }
  th, td { border-bottom: 1px solid var(--border-color); padding: 0.75rem 0.85rem; text-align: left; vertical-align: top; }
  th { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; }
  tbody tr { cursor: pointer; }
  tbody tr:hover { background: var(--hover-bg); }
  code { border: 1px solid var(--border-color); border-radius: 0.3rem; padding: 0.1rem 0.3rem; }
  .actions { display: flex; flex-wrap: wrap; gap: 0.4rem; min-width: 13rem; }
  button { border: 1px solid var(--border-color); border-radius: 0.4rem; background: transparent; color: var(--text-primary); cursor: pointer; padding: 0.35rem 0.55rem; }
  button:hover:not(:disabled) { border-color: var(--primary); }
  button:disabled { cursor: not-allowed; opacity: 0.55; }
  button.approve { border-color: rgba(16, 185, 129, 0.55); color: #86efac; }
  button.reject { border-color: rgba(239, 68, 68, 0.55); color: #fca5a5; }
  @media (max-width: 760px) { .toolbar { grid-template-columns: 1fr; } .count { padding-bottom: 0; } }
</style>
