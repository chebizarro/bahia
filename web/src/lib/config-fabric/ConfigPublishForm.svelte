<script>
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import api from '$lib/api/client.js';
  import {
    CONFIG_ACL_LIST,
    CONFIG_POLICY,
    initialConfigPublishForm,
    validateConfigPublishForm
  } from './model.js';

  let {
    initial = initialConfigPublishForm(),
    driftRows = [],
    onPublished = async () => {},
    onCancel = null
  } = $props();

  function createInitialForm() {
    return { ...initial };
  }

  let form = $state(createInitialForm());
  let submitting = $state(false);
  let error = $state('');
  let receipt = $state(null);

  const kindOptions = [
    { value: String(CONFIG_POLICY), label: `${CONFIG_POLICY} — NIP-78 structured policy` },
    { value: String(CONFIG_ACL_LIST), label: `${CONFIG_ACL_LIST} — NIP-51 membership list` }
  ];

  function handleKindChange(event) {
    form.kind = event.currentTarget.value;
    if (Number(form.kind) === CONFIG_ACL_LIST) {
      form.policy_name = 'membership';
      form.schema = 'cascadia.config.membership.v1';
    }
  }

  function suggestSchema() {
    const policyName = form.policy_name.trim();
    if (policyName && Number(form.kind) === CONFIG_POLICY) {
      form.schema = `cascadia.config.${policyName}.v1`;
    }
  }

  async function submit() {
    error = '';
    receipt = null;
    const validation = validateConfigPublishForm(form, driftRows);
    if (!validation.success) {
      error = validation.error;
      return;
    }

    submitting = true;
    try {
      receipt = await api.publishConfigFabricEvent(validation.payload);
      await onPublished(receipt);
    } catch (err) {
      error = err?.message || 'Failed to publish config';
    } finally {
      submitting = false;
    }
  }
</script>

<form class="publish-form" onsubmit={(event) => { event.preventDefault(); void submit(); }}>
  <p class="secret-boundary">
    Publish policy values and secret references only. Raw passwords, tokens, private keys, and credentials are rejected.
  </p>

  <div class="form-grid">
    <div class="field full">
      <label for="config-kind">Event shape *</label>
      <Select id="config-kind" bind:value={form.kind} options={kindOptions} onchange={handleKindChange} disabled={submitting} />
    </div>
    <div class="field">
      <label for="config-service">Service ID *</label>
      <Input id="config-service" bind:value={form.service_id} placeholder="khatru-relay" required disabled={submitting} />
    </div>
    <div class="field">
      <label for="config-policy-name">Policy name *</label>
      <Input id="config-policy-name" bind:value={form.policy_name} placeholder="rate-limits" onblur={suggestSchema} required disabled={submitting} />
    </div>
    <div class="field">
      <label for="config-scope">Scope *</label>
      <Input id="config-scope" bind:value={form.scope} placeholder="prod" required disabled={submitting} />
      <span class="help">prod, staging, fleet, or host:&lt;host&gt;</span>
    </div>
    <div class="field">
      <label for="config-version">Version *</label>
      <Input id="config-version" type="number" min="1" step="1" bind:value={form.version} required disabled={submitting} />
      <span class="help">Must exceed the latest desired version for this coordinate.</span>
    </div>
    <div class="field full">
      <label for="config-schema">Schema *</label>
      <Input id="config-schema" bind:value={form.schema} placeholder="cascadia.config.rate-limits.v1" required disabled={submitting} />
    </div>
  </div>

  {#if Number(form.kind) === CONFIG_POLICY}
    <div class="field">
      <label for="config-policy-json">Policy JSON *</label>
      <Textarea id="config-policy-json" bind:value={form.policy} rows={12} required disabled={submitting} />
    </div>
    <div class="field">
      <label for="config-secret-refs">Secret references JSON</label>
      <Textarea id="config-secret-refs" bind:value={form.secret_refs} rows={5} disabled={submitting} />
      <span class="help">Each entry accepts only provider (signet, file, or service) and ref.</span>
    </div>
  {:else}
    <div class="field">
      <label for="config-list-items">List items JSON *</label>
      <Textarea id="config-list-items" bind:value={form.items} rows={10} required disabled={submitting} />
      <span class="help">Array entries use tag p, a, or r and a string value.</span>
    </div>
  {/if}

  {#if error}
    <p class="error-message" role="alert">{error}</p>
  {/if}
  {#if receipt}
    <p class="success-message" role="status">Published version {receipt.version} as event <code>{receipt.event_id}</code>.</p>
  {/if}

  <div class="actions">
    {#if onCancel}
      <LoadingButton type="button" variant="secondary" onclick={onCancel} disabled={submitting}>Cancel</LoadingButton>
    {/if}
    <LoadingButton type="submit" variant="primary" loading={submitting}>Publish Config</LoadingButton>
  </div>
</form>

<style>
  .publish-form { display: flex; flex-direction: column; gap: 1rem; }
  .secret-boundary {
    margin: 0;
    padding: 0.75rem;
    border-left: 4px solid var(--warning, #f59e0b);
    background: rgba(245, 158, 11, 0.1);
    color: var(--text-primary);
    font-size: 0.875rem;
  }
  .form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
  .field { display: flex; flex-direction: column; gap: 0.4rem; }
  .field.full { grid-column: 1 / -1; }
  label { color: var(--text-primary); font-size: 0.875rem; font-weight: 500; }
  .help { color: var(--text-muted); font-size: 0.75rem; }
  .error-message, .success-message { border-radius: 4px; font-size: 0.875rem; margin: 0; padding: 0.65rem; overflow-wrap: anywhere; }
  .error-message { color: var(--error); background: rgba(239, 68, 68, 0.1); }
  .success-message { color: var(--success, #10b981); background: rgba(16, 185, 129, 0.1); }
  .actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
  @media (max-width: 640px) {
    .form-grid { grid-template-columns: 1fr; }
  }
</style>
