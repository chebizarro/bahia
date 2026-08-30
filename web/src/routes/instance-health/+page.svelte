<script>
  import api from '$lib/api/client.js';
  import {
    buildInstanceHealthSummary,
    formatBytes,
    formatInstanceTimestamp,
    managedInstanceKey,
    memoryPercent,
    statusClass
  } from './page-model.js';

  let rows = $state([]);
  let loading = $state(true);
  let error = $state('');
  let statusFilter = $state('');
  let search = $state('');
  let selected = $state(null);
  let detail = $state(null);
  let events = $state([]);
  let attempts = $state([]);
  let detailLoading = $state(false);
  let detailError = $state('');
  let maintenanceReason = $state('');
  let maintenanceExpiresAt = $state('');
  let mutating = $state(false);
  let mutationMessage = $state('');

  let summary = $derived(buildInstanceHealthSummary(rows));
  let filteredRows = $derived.by(() => {
    const needle = search.trim().toLowerCase();
    return rows.filter((row) => {
      if (statusFilter && row.status !== statusFilter) return false;
      if (!needle) return true;
      return [row.runtime_target_name, row.host, row.service_id, row.environment_id, row.supervisor_type]
        .some((value) => String(value || '').toLowerCase().includes(needle));
    });
  });

  $effect(() => { void loadRows(); });

  async function loadRows() {
    loading = true;
    error = '';
    try {
      rows = await api.listInstanceHealth();
      if (selected) {
        selected = rows.find((row) => managedInstanceKey(row) === managedInstanceKey(selected)) || null;
      }
    } catch (err) {
      error = err?.message || 'Failed to load managed instance health';
    } finally {
      loading = false;
    }
  }

  async function selectInstance(row) {
    selected = row;
    detail = null;
    events = [];
    attempts = [];
    detailLoading = true;
    detailError = '';
    mutationMessage = '';
    const key = managedInstanceKey(row);
    try {
      const [nextDetail, nextEvents, nextAttempts] = await Promise.all([
        api.getInstanceHealth(row),
        api.listInstanceHealthEvents(row),
        api.listInstanceRecoveryAttempts(row)
      ]);
      if (!selected || managedInstanceKey(selected) !== key) return;
      detail = nextDetail;
      events = nextEvents;
      attempts = nextAttempts;
    } catch (err) {
      if (selected && managedInstanceKey(selected) === key) detailError = err?.message || 'Failed to load instance history';
    } finally {
      if (selected && managedInstanceKey(selected) === key) detailLoading = false;
    }
  }

  async function setMaintenance() {
    if (!selected || !maintenanceReason.trim()) return;
    mutating = true;
    mutationMessage = '';
    try {
      await api.setInstanceMaintenance(selected, {
        reason: maintenanceReason.trim(),
        expires_at: maintenanceExpiresAt ? new Date(maintenanceExpiresAt).toISOString() : null
      });
      maintenanceReason = '';
      maintenanceExpiresAt = '';
      mutationMessage = 'Maintenance override enabled.';
      await loadRows();
      if (selected) await selectInstance(selected);
    } catch (err) {
      mutationMessage = err?.message || 'Failed to set maintenance override';
    } finally {
      mutating = false;
    }
  }

  async function clearMaintenance() {
    if (!selected) return;
    mutating = true;
    mutationMessage = '';
    try {
      await api.clearInstanceMaintenance(selected);
      mutationMessage = 'Maintenance override cleared.';
      await loadRows();
      if (selected) await selectInstance(selected);
    } catch (err) {
      mutationMessage = err?.message || 'Failed to clear maintenance override';
    } finally {
      mutating = false;
    }
  }
</script>

<svelte:head><title>Instance Health · Bahia</title></svelte:head>

