<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import {
    getNotificationChannel,
    listNotificationChannels,
    notificationState,
    subscribeToNotificationUpdates,
    updateNotificationChannel
  } from '$lib/stores/notifications.svelte.js';
  import { toast } from '$lib/components/toast.js';
  import NotificationChannelForm from '../../NotificationChannelForm.svelte';

  let channel = $state(null);
  let loading = $state(true);
  let loadError = $state('');
  let saving = $state(false);
  let saveError = $state('');
  let requestedChannelId = '';

  const channelId = $derived(page.params.id);

  $effect(() => {
    const id = channelId;
    if (!id) return;
    const liveChannel = notificationState.channels.find((candidate) => candidate.id === id);
    if (liveChannel) {
      channel = liveChannel;
      loadError = '';
      return;
    }
    if (!loading && !notificationState.channelsLoading && !loadError) {
      channel = null;
      loadError = 'Notification channel not found';
      return;
    }
    if (!channel && requestedChannelId !== id) {
      requestedChannelId = id;
      void loadChannel(id);
    }
  });

  onMount(() => {
    let disposed = false;
    let unsubscribe = null;
    void subscribeToNotificationUpdates().then((stop) => {
      if (disposed) stop();
      else unsubscribe = stop;
    }).catch((caught) => {
      loadError = caught?.message || 'Failed to subscribe to notification updates';
    });
    return () => {
      disposed = true;
      unsubscribe?.();
    };
  });

  async function loadChannel(id) {
    loading = true;
    loadError = '';
    channel = null;

    try {
      channel = await getNotificationChannel(id);

      // Some notification handlers return the raw channel while the shared API
      // client unwraps `data`; fall back to the list endpoint if needed.
      if (!channel) {
        const channels = await listNotificationChannels();
        channel = (channels || []).find((candidate) => candidate.id === id) || null;
      }

      if (!channel) {
        throw new Error('Notification channel not found');
      }
    } catch (err) {
      loadError = err?.message || 'Failed to load notification channel';
    } finally {
      loading = false;
    }
  }

  async function updateChannel(payload) {
    saving = true;
    saveError = '';

    try {
      const updated = await updateNotificationChannel(channelId, payload);
      channel = updated || { ...channel, ...payload };
      toast.success(`${channel?.name || payload.name} updated`);
      await goto('/notifications');
    } catch (err) {
      saveError = err?.message || 'Failed to update notification channel';
    } finally {
      saving = false;
    }
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <a class="back-link" href="/notifications">← Notifications</a>
      <h1>Edit notification channel</h1>
      <p class="subtitle">Update delivery configuration and event filters.</p>
    </div>
  </div>

  {#if loading}
    <p class="loading">Loading notification channel...</p>
  {:else if loadError}
    <ErrorState message={loadError} resetLabel="Try again" onReset={() => loadChannel(channelId)} />
  {:else if channel}
    <NotificationChannelForm
      initialChannel={channel}
      submitLabel="Save channel"
      submitting={saving}
      submitError={saveError}
      onSubmit={updateChannel}
      onCancel={() => goto('/notifications')}
    />
  {/if}
</div>

<style>
  .page { max-width: 960px; }

  .header {
    margin-bottom: 1.5rem;
  }

  .back-link {
    color: var(--text-muted);
    display: inline-block;
    font-size: 0.875rem;
    margin-bottom: 0.75rem;
    text-decoration: none;
  }

  .back-link:hover {
    color: var(--text-primary);
  }

  .subtitle,
  .loading {
    color: var(--text-muted);
  }

  .loading {
    padding: 2rem;
    text-align: center;
  }
</style>
