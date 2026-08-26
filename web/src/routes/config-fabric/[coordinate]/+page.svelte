<script>
  import { page } from '$app/state';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ConfigPublishForm from '$lib/config-fabric/ConfigPublishForm.svelte';
  import {
    configPayload,
    findConfigCoordinate,
    initialConfigPublishForm,
    shortEventId
  } from '$lib/config-fabric/model.js';
  import api from '$lib/api/client.js';
  import { ConfiguredIcon, RollbackIcon } from '$lib/icons/domain-icons.js';

  let rows = $state([]);
  let record = $state(null);
  let loading = $state(true);
  let error = $state('');
  let publishOpen = $state(false);
  let rollbackTarget = $state(null);
  let rollingBack = $state(false);
  let rollbackError = $state('');

  let desiredJSON = $derived(record?.desired ? JSON.stringify(configPayload(record.desired), null, 2) : '');
  let effectiveJSON = $derived(record?.effective ? JSON.stringify(configPayload(record.effective), null, 2) : '');

  $effect(() => {
    const coordinate = page.params.coordinate;
    if (coordinate) void loadDetail(coordinate);
  });

  async function loadDetail(coordinate = page.params.coordinate) {
    loading = true;
    error = '';
    try {
      rows = await api.listConfigFabricDrift();
      record = findConfigCoordinate(rows, coordinate);
      if (!record) throw new Error('Config Fabric coordinate not found');
    } catch (err) {
      record = null;
      error = err?.message || 'Failed to load Config Fabric policy';
    } finally {
      loading = false;
    }
  }

  async function handlePublished() {
    publishOpen = false;
    await loadDetail();
  }

  async function confirmRollback() {
    if (!rollbackTarget) return;
    rollingBack = true;
    rollbackError = '';
    try {
      await api.rollbackConfigFabricEvent(rollbackTarget.event_id);
      rollbackTarget = null;
      await loadDetail();
    } catch (err) {
      rollbackError = err?.message || 'Rollback failed';
    } finally {
      rollingBack = false;
    }
  }

  function displayTime(value) {
    if (!value) return 'Unknown';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? 'Unknown' : date.toLocaleString();
  }
</script>

