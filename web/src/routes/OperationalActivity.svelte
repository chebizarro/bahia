<script>
  let {
    items = [],
    title = 'Live operational activity',
    emptyMessage = 'No live operational events received yet.',
    limit = 8
  } = $props();

  let visibleItems = $derived((items || []).slice(0, limit));

  function label(operation) {
    return operation.operation || operation.step || operation.domain || 'operation';
  }

  function statusClass(status) {
    const value = String(status || '').toLowerCase();
    if (['success', 'succeeded', 'completed', 'complete'].includes(value)) return 'success';
    if (['failure', 'failed', 'error', 'rejected', 'cancelled', 'canceled', 'timeout', 'timed_out'].includes(value)) return 'error';
    return 'active';
  }

  function timestamp(operation) {
    const value = operation.completed_at || operation.status_at || operation.updated_at || operation.requested_at;
    if (!value) return '';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
  }
</script>

<section class="operational-activity" aria-live="polite">
  <div class="activity-heading">
    <h2>{title}</h2>
    {#if visibleItems.length}<span>{visibleItems.length} recent</span>{/if}
  </div>
  {#if visibleItems.length}
    <ol>
      {#each visibleItems as operation (operation.source + ':' + operation.operation_id)}
        <li>
          <span class="status-dot {statusClass(operation.status)}" aria-hidden="true"></span>
          <div class="activity-copy">
            <div class="activity-line">
              <strong>{label(operation)}</strong>
              <span class="status {statusClass(operation.status)}">{operation.status || 'processing'}</span>
            </div>
            {#if operation.message}<p>{operation.message}</p>{/if}
            {#if operation.error && operation.error !== operation.message}<p class="activity-error">{operation.error}</p>{/if}
            <small>{timestamp(operation)}</small>
          </div>
        </li>
      {/each}
    </ol>
  {:else}
    <p class="empty">{emptyMessage}</p>
  {/if}
</section>

<style>
  .operational-activity {
    margin: 1.5rem 0;
    padding: 1.25rem;
    border: 1px solid var(--border-color);
    border-radius: 8px;
    background: var(--card-bg);
  }
  .activity-heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  h2 { margin: 0; font-size: 1rem; color: var(--text-primary); }
  .activity-heading span, .empty, small { color: var(--text-muted); font-size: .8rem; }
  ol { list-style: none; display: grid; gap: .75rem; margin: 0; padding: 0; }
  li { display: flex; align-items: flex-start; gap: .75rem; }
  .status-dot { width: .65rem; height: .65rem; margin-top: .35rem; border-radius: 999px; flex: 0 0 auto; background: #3b82f6; }
  .status-dot.success { background: #10b981; }
  .status-dot.error { background: #ef4444; }
  .activity-copy { min-width: 0; flex: 1; }
  .activity-line { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  strong { font-size: .875rem; overflow-wrap: anywhere; }
  .status { font-size: .72rem; text-transform: uppercase; font-weight: 700; color: #3b82f6; }
  .status.success { color: #10b981; }
  .status.error, .activity-error { color: #ef4444; }
  p { margin: .2rem 0; font-size: .82rem; overflow-wrap: anywhere; }
  .empty { margin: 0; }
</style>
