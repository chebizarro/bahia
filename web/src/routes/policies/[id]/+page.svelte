<script>
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Select from '$lib/components/Select.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { api } from '$lib/api/client.js';

  let policy = null;
  let environments = [];
  let loading = true;
  let error = null;

  $: policyId = $page.params.id;

  // Edit modal state
  let editOpen = false;
  let editing = false;
  let editError = null;
  let editForm = {
    name: '',
    environment_id: '',
    rules: '[]',
    enforcement: 'warn',
    enabled: true
  };

  // Delete modal state
  let deleteOpen = false;
  let deleting = false;
  let deleteError = null;

  const enforcementOptions = [
    { value: 'warn', label: 'Warn' },
    { value: 'block', label: 'Block' }
  ];

  onMount(async () => {
    try {
      [policy, environments] = await Promise.all([
        api.getPolicy(policyId),
        api.listEnvironments().catch(() => [])
      ]);
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  });

  $: environmentOptions = [
    { value: '', label: 'Global policy' },
    ...environments.map(env => ({ value: env.id, label: env.name }))
  ];

  $: environmentName = policy?.environment_id 
    ? (environments.find(e => e.id === policy.environment_id)?.name || policy.environment_id)
    : 'Global';

  $: formattedRules = policy?.rules 
    ? JSON.stringify(policy.rules, null, 2)
    : '[]';

  function openEditModal() {
    if (!policy) return;
    
    editForm = {
      name: policy.name,
      environment_id: policy.environment_id || '',
      rules: JSON.stringify(policy.rules, null, 2),
      enforcement: policy.enforcement || 'warn',
      enabled: policy.enabled !== undefined ? policy.enabled : true
    };
    editError = null;
    editOpen = true;
  }

  function closeEditModal() {
    editOpen = false;
    editError = null;
  }

  async function handleEdit() {
    // Validate required fields
    if (!editForm.name.trim()) {
      editError = 'Name is required';
      return;
    }

    if (!editForm.enforcement) {
      editError = 'Enforcement mode is required';
      return;
    }

    // Validate and parse rules JSON
    let parsedRules;
    try {
      parsedRules = JSON.parse(editForm.rules);
      if (!Array.isArray(parsedRules)) {
        editError = 'Rules must be a JSON array';
        return;
      }
    } catch (err) {
      editError = 'Rules must be valid JSON';
      return;
    }

    editing = true;
    editError = null;

    try {
      const payload = {
        name: editForm.name.trim(),
        rules: parsedRules,
        enforcement: editForm.enforcement,
        enabled: editForm.enabled,
        environment_id: editForm.environment_id || null
      };

      const updated = await api.updatePolicy(policyId, payload);
      
      // Update local policy with response
      policy = updated;
      closeEditModal();
    } catch (err) {
      editError = err.message || 'Failed to update policy';
    } finally {
      editing = false;
    }
  }

  function openDeleteModal() {
    deleteError = null;
    deleteOpen = true;
  }

  function closeDeleteModal() {
    deleteOpen = false;
    deleteError = null;
  }

  async function handleDelete() {
    deleting = true;
    deleteError = null;

    try {
      await api.deletePolicy(policyId);
      // Navigate back to policies list on success
      goto('/policies');
    } catch (err) {
      deleteError = err.message || 'Failed to delete policy';
      deleting = false;
      // Keep modal open to show error
    }
  }
</script>

<div class="page">
  <a href="/policies" class="back">← Policies</a>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <p class="error">Error: {error}</p>
  {:else if policy}
    <div class="header">
      <h1>{policy.name}</h1>
      <div class="actions">
        <LoadingButton variant="secondary" on:click={openEditModal}>
          Edit
        </LoadingButton>
        <LoadingButton variant="danger" on:click={openDeleteModal}>
          Delete
        </LoadingButton>
      </div>
    </div>
    
    <div class="info-grid">
      <Card title="Scope" value={environmentName} />
      <Card title="Enforcement" value={policy.enforcement || 'warn'} />
      <Card title="Status" value={policy.enabled ? 'Enabled' : 'Disabled'} />
      <Card title="Rules Count" value={Array.isArray(policy.rules) ? policy.rules.length.toString() : '0'} />
    </div>

    <section>
      <h2>Policy Details</h2>
      <div class="detail-row">
        <span class="label">ID:</span>
        <code class="value">{policy.id}</code>
      </div>
      {#if policy.created_at}
        <div class="detail-row">
          <span class="label">Created:</span>
          <span class="value">{new Date(policy.created_at).toLocaleString()}</span>
        </div>
      {/if}
      {#if policy.updated_at}
        <div class="detail-row">
          <span class="label">Updated:</span>
          <span class="value">{new Date(policy.updated_at).toLocaleString()}</span>
        </div>
      {/if}
    </section>

    <section>
      <h2>Rules</h2>
      <pre class="rules-json"><code>{formattedRules}</code></pre>
    </section>
  {/if}
</div>

<!-- Edit Modal -->
<Modal bind:open={editOpen} title="Edit Policy" on:close={closeEditModal}>
  <form on:submit|preventDefault={handleEdit} class="edit-form">
    <div class="form-field">
      <label for="edit-name">Name *</label>
      <Input
        id="edit-name"
        bind:value={editForm.name}
        placeholder="my-policy"
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-environment-id">Scope</label>
      <Select
        id="edit-environment-id"
        bind:value={editForm.environment_id}
        options={environmentOptions}
        disabled={editing}
      />
      <span class="help-text">Leave as "Global policy" to apply to all environments</span>
    </div>

    <div class="form-field">
      <label for="edit-enforcement">Enforcement Mode *</label>
      <Select
        id="edit-enforcement"
        bind:value={editForm.enforcement}
        options={enforcementOptions}
        required
        disabled={editing}
      />
    </div>

    <div class="form-field">
      <label for="edit-rules">Rules (JSON Array) *</label>
      <Textarea
        id="edit-rules"
        bind:value={editForm.rules}
        placeholder='[{"type": "require_sbom"}]'
        rows={10}
        required
        disabled={editing}
      />
      <span class="help-text">Enter policy rules as a JSON array</span>
    </div>

    <div class="form-field">
      <Checkbox
        id="edit-enabled"
        bind:checked={editForm.enabled}
        label="Enabled"
        disabled={editing}
      />
    </div>

    {#if editError}
      <p class="error-message">{editError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        on:click={closeEditModal}
        disabled={editing}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={editing}
      >
        Save
      </LoadingButton>
    </div>
  </form>
</Modal>

<!-- Delete Confirmation Dialog -->
<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete Policy"
  confirmLabel="Delete"
  variant="danger"
  loading={deleting}
  on:confirm={handleDelete}
  on:cancel={closeDeleteModal}
  on:close={closeDeleteModal}
>
  <div class="delete-content">
    <p>Are you sure you want to delete <strong>{policy?.name}</strong>?</p>
    <p class="warning">This action cannot be undone.</p>

    {#if deleteError}
      <p class="error-message">{deleteError}</p>
    {/if}
  </div>
</ConfirmDialog>

<style>
  .page { max-width: 1000px; }
  .back {
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.875rem;
    display: inline-block;
    margin-bottom: 1rem;
  }
  .back:hover { color: var(--text-primary); }
  
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .header h1 {
    margin: 0;
  }
  .actions {
    display: flex;
    gap: 0.75rem;
  }

  .info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    margin-bottom: 1.5rem;
    border: 1px solid var(--border-color);
  }
  section h2 {
    font-size: 1rem;
    color: var(--text-muted);
    margin: 0 0 1rem 0;
  }

  .detail-row {
    display: flex;
    gap: 1rem;
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border-color);
  }
  .detail-row:last-child {
    border-bottom: none;
  }
  .detail-row .label {
    font-weight: 500;
    color: var(--text-muted);
    min-width: 100px;
  }
  .detail-row .value {
    color: var(--text-primary);
    word-break: break-all;
  }

  .rules-json {
    background: var(--hover-bg);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    padding: 1rem;
    overflow-x: auto;
    margin: 0;
  }
  .rules-json code {
    font-family: 'Monaco', 'Menlo', 'Courier New', monospace;
    font-size: 0.875rem;
    line-height: 1.5;
    color: var(--text-primary);
  }

  .loading, .error {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }
  .error { color: var(--error); }

  .edit-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .form-field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .form-field label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }
  .help-text {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-top: -0.25rem;
  }
  .form-actions {
    display: flex;
    gap: 0.75rem;
    justify-content: flex-end;
    margin-top: 0.5rem;
  }

  .delete-content {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .delete-content p {
    margin: 0;
    line-height: 1.5;
  }
  .warning {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
  .error-message {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0;
    padding: 0.5rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
  }
</style>
