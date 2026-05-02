<script>
  import { goto } from '$app/navigation';
  import { api } from '$lib/api/client.js';
  import { toast } from '$lib/components/toast.js';
  import NotificationChannelForm from '../NotificationChannelForm.svelte';

  let saving = $state(false);
  let saveError = $state('');

  async function createChannel(payload) {
    saving = true;
    saveError = '';

    try {
      const created = await api.createNotificationChannel(payload);
      toast.success(`${created?.name || payload.name} created`);
      await goto('/notifications');
    } catch (err) {
      saveError = err?.message || 'Failed to create notification channel';
    } finally {
      saving = false;
    }
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <a class="back-link" href="/notifications">← Notifications</a>
      <h1>Create notification channel</h1>
      <p class="subtitle">Add a webhook or Nostr DM destination for platform event notifications.</p>
    </div>
  </div>

  <NotificationChannelForm
    submitLabel="Create channel"
    submitting={saving}
    submitError={saveError}
    onSubmit={createChannel}
    onCancel={() => goto('/notifications')}
  />
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

  .subtitle {
    color: var(--text-muted);
    margin-top: 0.25rem;
  }
</style>
