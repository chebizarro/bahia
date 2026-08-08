<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import OperationalActivity from '../../OperationalActivity.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import {
    artifacts,
    deploymentIntents,
    deploymentRuns,
    environments,
    services,
    states,
    loadArtifacts,
    loadDeploymentIntents,
    loadDeploymentRuns,
    loadEnvironments,
    loadServices,
    loadStates
  } from '$lib/stores';
  import { operations } from '$lib/stores';
  import {
    approveDeploymentIntent,
    rejectDeploymentIntent,
    rollbackDeployment
  } from '$lib/stores/public-controlplane.svelte.js';
  import {
    ArtifactIcon,
    DeploymentIcon,
    EnvironmentIcon,
    ErrorIcon,
    PendingIcon,
    ProgressIcon,
    ServiceIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';
  import { shortenPubkey } from '$lib/nostr/nostr-hex.js';
  import { deploymentCanRollback, deploymentHealthFailed } from '$lib/deployment-observability.js';

  let loading = $state(true);
  let error = $state(null);
  let actionError = $state(null);
  let actionNotice = $state('');
  let approving = $state(false);
  let rejecting = $state(false);
  let rollingBack = $state(false);
  let approveOpen = $state(false);
  let rejectOpen = $state(false);
  let rollbackOpen = $state(false);
  let intentId = $derived(page.params.id);
  let liveDeploymentOperations = $derived(operations.filter((operation) => {
    const refs = operation.entity_refs || {};
    return operation.operation_id === intentId || refs.intent_id === intentId || refs.deployment_id === intentId;
  }));

  let intent = $derived(deploymentIntents.find((item) => item.id === intentId) || null);
  let runs = $derived(deploymentRuns.filter((run) => run.deployment_intent_id === intentId || run.intent_id === intentId));
  let latestRun = $derived(runs[0] || null);
  let service = $derived(intent?.service_id ? services.find((item) => item.id === intent.service_id) || null : null);
  let environment = $derived(intent?.environment_id ? environments.find((item) => item.id === intent.environment_id) || null : null);
  let artifact = $derived(intent?.artifact_id ? artifacts.find((item) => item.id === intent.artifact_id) || null : null);
  let runtimeState = $derived(intent
    ? states.find((item) => item.service_id === intent.service_id && item.environment_id === intent.environment_id) || null
    : null);
  let target = $derived(intent?.deployment_target || intent?.metadata?.deployment_target || {});
  let policy = $derived(intent?.policy || intent?.approval_metadata?.policy || intent?.metadata?.policy || {});
  let isPending = $derived(String(intent?.approval_status || '').toLowerCase() === 'pending');
  let phases = $derived(Array.isArray(latestRun?.phases) ? latestRun.phases : []);
  let failure = $derived(latestRun?.failure || null);
  let rollbackTargetIntent = $derived(intent
    ? deploymentIntents.find((candidate) =>
        candidate.id !== intent.id &&
        candidate.service_id === intent.service_id &&
        candidate.environment_id === intent.environment_id &&
        (candidate.deployment_unit_id || '') === (intent.deployment_unit_id || '') &&
        candidate.artifact_id !== intent.artifact_id &&
        String(candidate.status || candidate.deployment_status || '').toLowerCase() === 'deployed')
      || null
    : null);
  let rollbackArtifact = $derived(rollbackTargetIntent
    ? artifacts.find((item) => item.id === rollbackTargetIntent.artifact_id) || null
    : null);
  let healthFailed = $derived(deploymentHealthFailed(latestRun, runtimeState));
  let canRollback = $derived(deploymentCanRollback(intent, rollbackTargetIntent, latestRun, runtimeState));

  $effect(() => {
    const id = intentId;
    if (!id) return;
    void loadAggregate(id);
  });

  async function loadAggregate(id = intentId) {
    loading = true;
    error = null;
    try {
      await Promise.all([
        loadDeploymentIntents(),
        loadDeploymentRuns(),
        loadServices(),
        loadEnvironments(),
        loadArtifacts(),
        loadStates()
      ]);
      if (!deploymentIntents.some((item) => item.id === id)) {
        throw new Error('Deployment intent not found');
      }
    } catch (err) {
      error = err?.message || 'Failed to load deployment';
    } finally {
      loading = false;
    }
  }

  async function handleDecision(decision) {
    if (!intent) return;
    actionError = null;
    actionNotice = '';
    if (decision === 'approve') approving = true;
    else rejecting = true;
    try {
      if (decision === 'approve') await approveDeploymentIntent(intent.id);
      else await rejectDeploymentIntent(intent.id);
      actionNotice = `Signed ${decision} decision submitted. Waiting for the relay projection to converge.`;
      approveOpen = false;
      rejectOpen = false;
    } catch (err) {
      actionError = err?.message || `Failed to ${decision} deployment`;
    } finally {
      approving = false;
      rejecting = false;
    }
  }

  async function handleRollback() {
    if (!canRollback) return;
    rollingBack = true;
    actionError = null;
    actionNotice = '';
    try {
      await rollbackDeployment({
        service_id: intent.service_id,
        environment_id: intent.environment_id,
        ...(intent.deployment_unit_id ? { deployment_unit_id: intent.deployment_unit_id } : {}),
        target_artifact_id: rollbackTargetIntent.artifact_id,
        supersedes_intent_id: intent.id
      });
      actionNotice = 'Signed rollback intent submitted. Approval and execution progress will appear in deployment history.';
      rollbackOpen = false;
    } catch (err) {
      actionError = err?.message || 'Failed to create rollback intent';
    } finally {
      rollingBack = false;
    }
  }

  function formatDate(value) {
    if (!value) return '-';
    return new Date(value).toLocaleString();
  }

  function statusTone(value) {
    const status = String(value || '').toLowerCase();
    if (['healthy', 'in_sync', 'approved', 'deployed', 'succeeded', 'completed'].includes(status)) return 'success';
    if (['failed', 'unhealthy', 'drifted', 'rejected', 'timeout'].includes(status)) return 'error';
    if (['pending', 'queued', 'starting', 'deploying', 'running'].includes(status)) return 'warning';
    return 'default';
  }

  function phaseStatus(phase) {
    return String(phase?.status || 'pending').toLowerCase();
  }
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading signed deployment history…</p>
  {:else if error}
    <div class="error-state">
      <p class="error inline"><WarningIcon size={18} ariaHidden="true" /> {error}</p>
      <LoadingButton variant="secondary" onclick={() => goto('/deployments')}>Back to Deployments</LoadingButton>
    </div>
  {:else if intent}
    <a href="/deployments" class="back-link">← Deployment history</a>
    <header class="header">
      <div>
        <h1 class="inline"><DeploymentIcon size={28} ariaHidden="true" /> Deployment</h1>
        <p class="mono">{intent.id}</p>
      </div>
      <div class="actions">
        {#if isPending}
          <LoadingButton variant="primary" onclick={() => { actionError = null; approveOpen = true; }}>Approve</LoadingButton>
          <LoadingButton variant="danger" onclick={() => { actionError = null; rejectOpen = true; }}>Reject</LoadingButton>
        {/if}
        {#if canRollback}
          <LoadingButton variant="danger" onclick={() => { actionError = null; rollbackOpen = true; }}>Rollback</LoadingButton>
        {/if}
      </div>
    </header>

    {#if actionNotice}<p class="notice">{actionNotice}</p>{/if}
    {#if actionError}<p class="error">{actionError}</p>{/if}

    <OperationalActivity items={liveDeploymentOperations} title="Live deployment activity" />

    <section class="cards">
      <Card title="Approval" titleIcon={isPending ? PendingIcon : SuccessIcon} status={statusTone(intent.approval_status)} value={intent.approval_status || 'unknown'} />
      <Card title="Run" titleIcon={latestRun ? ProgressIcon : DeploymentIcon} status={statusTone(latestRun?.status || intent.deployment_status)} value={latestRun?.status || intent.deployment_status || intent.status || 'waiting'} />
      <Card title="Health" titleIcon={runtimeState?.health_status === 'healthy' ? SuccessIcon : WarningIcon} status={statusTone(runtimeState?.health_status || latestRun?.health_status)} value={runtimeState?.health_status || latestRun?.health_status || 'not observed'} />
      <Card title="Drift" titleIcon={runtimeState?.drift_status === 'in_sync' ? SuccessIcon : WarningIcon} status={statusTone(runtimeState?.drift_status)} value={runtimeState?.drift_status || 'unknown'} />
    </section>

    <Card title="Reviewed target" titleIcon={ArtifactIcon}>
      <div class="details">
        <div><span>Service</span><strong class="inline"><ServiceIcon size={15} ariaHidden="true" />{service?.name || intent.service_id}</strong></div>
        <div><span>Environment</span><strong class="inline"><EnvironmentIcon size={15} ariaHidden="true" />{environment?.name || intent.environment_id}</strong></div>
        <div><span>Deployment unit</span><code>{target.unit_key || latestRun?.deployment_unit_key || intent.deployment_unit_id || 'default'}</code></div>
        <div><span>Endpoint</span><code>{target.endpoint_ref || latestRun?.endpoint_ref || 'local runtime'}</code></div>
        <div class="wide"><span>Desired-state hash</span><code>{intent.desired_hash || intent.desired_state_hash || '-'}</code></div>
        <div class="wide"><span>Artifact digest</span><code>{intent.artifact_digest || artifact?.image_digest || '-'}</code></div>
        <div><span>Policy decision</span><strong>{policy.decision || (policy.allowed === false ? 'deny' : 'allow')}</strong></div>
        <div><span>Policy findings</span><strong>{policy.blockers || 0} blockers · {policy.warnings || 0} warnings</strong></div>
        <div><span>Requested by</span><strong title={intent.requested_by || ''}>{intent.requested_by ? shortenPubkey(intent.requested_by) : '-'}</strong></div>
        <div><span>Updated</span><strong>{formatDate(intent.updated_at || intent.created_at)}</strong></div>
      </div>
    </Card>

    {#if failure}
      <div class="failure">
        <div>
          <strong class="inline"><ErrorIcon size={18} ariaHidden="true" />{failure.code || 'deployment_failed'}</strong>
          <p>{failure.message || 'Bahia could not complete this deployment.'}</p>
        </div>
        {#if canRollback}
          <LoadingButton variant="danger" onclick={() => { actionError = null; rollbackOpen = true; }}>Rollback to {rollbackArtifact?.image_tag || 'previous healthy artifact'}</LoadingButton>
        {:else if healthFailed}
          <p>No previous healthy artifact exists for this deployment unit.</p>
        {/if}
      </div>
    {/if}

    <section class="section">
      <h2>Execution phases</h2>
      {#if phases.length}
        <ol class="timeline">
          {#each phases as phase, index}
            <li class:failed={phaseStatus(phase) === 'failed'} class:active={phaseStatus(phase) === 'running'}>
              <span class="sequence">{index + 1}</span>
              <div><strong>{phase.step || 'phase'}</strong><small>{phase.status || 'pending'} · {formatDate(phase.started_at)}</small></div>
            </li>
          {/each}
        </ol>
      {:else}
        <EmptyState iconComponent={DeploymentIcon} title="Waiting for execution" message="Approval, queued work, and runtime phases will appear here from relay projections." />
      {/if}
    </section>

    <section class="section">
      <h2>Runs and logs</h2>
      {#if runs.length}
        <div class="run-list">
          {#each runs as run}
            <a class="run-row" href={`/deployments/runs/${run.id}`}>
              <code>{run.id}</code>
              <span>{run.phase || run.status || 'queued'}</span>
              <span>{formatDate(run.updated_at || run.created_at)}</span>
              <strong>View phases & logs →</strong>
            </a>
          {/each}
        </div>
      {:else}
        <p class="muted">A rejected deployment has no run and makes no runtime change.</p>
      {/if}
    </section>

    <section class="section">
      <h2>Runtime observation</h2>
      <div class="details">
        <div><span>Health</span><strong>{runtimeState?.health_status || 'not observed'}</strong></div>
        <div><span>Drift</span><strong>{runtimeState?.drift_status || 'unknown'}</strong></div>
        <div class="wide"><span>Observed digest</span><code>{runtimeState?.observed_image_digest || '-'}</code></div>
        <div class="wide"><span>Observed hash</span><code>{runtimeState?.observed_hash || '-'}</code></div>
        <div><span>Observed</span><strong>{formatDate(runtimeState?.observed_at)}</strong></div>
        <div><span>Last reconciled</span><strong>{formatDate(runtimeState?.last_reconciled_at)}</strong></div>
      </div>
      <p class="relay-note">Relay projections are merged by logical service/environment identity and domain timestamp, so reconnects and duplicate events do not regress this view.</p>
    </section>

    {#if rollbackTargetIntent}
      <section class="section">
        <h2>Rollback target</h2>
        <div class="details">
          <div><span>Prior deployment</span><a href={`/deployments/${rollbackTargetIntent.id}`}><code>{rollbackTargetIntent.id}</code></a></div>
          <div><span>Artifact tag</span><strong>{rollbackArtifact?.image_tag || rollbackTargetIntent.artifact_id}</strong></div>
          <div class="wide"><span>Artifact digest</span><code>{rollbackTargetIntent.artifact_digest || rollbackArtifact?.image_digest || '-'}</code></div>
        </div>
      </section>
    {/if}
  {:else}
    <EmptyState iconComponent={UnknownIcon} title="Deployment not found" message="This link does not match a projected deployment intent." />
  {/if}
</div>

<ConfirmDialog bind:open={approveOpen} title="Approve deployment" titleIcon={SuccessIcon} message="Approve this reviewed desired state? Current policy is re-evaluated before execution." confirmLabel="Approve" loading={approving} onConfirm={() => handleDecision('approve')} onCancel={() => approveOpen = false} onClose={() => approveOpen = false} />
<ConfirmDialog bind:open={rejectOpen} title="Reject deployment" titleIcon={WarningIcon} message="Reject this deployment? Rejection creates no deployment run and makes no runtime change." confirmLabel="Reject" variant="danger" loading={rejecting} onConfirm={() => handleDecision('reject')} onCancel={() => rejectOpen = false} onClose={() => rejectOpen = false} />
<ConfirmDialog bind:open={rollbackOpen} title="Rollback deployment" titleIcon={WarningIcon} message={rollbackTargetIntent ? `Create a fresh policy-checked intent for artifact ${rollbackArtifact?.image_tag || rollbackTargetIntent.artifact_id}?` : 'No rollback target is available.'} confirmLabel="Rollback" variant="danger" loading={rollingBack} onConfirm={handleRollback} onCancel={() => rollbackOpen = false} onClose={() => rollbackOpen = false} />

<style>
  .page { padding: 0; }
  .header { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; margin-bottom: 1.25rem; }
  .header h1 { margin: 0; }
  .actions, .inline { display: inline-flex; align-items: center; gap: .5rem; }
  .actions { flex-wrap: wrap; justify-content: flex-end; }
  .back-link { display: inline-block; color: var(--primary); text-decoration: none; margin-bottom: .65rem; font-size: .875rem; }
  .mono, code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; overflow-wrap: anywhere; }
  .mono { color: var(--text-muted); font-size: .82rem; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 1rem; margin-bottom: 1.25rem; }
  .details { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 1rem; margin-top: .75rem; }
  .details > div { display: flex; flex-direction: column; gap: .25rem; min-width: 0; }
  .details span { color: var(--text-muted); text-transform: uppercase; font-size: .7rem; font-weight: 700; letter-spacing: .04em; }
  .details strong, .details code, .details a { font-size: .875rem; color: var(--text-primary); }
  .wide { grid-column: span 2; }
  .section { margin-top: 1.5rem; }
  .section h2 { font-size: 1.05rem; margin: 0 0 .75rem; }
  .timeline { list-style: none; padding: 0; margin: 0; display: grid; gap: .5rem; }
  .timeline li { display: flex; align-items: center; gap: .75rem; padding: .7rem; border: 1px solid var(--border-color); border-radius: 8px; }
  .timeline li.active { border-color: var(--primary); }
  .timeline li.failed { border-color: var(--error); }
  .timeline li div { display: flex; flex-direction: column; gap: .15rem; }
  .timeline small { color: var(--text-muted); }
  .sequence { display: grid; place-items: center; width: 1.75rem; height: 1.75rem; border-radius: 999px; background: var(--surface-secondary); font-weight: 700; }
  .run-list { display: grid; gap: .5rem; }
  .run-row { display: grid; grid-template-columns: minmax(180px, 1fr) auto auto auto; gap: 1rem; align-items: center; color: var(--text-primary); text-decoration: none; border: 1px solid var(--border-color); border-radius: 8px; padding: .75rem; font-size: .82rem; }
  .run-row:hover { border-color: var(--primary); }
  .failure { margin-top: 1.25rem; display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid var(--error); border-radius: 8px; background: rgba(239,68,68,.08); }
  .failure p { margin: .35rem 0 0; color: var(--text-muted); }
  .notice { padding: .75rem; border-radius: 6px; background: rgba(59,130,246,.1); color: var(--text-primary); }
  .error { color: var(--error); }
  .muted, .relay-note, .loading { color: var(--text-muted); }
  .relay-note { font-size: .8rem; margin: 1rem 0 0; }
  .error-state { text-align: center; padding: 2rem; }
  @media (max-width: 760px) {
    .header, .failure { flex-direction: column; }
    .actions { justify-content: flex-start; }
    .wide { grid-column: span 1; }
    .run-row { grid-template-columns: 1fr; gap: .35rem; }
  }
</style>
