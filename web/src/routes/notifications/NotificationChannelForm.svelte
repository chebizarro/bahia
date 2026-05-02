<script>
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import Select from '$lib/components/Select.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import {
    CHANNEL_TYPE_OPTIONS,
    EVENT_FILTER_MODE_OPTIONS,
    buildNotificationChannelPayload,
    createNotificationChannelForm
  } from './form-utils.js';

  let {
    initialChannel = null,
    submitLabel = 'Save channel',
    submitting = false,
    submitError = '',
    onSubmit,
    onCancel
  } = $props();

  let form = $state(createNotificationChannelForm());
  let localError = $state('');

  $effect(() => {
    form = createNotificationChannelForm(initialChannel);
  });

  const isWebhook = $derived(form.channel_type === 'webhook');
  const isNostrDM = $derived(form.channel_type === 'nostr_dm');
  const displayError = $derived(localError || submitError);

  async function handleSubmit() {
    localError = '';

    try {
      const payload = buildNotificationChannelPayload(form);
      await onSubmit?.(payload);
    } catch (err) {
      localError = err?.message || 'Failed to save notification channel';
    }
  }
</script>

<form class="channel-form" onsubmit={(event) => { event.preventDefault(); void handleSubmit(); }}>
  {#if displayError}
    <div class="form-error" role="alert">{displayError}</div>
  {/if}

  <section class="form-section" aria-labelledby="channel-basics-heading">
    <div>
      <h2 id="channel-basics-heading">Channel details</h2>
      <p>Name the destination, choose a delivery type, and decide whether it is active.</p>
    </div>

    <div class="field-grid">
      <div class="form-field">
        <label for="notification-channel-name">Name *</label>
        <Input
          id="notification-channel-name"
          bind:value={form.name}
          placeholder="Production deployments"
          required
          disabled={submitting}
        />
      </div>

      <div class="form-field">
        <label for="notification-channel-type">Channel type *</label>
        <Select
          id="notification-channel-type"
          bind:value={form.channel_type}
          options={CHANNEL_TYPE_OPTIONS}
          placeholder=""
          disabled={submitting}
          required
        />
      </div>
    </div>

    <Checkbox id="notification-channel-enabled" bind:checked={form.enabled} disabled={submitting} label="Enabled" />
  </section>

  <section class="form-section" aria-labelledby="delivery-config-heading">
    <div>
      <h2 id="delivery-config-heading">Delivery configuration</h2>
      {#if isWebhook}
        <p>Webhook notifications are sent as HTTP POST requests. Optional secrets sign the payload with HMAC-SHA256.</p>
      {:else}
        <p>Nostr DM notifications are encrypted direct messages to the recipient public key.</p>
      {/if}
    </div>

    {#if isWebhook}
      <div class="form-field">
        <label for="webhook-url">Webhook URL *</label>
        <Input
          id="webhook-url"
          type="url"
          bind:value={form.webhook_url}
          placeholder="https://hooks.example.com/bahia"
          required
          disabled={submitting}
        />
      </div>

      <div class="form-field">
        <label for="webhook-secret">Signing secret</label>
        <Input
          id="webhook-secret"
          type="password"
          bind:value={form.webhook_secret}
          placeholder="Optional shared secret"
          disabled={submitting}
        />
        <span class="help-text">When set, Bahia sends an <code>X-Bahia-Signature</code> header.</span>
      </div>

      <div class="form-field">
        <label for="webhook-headers">Custom headers JSON</label>
        <Textarea
          id="webhook-headers"
          bind:value={form.webhook_headers}
          rows={5}
          placeholder={'{\n  "X-Team": "platform"\n}'}
          disabled={submitting}
        />
        <span class="help-text">Optional JSON object of string header names and values.</span>
      </div>
    {:else if isNostrDM}
      <div class="form-field">
        <label for="nostr-pubkey">Recipient pubkey *</label>
        <Input
          id="nostr-pubkey"
          bind:value={form.nostr_pubkey}
          placeholder="64-character hex public key"
          required
          disabled={submitting}
        />
      </div>
    {/if}
  </section>

  <section class="form-section" aria-labelledby="event-filter-heading">
    <div>
      <h2 id="event-filter-heading">Event filter</h2>
      <p>Choose which platform events should be delivered to this channel.</p>
    </div>

    <div class="form-field">
      <label for="event-filter-mode">Filter mode</label>
      <Select
        id="event-filter-mode"
        bind:value={form.event_filter_mode}
        options={EVENT_FILTER_MODE_OPTIONS}
        placeholder=""
        disabled={submitting}
      />
    </div>

    {#if form.event_filter_mode === 'types'}
      <div class="form-field">
        <label for="event-types">Event types</label>
        <Textarea
          id="event-types"
          bind:value={form.event_types}
          rows={5}
          placeholder={'deployment.succeeded\ndeployment.failed'}
          disabled={submitting}
        />
        <span class="help-text">Enter event type names separated by commas or new lines.</span>
      </div>
    {:else if form.event_filter_mode === 'json'}
      <div class="form-field">
        <label for="event-filter-json">Event filter JSON</label>
        <Textarea
          id="event-filter-json"
          bind:value={form.event_filter_json}
          rows={6}
          placeholder={'{\n  "type": "deployment.failed"\n}'}
          disabled={submitting}
        />
        <span class="help-text">Advanced JSON object saved directly as <code>event_filter</code>.</span>
      </div>
    {/if}
  </section>

  <div class="form-actions">
    <LoadingButton type="button" variant="secondary" disabled={submitting} onclick={onCancel}>
      Cancel
    </LoadingButton>
    <LoadingButton type="submit" variant="primary" loading={submitting}>
      {submitLabel}
    </LoadingButton>
  </div>
</form>

<style>
  .channel-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    max-width: 820px;
  }

  .form-section {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    padding: 1rem;
    border: 1px solid var(--border-color);
    border-radius: 8px;
    background: var(--card-bg);
  }

  .form-section h2 {
    font-size: 1rem;
    margin-bottom: 0.25rem;
  }

  .form-section p,
  .help-text {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .field-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
    gap: 1rem;
  }

  .form-field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .form-field label {
    color: var(--text-muted);
    font-size: 0.875rem;
    font-weight: 500;
  }

  .form-error {
    border: 1px solid var(--error);
    border-radius: 6px;
    background: color-mix(in srgb, var(--error) 12%, transparent);
    color: var(--error);
    padding: 0.75rem 1rem;
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.75rem;
  }

  code {
    font-size: 0.8125rem;
  }
</style>
