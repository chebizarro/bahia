<script>
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Select from '$lib/components/Select.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { api } from '$lib/api/client.js';

  let policies = $state([]);
  let environments = $state([]);
  let loading = $state(true);
  let error = $state(null);

  // Create modal state
  let createOpen = $state(false);
  let creating = $state(false);
  let createError = $state(null);

  let createForm = $state({
    name: '',
    environment_id: '',
    rules: '[]',
    enforcement: 'warn',
    enabled: true
  });

  const enforcementOptions = [
    { value: 'warn', label: 'Warn' },
    { value: 'block', label: 'Block' }
  ];

  $effect(() => {
    void loadPolicyList();
  });

  async function loadPolicyList() {
    try {
      [policies, environments] = await Promise.all([
        api.listPolicies().catch(() => []),
        api.listEnvironments().catch(() => [])
      ]);
    } catch (err) {
      console.error('Failed to load data:', err);
      error = err.message;
    } finally {
      loading = false;
    }
  }

  let environmentOptions = $derived([
    { value: '', label: 'Global policy' },
    ...environments.map(env => ({ value: env.id, label: env.name }))
  ]);

  let columns = $derived([
    { key: 'name', label: 'Name' },
    {
      key: 'environment_id',
      label: 'Scope',
      render: (r) => {
        if (!r.environment_id) return 'Global';
        const env = environments.find(e => e.id === r.environment_id);
        return env ? env.name : r.environment_id.slice(0, 8) + '...';
      }
    },
    { key: 'enforcement', label: 'Enforcement' },
    { key: 'enabled', label: 'Status', render: (r) => r.enabled ? '✅ Enabled' : '⏸️ Disabled' },
    {
      key: 'rules',
      label: 'Rules',
      render: (r) => Array.isArray(r.rules) ? `${r.rules.length} rule${r.rules.length !== 1 ? 's' : ''}` : '0 rules'
    },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ]);

  function openCreateModal() {
    createOpen = true;
    createError = null;
  }

  function closeCreateModal() {
    createOpen = false;
    createError = null;
    // Reset form
    createForm = {
      name: '',
      environment_id: '',
      rules: '[]',
      enforcement: 'warn',
      enabled: true
    };
  }

  async function handleCreate() {
    // Validate required fields
    if (!createForm.name.trim()) {
      createError = 'Name is required';
      return;
    }

    if (!createForm.enforcement) {
      createError = 'Enforcement mode is required';
      return;
    }

    // Validate and parse rules JSON
    let parsedRules;
    try {
      parsedRules = JSON.parse(createForm.rules);
      if (!Array.isArray(parsedRules)) {
        createError = 'Rules must be a JSON array';
        return;
      }
    } catch (err) {
      createError = 'Rules must be valid JSON';
      return;
    }

    creating = true;
    createError = null;

    try {
      const payload = {
        name: createForm.name.trim(),
        rules: parsedRules,
        enforcement: createForm.enforcement,
        enabled: createForm.enabled
      };

      // Only include environment_id if it's not empty (for scoped policies)
      if (createForm.environment_id) {
        payload.environment_id = createForm.environment_id;
      }

      await api.createPolicy(payload);
      
      closeCreateModal();
      // Reload policies
      policies = await api.listPolicies();
    } catch (err) {
      createError = err.message || 'Failed to create policy';
    } finally {
      creating = false;
    }
  }
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>Policies</h1>
      <span class="count">{policies.length} policies</span>
    </div>
    <LoadingButton variant="primary" onclick={openCreateModal}>
      Create Policy
    </LoadingButton>
  </div>

  {#if loading}
    <p class="loading">Loading...</p>
  {:else if error}
    <p class="error">Error: {error}</p>
  {:else if policies.length === 0}
    <EmptyState
      icon="🛡️"
      title="No policies yet"
      message="Create your first deployment policy to enforce rules and controls"
      actionLabel="Create Policy"
      onAction={openCreateModal}
    />
  {:else}
    <Table {columns} data={policies} onRowClick={(row) => goto(`/policies/${row.id}`)} />
  {/if}
</div>

<Modal bind:open={createOpen} title="Create Policy" onClose={closeCreateModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleCreate(); }} class="create-form">
    <div class="form-field">
      <label for="policy-name">Name *</label>
      <Input
        id="policy-name"
        bind:value={createForm.name}
        placeholder="my-policy"
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="environment-id">Scope</label>
      <Select
        id="environment-id"
        bind:value={createForm.environment_id}
        options={environmentOptions}
        disabled={creating}
      />
      <span class="help-text">Leave as "Global policy" to apply to all environments</span>
    </div>

    <div class="form-field">
      <label for="enforcement">Enforcement Mode *</label>
      <Select
        id="enforcement"
        bind:value={createForm.enforcement}
        options={enforcementOptions}
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="rules">Rules (JSON Array) *</label>
      <Textarea
        id="rules"
        bind:value={createForm.rules}
        placeholder={'[{"type": "require_sbom"}]'}
        rows={8}
        required
        disabled={creating}
      />
      <span class="help-text">Enter policy rules as a JSON array</span>
    </div>

    <div class="form-field">
      <Checkbox
        id="enabled"
        bind:checked={createForm.enabled}
        label="Enabled"
        disabled={creating}
      />
    </div>

    {#if createError}
      <p class="error-message">{createError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton
        type="button"
        variant="secondary"
        onclick={closeCreateModal}
        disabled={creating}
      >
        Cancel
      </LoadingButton>
      <LoadingButton
        type="submit"
        variant="primary"
        loading={creating}
      >
        Create
      </LoadingButton>
    </div>
  </form>
</Modal>

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }
  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
  .loading, .error {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }
  .error {
    color: var(--error);
  }

  .create-form {
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
  .error-message {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0;
    padding: 0.5rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
  }
</style>