<div class="page">
  <a class="back" href="/config-fabric">← Config Fabric</a>

  {#if loading}
    <p class="loading">Loading configuration history...</p>
  {:else if error}
    <section class="error-state" role="alert">
      <p>Error: {error}</p>
      <LoadingButton variant="secondary" onclick={() => loadDetail()}>Retry</LoadingButton>
    </section>
  {:else if record}
    <div class="header">
      <div>
        <h1>{record.service_id} / {record.policy_name}</h1>
        <p><code>{record.scope}</code> · <code>{record.desired?.schema}</code></p>
      </div>
      <LoadingButton variant="primary" onclick={() => { publishOpen = true; }}>Publish New Version</LoadingButton>
    </div>

    <div class="cards">
      <Card title="Desired version" value={`v${record.desired_version}`} subtitle={shortEventId(record.desired_event_id)} />
      <Card title="Effective version" value={record.applied_version ? `v${record.applied_version}` : 'None'} subtitle={shortEventId(record.applied_event_id)} />
      <Card title="Drift" value={record.drift ? 'Drifted' : 'In sync'} status={record.drift ? 'warning' : 'success'} />
    </div>

    {#if record.last_rejection_reason}
      <section class="rejection" aria-label="Last rejection">
        <h2>Last rejection</h2>
        <p>{record.last_rejection_reason}</p>
      </section>
    {/if}

    <div class="config-grid">
      <section class="panel">
        <h2>Current desired config</h2>
        <p>Version {record.desired_version} · event <code>{record.desired_event_id}</code></p>
        <pre>{desiredJSON}</pre>
      </section>
      <section class="panel">
        <h2>Current effective config</h2>
        {#if record.effective}
          <p>Version {record.applied_version} · event <code>{record.applied_event_id}</code></p>
          <pre>{effectiveJSON}</pre>
        {:else}
          <p class="muted">No retained desired event matches the latest applied status.</p>
        {/if}
      </section>
    </div>

    <section class="panel">
      <h2>Versions</h2>
      <div class="table-container">
        <table aria-label="Config Fabric versions">
          <thead><tr><th>Version</th><th>Event</th><th>Kind</th><th>Created</th><th>Status</th><th>Action</th></tr></thead>
          <tbody>
            {#each record.versions || [] as version}
              <tr>
                <td>v{version.version}</td>
                <td><code title={version.event_id}>{shortEventId(version.event_id)}</code></td>
                <td>{version.kind}</td>
                <td>{displayTime(version.created_at)}</td>
                <td>
                  {#if version.event_id === record.desired_event_id}
                    <Badge variant="primary">Desired</Badge>
                  {:else if version.event_id === record.applied_event_id}
                    <Badge variant="success">Effective</Badge>
                  {:else}
                    <span class="muted">Historical</span>
                  {/if}
                </td>
                <td>
                  <LoadingButton
                    variant="secondary"
                    onclick={() => { rollbackError = ''; rollbackTarget = version; }}
                    disabled={version.event_id === record.desired_event_id}
                  >
                    Rollback
                  </LoadingButton>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <section class="panel">
      <h2>Status and audit history</h2>
      {#if (record.status_history || []).length === 0}
        <p class="muted">No applied or rejected status events have been received.</p>
      {:else}
        <div class="timeline">
          {#each record.status_history as status}
            <article class:rejected={status.status === 'rejected'}>
              <div>
                <Badge variant={status.status === 'applied' ? 'success' : 'error'}>{status.status}</Badge>
                <strong>v{status.version}</strong>
                <time>{displayTime(status.created_at)}</time>
              </div>
              <p>Desired event <code>{shortEventId(status.config_event_id)}</code></p>
              {#if status.status === 'applied'}
                <p>Effective v{status.effective_version}, event <code>{shortEventId(status.last_applied_event_id)}</code></p>
              {:else}
                <p class="reason">{status.reason}</p>
              {/if}
            </article>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
</div>

<Modal bind:open={publishOpen} title="Publish Config Fabric Version" titleIcon={ConfiguredIcon} size="lg">
  {#if record}
    <ConfigPublishForm
      initial={initialConfigPublishForm(record)}
      driftRows={rows}
      onPublished={handlePublished}
      onCancel={() => { publishOpen = false; }}
    />
  {/if}
</Modal>

<ConfirmDialog
  open={Boolean(rollbackTarget)}
  title="Confirm Config Rollback"
  titleIcon={RollbackIcon}
  message={rollbackTarget ? `Republish version ${rollbackTarget.version} as the next monotonically increasing version for ${record?.service_id} / ${record?.policy_name}?` : ''}
  confirmLabel="Rollback"
  loading={rollingBack}
  onConfirm={confirmRollback}
  onCancel={() => { rollbackTarget = null; rollbackError = ''; }}
  onClose={() => { rollbackTarget = null; rollbackError = ''; }}
>
  {#if rollbackError}<p class="rollback-error" role="alert">{rollbackError}</p>{/if}
</ConfirmDialog>

<style>
  .back { color: var(--primary); display: inline-block; margin-bottom: 1rem; text-decoration: none; }
  .header { align-items: flex-start; display: flex; justify-content: space-between; gap: 1rem; margin-bottom: 1.5rem; }
  .header h1 { margin-bottom: 0.25rem; }
  .header p, .muted { color: var(--text-muted); }
  .cards { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1rem; margin-bottom: 1rem; }
  .config-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
  .panel, .rejection { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 8px; margin-bottom: 1rem; padding: 1.25rem; min-width: 0; }
  .panel h2, .rejection h2 { font-size: 1rem; margin: 0 0 0.75rem; }
  .panel > p { font-size: 0.8rem; overflow-wrap: anywhere; }
  .rejection { border-left: 4px solid var(--error); }
  .rejection p, .reason, .rollback-error { color: var(--error); }
  pre { background: var(--bg); border: 1px solid var(--border-color); border-radius: 4px; font-size: 0.8rem; max-height: 28rem; overflow: auto; padding: 1rem; white-space: pre-wrap; }
  .table-container { overflow-x: auto; }
  table { border-collapse: collapse; width: 100%; }
  th, td { border-bottom: 1px solid var(--border-color); padding: 0.7rem; text-align: left; }
  th { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; }
  .timeline { display: flex; flex-direction: column; gap: 0.75rem; }
  .timeline article { border-left: 4px solid #10b981; background: var(--bg); border-radius: 4px; padding: 0.75rem; }
  .timeline article.rejected { border-left-color: var(--error); }
  .timeline article div { align-items: center; display: flex; gap: 0.75rem; }
  .timeline article p { font-size: 0.8rem; margin: 0.5rem 0 0; }
  time { color: var(--text-muted); font-size: 0.75rem; margin-left: auto; }
  .loading, .error-state { color: var(--text-muted); padding: 2rem; text-align: center; }
  .error-state p { color: var(--error); }
  @media (max-width: 800px) {
    .cards, .config-grid { grid-template-columns: 1fr; }
    .header { flex-direction: column; }
  }
</style>
