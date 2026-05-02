<script>
  import { goto } from '$app/navigation';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import Select from '$lib/components/Select.svelte';
  import { api } from '$lib/api/client.js';
  import {
    channelLabel,
    filterNotificationLogs,
    formatDateTime,
    getNotificationLogChannelOptions,
    getNotificationLogEventTypeOptions,
    normalizeChannels,
    normalizeNotificationLogs,
    truncateMiddle
  } from '../list-utils.js';

  let logs = $state([]);
  let channels = $state([]);
  let loading = $state(true);
  let error = $state(null);
  let channelFilter = $state('all');
  let eventTypeFilter = $state('all');

  const channelOptions = $derived([
    { value: 'all', label: 'All channels' },
    ...getNotificationLogChannelOptions(logs, channels)
  ]);

  const eventTypeOptions = $derived([
    { value: 'all', label: 'All event types' },
    ...getNotificationLogEventTypeOptions(logs)
  ]);

  const filteredLogs = $derived(filterNotificationLogs(logs, {
    channel: channelFilter,
    eventType: eventTypeFilter
  }));

  $effect(() => {
    void loadLogs();
  });

  async function loadLogs() {
    loading = true;
    error = null;

    try {
      logs = normalizeNotificationLogs(await api.listNotificationLogs());

      try {
        channels = normalizeChannels(await api.listNotificationChannels());
      } catch {
        channels = [];
      }
    } catch (err) {
      error = err?.message || 'Failed to load notification log';
      logs = [];
      channels = [];
    } finally {
      loading = false;
    }
  }

  function resetFilters() {
    channelFilter = 'all';
    eventTypeFilter = 'all';
  }

  function statusLabel(status) {
    const value = String(status || '').replace(/_/g, ' ');
    return value ? value.charAt(0).toUpperCase() + value.slice(1) : 'Unknown';
  }

  function statusClass(status) {
    const value = String(status || '').toLowerCase();
    if (value === 'sent') return 'sent';
    if (value === 'failed') return 'failed';
    if (value === 'retrying') return 'retrying';
    return 'pending';
  }

  function formatPayload(payload) {
    if (!payload || Object.keys(payload).length === 0) return '-';
    try {
      return JSON.stringify(payload, null, 2);
    } catch {
      return String(payload);
    }
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <a class="back-link" href="/notifications">← Notifications</a>
      <h1>Notification log</h1>
      <p class="subtitle">Review recent notification delivery attempts across channels and event types.</p>
    </div>
    <div class="header-actions">
      <LoadingButton variant="secondary" loading={loading} onclick={loadLogs}>
        Refresh
      </LoadingButton>
      <LoadingButton variant="primary" onclick={() => goto('/notifications/new')}>
        Create channel
      </LoadingButton>
    </div>
  </div>

  <section class="filters-panel" aria-labelledby="notification-log-filter-heading">
    <h2 id="notification-log-filter-heading">Filters</h2>
    <div class="filters">
      <div class="filter-field">
        <label for="notification-log-channel-filter">Channel</label>
        <Select id="notification-log-channel-filter" bind:value={channelFilter} options={channelOptions} placeholder="" />
      </div>

      <div class="filter-field">
        <label for="notification-log-event-type-filter">Event type</label>
        <Select id="notification-log-event-type-filter" bind:value={eventTypeFilter} options={eventTypeOptions} placeholder="" />
      </div>

      <div class="filter-actions">
        <LoadingButton variant="secondary" onclick={resetFilters} disabled={channelFilter === 'all' && eventTypeFilter === 'all'}>
          Reset
        </LoadingButton>
      </div>
    </div>
  </section>

  <div class="summary-row" aria-live="polite">
    <span>{filteredLogs.length} of {logs.length} log entries</span>
    <span>Most recent 50 delivery attempts</span>
  </div>

  {#if loading}
    <p class="loading">Loading notification log...</p>
  {:else if error}
    <ErrorState message={error} resetLabel="Try again" onReset={loadLogs} />
  {:else if logs.length === 0}
    <EmptyState
      title="No notification deliveries yet"
      message="Delivery attempts will appear here after events match an enabled notification channel."
      icon="📬"
    />
  {:else}
    <div class="table-container">
      <table>
        <thead>
          <tr>
            <th>Created</th>
            <th>Channel</th>
            <th>Event type</th>
            <th>Status</th>
            <th>Attempts</th>
            <th>Last error</th>
            <th>Payload</th>
          </tr>
        </thead>
        <tbody>
          {#each filteredLogs as log (log.id)}
            <tr>
              <td>{formatDateTime(log.created_at)}</td>
              <td>
                <div class="channel-cell">
                  <strong>{channelLabel(log.channel_id, channels)}</strong>
                  <code title={log.channel_id}>{truncateMiddle(log.channel_id, 8, 6)}</code>
                </div>
              </td>
              <td><code>{log.event_type || '-'}</code></td>
              <td>
                <span class="delivery-status" class:sent={statusClass(log.status) === 'sent'} class:failed={statusClass(log.status) === 'failed'} class:retrying={statusClass(log.status) === 'retrying'} class:pending={statusClass(log.status) === 'pending'}>
                  <span class="status-dot" aria-hidden="true"></span>
                  {statusLabel(log.status)}
                </span>
              </td>
              <td>{log.attempts ?? 0}</td>
              <td class="error-cell">{log.last_error || '-'}</td>
              <td>
                <details class="payload-details">
                  <summary>View payload</summary>
                  <pre>{formatPayload(log.payload)}</pre>
                </details>
              </td>
            </tr>
          {/each}
          {#if filteredLogs.length === 0}
            <tr>
              <td colspan="7" class="empty-row">No log entries match the selected channel and event type.</td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .page { max-width: 1200px; }

  .header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
    margin-bottom: 1.5rem;
  }

  .back-link {
    color: var(--primary);
    display: inline-block;
    font-size: 0.875rem;
    margin-bottom: 0.5rem;
    text-decoration: none;
  }

  .back-link:hover {
    text-decoration: underline;
  }

  .subtitle {
    color: var(--text-muted);
    margin-top: 0.25rem;
  }

  .header-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    justify-content: flex-end;
  }

  .filters-panel {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 1rem;
    margin-bottom: 1rem;
  }

  .filters-panel h2 {
    font-size: 1rem;
    color: var(--text-muted);
    margin-bottom: 1rem;
  }

  .filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.75rem;
    align-items: end;
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .filter-field label {
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .filter-actions {
    display: flex;
    gap: 0.5rem;
  }

  .summary-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    color: var(--text-muted);
    font-size: 0.875rem;
    margin-bottom: 1rem;
  }

  .summary-row span:not(:last-child)::after {
    content: '•';
    margin-left: 0.75rem;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .table-container {
    overflow-x: auto;
  }

  table {
    width: 100%;
    border-collapse: collapse;
  }

  th,
  td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid var(--border-color, #2a2a4a);
    vertical-align: top;
  }

  th {
    background: var(--card-bg, #1a1a2e);
    font-weight: 600;
    font-size: 0.75rem;
    text-transform: uppercase;
    color: var(--text-muted, #888);
  }

  .channel-cell {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .channel-cell code {
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  .delivery-status {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    border-radius: 999px;
    padding: 0.125rem 0.5rem;
    font-size: 0.75rem;
    font-weight: 600;
  }

  .delivery-status.sent {
    color: var(--success);
    background: color-mix(in srgb, var(--success) 12%, transparent);
  }

  .delivery-status.failed {
    color: var(--error);
    background: color-mix(in srgb, var(--error) 12%, transparent);
  }

  .delivery-status.retrying {
    color: var(--warning, #f59e0b);
    background: color-mix(in srgb, var(--warning, #f59e0b) 12%, transparent);
  }

  .delivery-status.pending {
    color: var(--text-muted);
    background: var(--hover-bg);
  }

  .status-dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 999px;
    display: inline-block;
    background: currentColor;
  }

  .error-cell {
    color: var(--error);
    max-width: 20rem;
    word-break: break-word;
  }

  .payload-details summary {
    color: var(--primary);
    cursor: pointer;
    font-size: 0.8125rem;
  }

  .payload-details pre {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-primary);
    font-size: 0.75rem;
    margin: 0.5rem 0 0;
    max-width: 26rem;
    overflow-x: auto;
    padding: 0.5rem;
    white-space: pre-wrap;
  }

  .empty-row {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  @media (max-width: 760px) {
    .header {
      flex-direction: column;
    }

    .header-actions {
      justify-content: flex-start;
    }
  }
</style>
