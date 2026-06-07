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
  import PolicyRuleBuilder from '$lib/components/PolicyRuleBuilder.svelte';
  import { policies, environments, loadPolicies, loadEnvironments } from '$lib/stores';
  import { createPolicy as createPolicyCommand } from '$lib/stores/public-controlplane.svelte.js';
  import { policyFormSchema, validateForm } from '$lib/validation/forms.js';
  import { CloseIcon, EnvironmentIcon, PolicyIcon, SuccessIcon } from '$lib/icons/domain-icons.js';

  let loading = $state(true);
  let error = $state(null);
  let enforcementFilter = $state('all');
  let enabledFilter = $state('all');

  // Create modal state
  let createOpen = $state(false);
  let creating = $state(false);
  let createError = $state(null);
  let useVisualBuilder = $state(true); // Toggle between visual builder and JSON
  let visualRules = $state([]); // Rules from visual builder

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
      await Promise.all([loadPolicies(), loadEnvironments()]);
    } catch (err) {
      console.error('Failed to load data:', err);
      error = err.message;
    } finally {
      loading = false;
    }
  }

  let filterEnforcementOptions = [
    { value: 'all', label: 'All enforcement levels' },
    ...enforcementOptions
  ];

  let filterEnabledOptions = [
    { value: 'all', label: 'All statuses' },
    { value: 'enabled', label: 'Enabled' },
    { value: 'disabled', label: 'Disabled' }
  ];

  let environmentOptions = $derived([
    { value: '', label: 'Global policy' },
    ...environments.map(env => ({ value: env.id, label: env.name }))
  ]);

  function getRuleCount(policy) {
    if (typeof policy?.rule_count === 'number') {
      return policy.rule_count;
    }
    return Array.isArray(policy?.rules) ? policy.rules.length : 0;
  }

  let filteredPolicies = $derived(
    policies.filter((policy) => {
      if (enforcementFilter !== 'all' && policy.enforcement !== enforcementFilter) {
        return false;
      }

      if (enabledFilter === 'enabled' && !policy.enabled) {
        return false;
      }

      if (enabledFilter === 'disabled' && policy.enabled) {
        return false;
      }

      return true;
    })
  );

  let columns = $derived([
    { key: 'name', label: 'Name', icon: PolicyIcon, text: (r) => r.name || '-' },
    {
      key: 'environment_id',
      label: 'Scope',
      icon: EnvironmentIcon,
      text: (r) => {
        if (!r.environment_id) return 'Global';
        const env = environments.find(e => e.id === r.environment_id);
        return env ? env.name : r.environment_id.slice(0, 8) + '...';
      }
    },
    { key: 'enforcement', label: 'Enforcement' },
    {
      key: 'enabled',
      label: 'Status',
      icon: (r) => r.enabled ? SuccessIcon : CloseIcon,
      text: (r) => r.enabled ? 'Enabled' : 'Disabled'
    },
    {
      key: 'rules',
      label: 'Rules',
      render: (r) => {
        const count = getRuleCount(r);
        return `${count} rule${count !== 1 ? 's' : ''}`;
      }
    },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` },
    {
      key: 'actions',
      label: 'Actions',
      render: (r) => {
        const policyPath = `/policies/${encodeURIComponent(r.id)}`;
        return `<div class="table-actions"><a href="${policyPath}">Edit</a><a href="${policyPath}">Delete</a></div>`;
      }
    }
  ]);

  function openCreateModal() {
    createOpen = true;
    createError = null;
  }

  function closeCreateModal() {
    createOpen = false;
    createError = null;
    visualRules = [];
    useVisualBuilder = true;
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
    // Get rules from visual builder or JSON
    let parsedRules;
    if (useVisualBuilder) {
      parsedRules = visualRules;
      if (parsedRules.length === 0) {
        createError = 'Please add at least one rule';
        return;
      }
    } else {
      const validationResult = validateForm(policyFormSchema, createForm);
      if (!validationResult.success) {
        createError = validationResult.error;
        return;
      }
      try {
        parsedRules = JSON.parse(createForm.rules);
      } catch {
        createError = 'Invalid JSON in rules';
        return;
      }
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

      await createPolicyCommand(payload);
      
      closeCreateModal();
      await loadPolicies();
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
      <span class="count">{filteredPolicies.length} of {policies.length} policies</span>
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
      iconComponent={PolicyIcon}
      title="No policies yet"
      message="Create your first deployment policy to enforce rules and controls"
      actionLabel="Create Policy"
      onAction={openCreateModal}
    />
  {:else}
    <div class="filters" aria-label="Policy filters">
      <div class="filter-field">
        <label for="enforcement-filter">Enforcement</label>
        <Select
          id="enforcement-filter"
          bind:value={enforcementFilter}
          options={filterEnforcementOptions}
        />
      </div>
      <div class="filter-field">
        <label for="enabled-filter">Status</label>
        <Select
          id="enabled-filter"
          bind:value={enabledFilter}
          options={filterEnabledOptions}
        />
      </div>
    </div>

    <Table {columns} data={filteredPolicies} onRowClick={(row) => goto(`/policies/${row.id}`)} />
  {/if}
</div>

<Modal bind:open={createOpen} title="Create Policy" titleIcon={PolicyIcon} onClose={closeCreateModal}>
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

    <fieldset class="form-field rules-field">
      <div class="rules-header">
        <legend class="field-heading">Rules *</legend>
        <div class="builder-toggle">
          <button
            type="button"
            class="toggle-btn"
            class:active={useVisualBuilder}
            onclick={() => useVisualBuilder = true}
            disabled={creating}
          >
            Visual Builder
          </button>
          <button
            type="button"
            class="toggle-btn"
            class:active={!useVisualBuilder}
            onclick={() => {
              useVisualBuilder = false;
              // Sync visual rules to JSON when switching
              createForm.rules = JSON.stringify(visualRules, null, 2);
            }}
            disabled={creating}
          >
            JSON Editor
          </button>
        </div>
      </div>
      
      {#if useVisualBuilder}
        <PolicyRuleBuilder bind:rules={visualRules} disabled={creating} />
      {:else}
        <Textarea
          id="rules"
          bind:value={createForm.rules}
          placeholder={'[{"type": "require_sbom"}]'}
          rows={8}
          required
          disabled={creating}
        />
        <span class="help-text">Enter policy rules as a JSON array</span>
      {/if}
    </fieldset>

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

  .filters {
    display: flex;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .filter-field {
    min-width: 220px;
  }
  .filter-field label {
    display: block;
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-bottom: 0.25rem;
  }

  :global(.table-actions) {
    display: inline-flex;
    gap: 0.75rem;
  }
  :global(.table-actions a) {
    color: var(--primary);
    text-decoration: none;
    font-size: 0.875rem;
  }
  :global(.table-actions a:hover) {
    text-decoration: underline;
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
  .form-field label,
  .field-heading {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }
  .rules-field {
    border: 0;
    padding: 0;
    margin: 0;
    min-inline-size: 0;
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

  /* Rules builder toggle */
  .rules-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.5rem;
  }

  .builder-toggle {
    display: flex;
    gap: 0.25rem;
    background: var(--hover-bg, rgba(255,255,255,0.05));
    border-radius: 6px;
    padding: 0.125rem;
  }

  .toggle-btn {
    padding: 0.375rem 0.75rem;
    font-size: 0.75rem;
    background: transparent;
    border: none;
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
    transition: background 0.15s, color 0.15s;
  }

  .toggle-btn:hover:not(:disabled) {
    color: var(--text-primary);
  }

  .toggle-btn.active {
    background: var(--primary);
    color: white;
  }

  .toggle-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
