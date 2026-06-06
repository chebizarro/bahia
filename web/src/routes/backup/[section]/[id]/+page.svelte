<script>
  import { page } from '$app/state';
  import BackupShell from '../../BackupShell.svelte';
  import StatusPill from '../../StatusPill.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
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
  import { approveBackupRestore, probeBackupRepository, rejectBackupRestore, resultContent } from '$lib/stores/public-controlplane.svelte.js';
  import {
    backupContext,
    capabilityEntries,
    cellText,
    detailFields,
    evidenceSummary,
    findSectionRecord,
    formatTimestamp,
    repositoryHealth,
    sectionConfig,
    terminalReason,
    titleize
  } from '$lib/backup/model.js';

  let loading = $state(true);
  let error = $state(null);
  let notice = $state(null);
  let pending = $state('');

  const section = $derived(page.params.section || '');
  const id = $derived(page.params.id || '');
  const config = $derived(sectionConfig(section));
  const stores = $derived({ backupRepositories, backupPolicies, backupRecipes, backupDefinitions, backupRuns, backupVerifications, backupRestores, backupRetentionRuns, backupRuntimeObservations });
  const context = $derived(backupContext(stores));
  const record = $derived(config ? findSectionRecord(section, id, stores) : null);
  const fields = $derived(config ? detailFields(section) : []);
  const health = $derived(section === 'repositories' && record ? repositoryHealth(record, backupRuntimeObservations) : null);
  const capabilities = $derived(record ? capabilityEntries(record) : []);

  $effect(() => {
    const current = `${section}:${id}`;
    if (current) void loadBackup();
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

  async function runProbe() {
    if (!record) return;
    pending = 'probe';
    notice = null;
    try {
      const result = await probeBackupRepository(record);
      const content = resultContent(result);
      notice = { type: 'success', message: content.message || 'Repository probe queued' };
    } catch (err) {
      notice = { type: 'error', message: err?.message || 'Failed to queue repository probe' };
    } finally {
      pending = '';
    }
  }

  async function decideRestore(approved) {
    if (!record) return;
    const message = globalThis.prompt?.(approved ? 'Approval message for this restore' : 'Rejection reason for this restore', '') ?? null;
    if (message === null) return;
    pending = approved ? 'approve' : 'reject';
    notice = null;
    try {
      const result = await (approved ? approveBackupRestore(record, message) : rejectBackupRestore(record, message));
      const content = resultContent(result);
      notice = { type: 'success', message: content.message || 'Restore decision accepted' };
    } catch (err) {
      notice = { type: 'error', message: err?.message || 'Failed to submit restore decision' };
    } finally {
      pending = '';
    }
  }

  function jsonBlock(value) {
    if (!value || (typeof value === 'object' && Object.keys(value).length === 0)) return '';
    return JSON.stringify(value, null, 2);
  }

  function hasApprovalActions(value) {
    return section === 'restores' && value && (value.pending_approval || value.approval_status === 'pending');
  }
</script>

<BackupShell title={record ? `${config?.singular}: ${record.name || record.id || record.backup_run_id}` : config?.singular || 'Backup detail'} subtitle={config?.description || ''}>
  {#if !config}
    <EmptyState iconComponent={WarningIcon} title="Unknown backup section" message={`No backup detail page is registered for ${section}.`} />
  {:else if loading}
    <p class="loading">Loading backup detail...</p>
  {:else if error}
    <EmptyState iconComponent={WarningIcon} title="Unable to load backup detail" message={error} />
  {:else if !record}
    <EmptyState iconComponent={RepositoryIcon} title={`${config.singular} not found`} message="The read model may not have been projected yet or may have been deleted." />
  {:else}
    <div class="actions">
      <a href={`/backup/${section}`}>Back to {config.label}</a>
      {#if section === 'repositories'}
        <button type="button" disabled={pending === 'probe'} onclick={runProbe}>{pending === 'probe' ? 'Queueing probe…' : 'Probe repository'}</button>
      {/if}
      {#if hasApprovalActions(record)}
        <button type="button" class="approve" disabled={Boolean(pending)} onclick={() => decideRestore(true)}>{pending === 'approve' ? 'Approving…' : 'Approve restore'}</button>
        <button type="button" class="reject" disabled={Boolean(pending)} onclick={() => decideRestore(false)}>{pending === 'reject' ? 'Rejecting…' : 'Reject restore'}</button>
      {/if}
    </div>

    {#if notice}
      <div class={`notice ${notice.type}`} role="status">{notice.message}</div>
    {/if}

    <section class="summary">
      <article>
        <span>Status</span>
        <StatusPill value={record.status || record.approval_status || record.restore_eligibility || health?.status || 'unknown'} />
      </article>
      {#if section === 'repositories'}
        <article><span>Backend</span><strong>{record.backend || '-'}</strong></article>
        <article><span>Health</span><StatusPill value={health.status} /></article>
      {:else if section === 'runs'}
        <article><span>Restore eligibility</span><StatusPill value={record.restore_eligible ? 'eligible' : record.restore_eligibility} label={record.restore_eligible ? 'Eligible' : titleize(record.restore_eligibility)} /></article>
        <article><span>Verification</span><StatusPill value={record.verification_status} /></article>
      {:else if section === 'verifications'}
        <article><span>Verified</span><StatusPill value={record.verified ? 'verified' : record.status} /></article>
        <article><span>Mode</span><strong>{titleize(record.mode)}</strong></article>
      {:else if section === 'definitions'}
        <article><span>Schedule</span><StatusPill value={record.schedule_enabled ? 'enabled' : 'disabled'} label={record.schedule_enabled ? 'Enabled' : 'Disabled'} /></article>
        <article><span>Requires approval</span><strong>{record.requires_approval ? 'Yes' : 'No'}</strong></article>
      {:else if section === 'restores'}
        <article><span>Approval</span><StatusPill value={record.approval_status} /></article>
        <article><span>Verification</span><StatusPill value={record.verification_status} /></article>
      {/if}
    </section>

    <section class="grid">
      <article class="panel">
        <h2>Operator fields</h2>
        <dl>
          {#each fields as [key, label]}
            <div><dt>{label}</dt><dd>{cellText(section, key, record, context)}</dd></div>
          {/each}
          <div><dt>Terminal success/failure reason</dt><dd>{terminalReason(record)}</dd></div>
          {#if record.created_at}<div><dt>Created</dt><dd>{formatTimestamp(record.created_at)}</dd></div>{/if}
          {#if record.updated_at}<div><dt>Updated</dt><dd>{formatTimestamp(record.updated_at)}</dd></div>{/if}
          {#if record.started_at}<div><dt>Started</dt><dd>{formatTimestamp(record.started_at)}</dd></div>{/if}
          {#if record.finished_at}<div><dt>Finished</dt><dd>{formatTimestamp(record.finished_at)}</dd></div>{/if}
        </dl>
      </article>

      <article class="panel">
        <h2>Composition and provenance</h2>
        <dl>
          {#if record.repository_id}<div><dt>Repository</dt><dd><a href={`/backup/repositories/${encodeURIComponent(record.repository_id)}`}>{record.repository_name || record.repository_id}</a></dd></div>{/if}
          {#if record.policy_id}<div><dt>Policy</dt><dd><a href={`/backup/policies/${encodeURIComponent(record.policy_id)}`}>{record.policy_name || record.policy_id}</a></dd></div>{/if}
          {#if record.recipe_id}<div><dt>Recipe</dt><dd><a href={`/backup/recipes/${encodeURIComponent(record.recipe_id)}`}>{record.recipe_name || record.recipe_id}</a></dd></div>{/if}
          {#if record.backup_run_id}<div><dt>Backup run</dt><dd><a href={`/backup/runs/${encodeURIComponent(record.backup_run_id)}`}>{record.backup_run_id}</a></dd></div>{/if}
          {#if record.requested_by}<div><dt>Requested by</dt><dd><code>{record.requested_by}</code></dd></div>{/if}
          {#if record.request_event_id}<div><dt>Request event</dt><dd><code>{record.request_event_id}</code></dd></div>{/if}
          {#if record.nostr_event_id}<div><dt>Read-model event</dt><dd><code>{record.nostr_event_id}</code></dd></div>{/if}
        </dl>
      </article>
    </section>

    {#if section === 'repositories'}
      <section class="panel">
        <h2>Advertised backend capabilities</h2>
        {#if capabilities.length > 0}
          <div class="chips">
            {#each capabilities as capability}
              <StatusPill value={capability.enabled ? 'success' : 'unsupported'} label={`${capability.key}: ${capability.enabled ? 'yes' : 'no'}`} />
            {/each}
          </div>
        {:else}
          <EmptyState
            iconComponent={RepositoryIcon}
            title="No backend capabilities advertised"
            message="This repository record does not advertise backend capability metadata yet."
          />
        {/if}
      </section>
    {/if}

    <section class="grid">
      <article class="panel">
        <h2>Evidence</h2>
        <p>{evidenceSummary(record)}</p>
        {#if jsonBlock(record.evidence || record.evidence_details || record.verification?.evidence)}
          <pre>{jsonBlock(record.evidence || record.evidence_details || record.verification?.evidence)}</pre>
        {/if}
      </article>
      <article class="panel">
        <h2>Metadata</h2>
        {#if jsonBlock(record.metadata)}
          <pre>{jsonBlock(record.metadata)}</pre>
        {:else}
          <p class="muted">No metadata projected.</p>
        {/if}
      </article>
    </section>

    {#if jsonBlock(record.publish_summary)}
      <section class="panel">
        <h2>Publish summary</h2>
        <pre>{jsonBlock(record.publish_summary)}</pre>
      </section>
    {/if}
  {/if}
</BackupShell>

<style>
  .loading, .muted { color: var(--text-muted); padding: 1rem 0; }
  .actions { display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem; }
  .actions a, button { border: 1px solid var(--border-color); border-radius: 0.45rem; background: var(--card-bg); color: var(--text-primary); cursor: pointer; padding: 0.45rem 0.7rem; text-decoration: none; }
  .actions a:hover, button:hover:not(:disabled) { border-color: var(--primary); }
  button:disabled { cursor: not-allowed; opacity: 0.55; }
  button.approve { border-color: rgba(16, 185, 129, 0.55); color: #86efac; }
  button.reject { border-color: rgba(239, 68, 68, 0.55); color: #fca5a5; }
  .notice { border: 1px solid var(--border-color); border-radius: 0.55rem; padding: 0.75rem 1rem; }
  .notice.success { border-color: rgba(16, 185, 129, 0.5); color: #86efac; background: rgba(16, 185, 129, 0.08); }
  .notice.error { border-color: rgba(239, 68, 68, 0.5); color: #fca5a5; background: rgba(239, 68, 68, 0.08); }
  .summary { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); }
  .summary article, .panel { border: 1px solid var(--border-color); border-radius: 0.75rem; background: var(--card-bg); padding: 1rem; }
  .summary article { display: flex; flex-direction: column; gap: 0.35rem; }
  .summary span, dt { color: var(--text-muted); font-size: 0.8rem; }
  .grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }
  .panel { min-width: 0; }
  h2 { font-size: 1rem; margin-bottom: 0.75rem; }
  dl { display: grid; gap: 0.65rem; }
  dl div { border-bottom: 1px solid var(--border-color); padding-bottom: 0.55rem; }
  dd { word-break: break-word; }
  dd a { color: var(--primary); text-decoration: none; }
  dd a:hover { text-decoration: underline; }
  code { border: 1px solid var(--border-color); border-radius: 0.3rem; padding: 0.1rem 0.3rem; }
  .chips { display: flex; flex-wrap: wrap; gap: 0.4rem; }
  .panel :global(.empty-state) { padding: 1.5rem 0; }
  pre { background: #080812; border: 1px solid var(--border-color); border-radius: 0.55rem; color: var(--text-primary); margin-top: 0.75rem; max-height: 24rem; overflow: auto; padding: 0.75rem; white-space: pre-wrap; }
</style>