<div class="page">
  <header class="hero">
    <div>
      <p class="eyebrow">Managed runtime supervision</p>
      <h1>Instance Health</h1>
      <p>Current runtime health, bounded recovery history, and operator maintenance controls.</p>
    </div>
    <button class="secondary" type="button" onclick={loadRows} disabled={loading}>Refresh</button>
  </header>

  <section class="summary-grid" aria-label="Instance health summary">
    <article><span>Total</span><strong>{summary.total}</strong><em>managed targets</em></article>
    <article><span>Operational</span><strong>{summary.operational}</strong><em>healthy or running</em></article>
    <article class="warning"><span>Attention</span><strong>{summary.attention}</strong><em>degraded or unknown</em></article>
    <article class="critical"><span>Recovery needed</span><strong>{summary.recovery}</strong><em>stopped or unhealthy</em></article>
    <article><span>Recent recovery</span><strong>{summary.recentRecovery}</strong><em>attempt recorded</em></article>
    <article><span>Maintenance</span><strong>{summary.maintenance}</strong><em>active override</em></article>
  </section>

  <section class="panel controls">
    <label>Search <input bind:value={search} placeholder="target, host, service, environment" /></label>
    <label>Status
      <select bind:value={statusFilter}>
        <option value="">All</option>
        <option value="healthy">healthy</option><option value="running">running</option>
        <option value="degraded">degraded</option><option value="stopped">stopped</option>
        <option value="unhealthy">unhealthy</option><option value="oom_killed">oom killed</option>
        <option value="restart_loop">restart loop</option><option value="unknown">unknown</option>
        <option value="manual_override">manual override</option>
      </select>
    </label>
  </section>

  {#if loading}
    <p class="muted state">Loading managed instance health…</p>
  {:else if error}
    <section class="panel state error" role="alert"><p>{error}</p><button type="button" onclick={loadRows}>Retry</button></section>
  {:else if filteredRows.length === 0}
    <p class="muted state">No managed instances match the current filters.</p>
  {:else}
    <section class="instance-grid" aria-label="Managed instances">
      {#each filteredRows as row (managedInstanceKey(row))}
        <button class:selected={selected && managedInstanceKey(selected) === managedInstanceKey(row)} class="instance-card" type="button" onclick={() => selectInstance(row)}>
          <header><strong>{row.runtime_target_name}</strong><span class={`badge ${statusClass(row.status)}`}>{row.status}</span></header>
          <p>{row.host || 'host unavailable'} · {row.supervisor_type}</p>
          <dl>
            <div><dt>Restarts</dt><dd>{row.restart_count}</dd></div>
            <div><dt>Consecutive</dt><dd>{row.consecutive_restart_count}</dd></div>
            <div><dt>Memory</dt><dd>{formatBytes(row.memory_current_bytes)}{memoryPercent(row) === null ? '' : ` (${memoryPercent(row).toFixed(0)}%)`}</dd></div>
            <div><dt>Observed</dt><dd>{formatInstanceTimestamp(row.last_observed_at)}</dd></div>
            <div><dt>Last recovery</dt><dd>{formatInstanceTimestamp(row.last_recovery_attempt?.requested_at)}</dd></div>
            <div><dt>Maintenance</dt><dd>{row.maintenance_override ? 'active' : 'off'}</dd></div>
          </dl>
          {#if row.failure_reason}<p class="reason">{row.failure_reason}</p>{/if}
        </button>
      {/each}
    </section>
  {/if}

  {#if selected}
    <section class="panel detail-panel">
      <header class="section-heading"><div><h2>{selected.runtime_target_name}</h2><p>{selected.service_id} / {selected.environment_id}</p></div></header>
      {#if detailLoading}<p class="muted">Loading detail and recent history…</p>
      {:else if detailError}<p class="error">{detailError}</p>
      {:else if detail}
        <div class="maintenance">
          <h3>Maintenance override</h3>
          {#if detail.maintenance_override}
            <article class="active-override">
              <strong>Recovery suppressed</strong>
              <p>{detail.maintenance_override.reason}</p>
              <small>{detail.maintenance_override.actor} · expires {formatInstanceTimestamp(detail.maintenance_override.expires_at)}</small>
              <button type="button" onclick={clearMaintenance} disabled={mutating}>Clear override</button>
            </article>
          {:else}
            <div class="maintenance-form">
              <label>Reason <input bind:value={maintenanceReason} maxlength="1024" /></label>
              <label>Expires <input bind:value={maintenanceExpiresAt} type="datetime-local" /></label>
              <button type="button" onclick={setMaintenance} disabled={mutating || !maintenanceReason.trim()}>Set override</button>
            </div>
          {/if}
          {#if mutationMessage}<p class="mutation-message" role="status">{mutationMessage}</p>{/if}
        </div>

        <div class="history-grid">
          <section><h3>Recent health events</h3>
            {#if events.length === 0}<p class="muted">No health events recorded.</p>{:else}
              <div class="history-list">{#each events as event}<article><header><span class={`badge ${statusClass(event.status)}`}>{event.status}</span><time>{formatInstanceTimestamp(event.observed_at)}</time></header><p>{event.reason || event.evidence || 'Observed state change'}</p></article>{/each}</div>
            {/if}
          </section>
          <section><h3>Recent recovery attempts</h3>
            {#if attempts.length === 0}<p class="muted">No recovery attempts recorded.</p>{:else}
              <div class="history-list">{#each attempts as attempt}<article><header><strong>{attempt.result}</strong><time>{formatInstanceTimestamp(attempt.requested_at)}</time></header><p>{attempt.evidence || 'No additional evidence'}</p></article>{/each}</div>
            {/if}
          </section>
        </div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .page { display: grid; gap: 1.5rem; padding: 2rem; }
  .hero, .section-heading, .instance-card header, .history-list header { align-items: flex-start; display: flex; justify-content: space-between; gap: 1rem; }
  .hero h1 { margin: 0.15rem 0; }
  .hero p { color: var(--text-muted); margin: 0; }
  .eyebrow { color: var(--primary) !important; font-size: 0.72rem; font-weight: 700; letter-spacing: 0.12em; text-transform: uppercase; }
  button { cursor: pointer; }
  button:disabled { cursor: not-allowed; opacity: 0.55; }
  .secondary, .maintenance button, .state button { background: transparent; border: 1px solid var(--border-color); border-radius: 7px; color: var(--text); padding: 0.65rem 0.9rem; }
  .summary-grid { display: grid; gap: 0.8rem; grid-template-columns: repeat(6, minmax(0, 1fr)); }
  .summary-grid article, .panel, .instance-card { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 10px; }
  .summary-grid article { border-top: 3px solid var(--primary); display: grid; gap: 0.25rem; padding: 1rem; }
  .summary-grid article.warning { border-top-color: #f59e0b; } .summary-grid article.critical { border-top-color: #ef4444; }
  .summary-grid span, .summary-grid em { color: var(--text-muted); font-size: 0.74rem; font-style: normal; } .summary-grid strong { font-size: 1.7rem; }
  .panel { padding: 1.1rem; }
  .controls { align-items: end; display: flex; gap: 1rem; }
  label { color: var(--text-muted); display: grid; font-size: 0.78rem; gap: 0.35rem; }
  input, select { background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; color: var(--text); min-width: 210px; padding: 0.55rem; }
  .instance-grid { display: grid; gap: 0.9rem; grid-template-columns: repeat(auto-fill, minmax(315px, 1fr)); }
  .instance-card { color: var(--text); padding: 1rem; text-align: left; width: 100%; }
  .instance-card.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .instance-card > p { color: var(--text-muted); font-size: 0.82rem; }
  dl { display: grid; gap: 0.55rem; grid-template-columns: 1fr 1fr; margin: 0.85rem 0; }
  dl div { display: grid; gap: 0.15rem; } dt { color: var(--text-muted); font-size: 0.7rem; text-transform: uppercase; } dd { margin: 0; overflow-wrap: anywhere; }
  .badge { border-radius: 999px; font-size: 0.68rem; font-weight: 700; padding: 0.25rem 0.5rem; text-transform: uppercase; }
  .badge.healthy { background: rgba(34,197,94,.15); color: #4ade80; } .badge.warning { background: rgba(245,158,11,.15); color: #fbbf24; } .badge.critical { background: rgba(239,68,68,.15); color: #f87171; }
  .reason, .error { color: #f87171; } .muted { color: var(--text-muted); } .state { padding: 2rem; text-align: center; }
  .detail-panel { display: grid; gap: 1.2rem; } .section-heading h2, h3 { margin: 0; } .section-heading p { color: var(--text-muted); font-size: .75rem; overflow-wrap: anywhere; }
  .maintenance { border-top: 1px solid var(--border-color); padding-top: 1rem; } .maintenance-form { align-items: end; display: flex; gap: .75rem; margin-top: .75rem; }
  .active-override { background: rgba(245,158,11,.08); border-left: 3px solid #f59e0b; margin-top: .75rem; padding: .9rem; } .active-override small { color: var(--text-muted); display: block; margin-bottom: .7rem; }
  .mutation-message { color: var(--primary); }
  .history-grid { display: grid; gap: 1rem; grid-template-columns: 1fr 1fr; }
  .history-list { display: grid; gap: .65rem; margin-top: .75rem; } .history-list article { border: 1px solid var(--border-color); border-radius: 7px; padding: .75rem; } .history-list p { margin-bottom: 0; } time { color: var(--text-muted); font-size: .72rem; }
  @media (max-width: 1100px) { .summary-grid { grid-template-columns: repeat(3, 1fr); } }
  @media (max-width: 700px) { .page { padding: 1rem; } .summary-grid, .history-grid { grid-template-columns: 1fr; } .controls, .maintenance-form, .hero { align-items: stretch; flex-direction: column; } input, select { min-width: 0; width: 100%; } }
</style>
