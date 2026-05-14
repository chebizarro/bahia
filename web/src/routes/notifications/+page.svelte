<script>
  import { goto } from '$app/navigation';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import Select from '$lib/components/Select.svelte';
  import { toast } from '$lib/components/toast.js';
  import { NotificationIcon, WarningIcon } from '$lib/icons/domain-icons.js';
  import {
    deleteNotificationChannel,
    listNotificationChannels,
    testNotificationChannel,
    updateNotificationChannel
  } from '$lib/stores/notifications.svelte.js';
  import {
    channelDestination,
    channelTypeLabel,
    escapeHtml,
    eventFilterSummary,
    filterChannels,
    formatDateTime,
    getChannelTypeOptions,
    normalizeChannels,
    rawChannelDestination,
    truncateMiddle
  } from './list-utils.js';

  let channels = $state([]);
  let loading = $state(true);
  let error = $state(null);
  let statusFilter = $state('all');
  let typeFilter = $state('all');
  let searchQuery = $state('');
  let actionKey = $state('');
  let deleteTarget = $state(null);
  let deleteDialogOpen = $state(false);

  const statusOptions = [
    { value: 'all', label: 'All statuses' },
    { value: 'enabled', label: 'Enabled' },
    { value: 'disabled', label: 'Disabled' }
  ];

  const typeOptions = $derived([
    { value: 'all', label: 'All channel types' },
    ...getChannelTypeOptions(channels)
  ]);

  const filteredChannels = $derived(filterChannels(channels, {
    status: statusFilter,
    type: typeFilter,
    search: searchQuery
  }));

  const enabledCount = $derived(channels.filter((channel) => channel.enabled).length);

  $effect(() => {
    void loadChannels();
  });

  async function loadChannels() {
    loading = true;
    error = null;

    try {
      channels = normalizeChannels(await listNotificationChannels());
    } catch (err) {
      error = err.message || 'Failed to load notification channels';
      channels = [];
    } finally {
      loading = false;
    }
  }

  async function toggleChannel(channel) {
    const nextEnabled = !channel.enabled;
    actionKey = `toggle:${channel.id}`;

    try {
      const updated = await updateNotificationChannel(channel.id, { enabled: nextEnabled });
      upsertChannel(updated || { ...channel, enabled: nextEnabled });
      toast.success(`${channel.name} ${nextEnabled ? 'enabled' : 'disabled'}`);
    } catch (err) {
      toast.error(`Failed to update channel: ${err.message}`);
    } finally {
      actionKey = '';
    }
  }

  async function testChannel(channel) {
    actionKey = `test:${channel.id}`;

    try {
      await testNotificationChannel(channel.id);
      toast.success(`Test notification sent to ${channel.name}`);
    } catch (err) {
      toast.error(`Failed to send test notification: ${err.message}`);
    } finally {
      actionKey = '';
    }
  }

  function requestDelete(channel) {
    deleteTarget = channel;
    deleteDialogOpen = true;
  }

  async function confirmDelete() {
    if (!deleteTarget) return;

    const target = deleteTarget;
    actionKey = `delete:${target.id}`;

    try {
      await deleteNotificationChannel(target.id);
      channels = channels.filter((channel) => channel.id !== target.id);
      toast.success(`${target.name} deleted`);
      deleteDialogOpen = false;
      deleteTarget = null;
    } catch (err) {
      toast.error(`Failed to delete channel: ${err.message}`);
    } finally {
      actionKey = '';
    }
  }

  function upsertChannel(updated) {
    const index = channels.findIndex((channel) => channel.id === updated.id);
    if (index === -1) {
      channels = [updated, ...channels];
      return;
    }

    channels = channels.map((channel, i) => i === index ? { ...channel, ...updated } : channel);
  }

  function resetFilters() {
    statusFilter = 'all';
    typeFilter = 'all';
    searchQuery = '';
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <h1>Notifications</h1>
      <p class="subtitle">Manage webhook and Nostr DM delivery channels for platform events.</p>
    </div>
    <div class="header-actions">
      <LoadingButton variant="secondary" loading={loading} onclick={loadChannels}>
        Refresh
      </LoadingButton>
      <LoadingButton variant="secondary" onclick={() => goto('/notifications/log')}>
        View log
      </LoadingButton>
      <LoadingButton variant="primary" onclick={() => goto('/notifications/new')}>
        Create channel
      </LoadingButton>
    </div>
  </div>

  <section class="filters-panel" aria-labelledby="notification-filter-heading">
    <h2 id="notification-filter-heading">Filters</h2>
    <div class="filters">
      <div class="filter-field">
        <label for="notification-status-filter">Status</label>
        <Select id="notification-status-filter" bind:value={statusFilter} options={statusOptions} placeholder="" />
      </div>

      <div class="filter-field">
        <label for="notification-type-filter">Channel type</label>
        <Select id="notification-type-filter" bind:value={typeFilter} options={typeOptions} placeholder="" />
      </div>

      <div class="filter-field search-field">
        <label for="notification-search">Search channels</label>
        <Input id="notification-search" type="search" bind:value={searchQuery} placeholder="Name, destination, event type, or ID" />
      </div>

      <div class="filter-actions">
        <LoadingButton variant="secondary" onclick={resetFilters} disabled={statusFilter === 'all' && typeFilter === 'all' && !searchQuery}>
          Reset
        </LoadingButton>
      </div>
    </div>
  </section>

  <div class="summary-row" aria-live="polite">
    <span>{filteredChannels.length} of {channels.length} channels</span>
    <span>{enabledCount} enabled</span>
  </div>

  {#if loading}
    <p class="loading">Loading notification channels...</p>
  {:else if error}
    <ErrorState message={error} resetLabel="Try again" onReset={loadChannels} />
  {:else if channels.length === 0}
    <EmptyState
      title="No notification channels"
      message="Create a webhook or Nostr DM channel to start receiving platform event notifications."
      iconComponent={NotificationIcon}
      actionLabel="Create channel"
      onAction={() => goto('/notifications/new')}
    />
  {:else}
    <div class="table-container">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Destination</th>
            <th>Event filter</th>
            <th>Status</th>
            <th>Updated</th>
            <th class="actions-heading">Actions</th>
          </tr>
        </thead>
        <tbody>
          {#each filteredChannels as channel (channel.id)}
            <tr>
              <td>
                <div class="channel-name">
                  <strong>{channel.name}</strong>
                  <code>{truncateMiddle(channel.id, 8, 6)}</code>
                </div>
              </td>
              <td>{channelTypeLabel(channel.channel_type)}</td>
              <td><span title={rawChannelDestination(channel) || channelDestination(channel)}>{@html escapeHtml(channelDestination(channel))}</span></td>
              <td>{eventFilterSummary(channel.event_filter)}</td>
              <td>
                <span class="channel-status" class:enabled={channel.enabled} class:disabled={!channel.enabled}>
                  <span class="status-dot" aria-hidden="true"></span>
                  {channel.enabled ? 'Enabled' : 'Disabled'}
                </span>
              </td>
              <td>{formatDateTime(channel.updated_at || channel.created_at)}</td>
              <td>
                <div class="row-actions">
                  <button
                    type="button"
                    class="action-button"
                    disabled={Boolean(actionKey)}
                    onclick={() => toggleChannel(channel)}
                  >
                    {actionKey === `toggle:${channel.id}` ? 'Saving...' : channel.enabled ? 'Disable' : 'Enable'}
                  </button>
                  <button
                    type="button"
                    class="action-button"
                    disabled={Boolean(actionKey)}
                    onclick={() => testChannel(channel)}
                  >
                    {actionKey === `test:${channel.id}` ? 'Sending...' : 'Test'}
                  </button>
                  <button
                    type="button"
                    class="action-button"
                    disabled={Boolean(actionKey)}
                    onclick={() => goto(`/notifications/${encodeURIComponent(channel.id)}/edit`)}
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    class="action-button danger"
                    disabled={Boolean(actionKey)}
                    onclick={() => requestDelete(channel)}
                  >
                    Delete
                  </button>
                </div>
              </td>
            </tr>
          {/each}
          {#if filteredChannels.length === 0}
            <tr>
              <td colspan="7" class="empty-row">No channels match the current filters.</td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<ConfirmDialog
  bind:open={deleteDialogOpen}
  title="Delete notification channel"
  titleIcon={WarningIcon}
  message={deleteTarget ? `Delete ${deleteTarget.name}? Delivery history remains, but this channel can no longer receive notifications.` : ''}
  confirmLabel="Delete channel"
  variant="danger"
  loading={actionKey === `delete:${deleteTarget?.id}`}
  onConfirm={confirmDelete}
  onClose={() => { deleteTarget = null; }}
  onCancel={() => { deleteTarget = null; }}
/>

<style>
  .page { max-width: 1200px; }

  .header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
    margin-bottom: 1.5rem;
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
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
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

  .search-field {
    grid-column: span 2;
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

  .summary-row span:not(:last-child) {
    border-right: 1px solid var(--border-color);
    padding-right: 0.75rem;
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

  .actions-heading {
    text-align: right;
  }

  .channel-name {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .channel-name code {
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  .channel-status {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    border-radius: 999px;
    padding: 0.125rem 0.5rem;
    font-size: 0.75rem;
    font-weight: 600;
  }

  .channel-status.enabled {
    color: var(--success);
    background: color-mix(in srgb, var(--success) 12%, transparent);
  }

  .channel-status.disabled {
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

  .row-actions {
    display: flex;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .action-button {
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--card-bg);
    color: var(--text-primary);
    cursor: pointer;
    font-size: 0.8125rem;
    padding: 0.375rem 0.625rem;
  }

  .action-button:hover:not(:disabled) {
    background: var(--hover-bg);
  }

  .action-button.danger {
    color: var(--error);
  }

  .action-button:disabled {
    cursor: not-allowed;
    opacity: 0.5;
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

    .search-field {
      grid-column: span 1;
    }
  }
</style>
