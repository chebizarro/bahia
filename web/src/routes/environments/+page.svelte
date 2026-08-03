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
  import { EnvironmentIcon, ProtectedIcon } from '$lib/icons/domain-icons.js';
  import { environments, loading, loadEnvironments, workers, loadWorkers } from '$lib/stores';
  import { createEnvironment as createEnvironmentCommand } from '$lib/stores/public-controlplane.svelte.js';
  import { orgsState } from '$lib/stores/orgs.svelte.js';
  import { parseKeyValueLines } from '../ml/page-model.js';
  import { environmentFormSchema, parseRuntimeConfig, validateForm } from '$lib/validation/forms.js';
  import {
    DEPLOYMENT_UNIT_EXECUTION_OPTIONS,
    DEPLOYMENT_UNIT_OWNERSHIP_MODE,
    DEPLOYMENT_UNIT_RECONCILE_OPTIONS,
    DEPLOYMENT_UNIT_RUNTIME_TYPE,
    deploymentUnitForm,
    deploymentUnitWriteShape,
    validateDeploymentUnitForm
  } from '$lib/deployment-units.js';

  $effect(() => {
    void Promise.all([loadEnvironments(), loadWorkers()]);
  });

  // Create modal state
  let createOpen = $state(false);
  let creating = $state(false);
  let createError = $state(null);

  let createForm = $state({
    org_id: '',
    name: '',
    loom_worker_selector: '',
    runtime_config: '{}',
    pinned_worker: '',
    label_selector: '',
    rollout_from_labels: '',
    rollout_to_labels: '',
    deploy_strategy: 'rolling',
    protected: false,
    unit_enabled: false,
    unit: deploymentUnitForm()
  });

  const deployStrategyOptions = [
    { value: 'rolling', label: 'Rolling' },
    { value: 'blue-green', label: 'Blue-Green' },
    { value: 'canary', label: 'Canary' }
  ];

  let workerOptions = $derived([
    { value: '', label: 'Any eligible worker' },
    ...workers.map((worker) => ({ value: worker.pubkey, label: `${worker.name || worker.pubkey?.slice(0, 16) + '…'} (${String(worker.pubkey || '').slice(0, 12)}…)` }))
  ]);

  let organizationOptions = $derived.by(() => {
    const byId = new Map();
    for (const org of orgsState.orgs || []) {
      const id = String(org?.id || org?.org_id || '').trim();
      if (id) byId.set(id, { value: id, label: org.display_name || org.name || id });
    }
    for (const environment of environments) {
      const id = String(environment?.org_id || environment?.owner_org_id || '').trim();
      if (id && !byId.has(id)) byId.set(id, { value: id, label: id });
    }
    return Array.from(byId.values());
  });

  const deployStrategyApiMap = {
    rolling: 'replace',
    'blue-green': 'blue_green',
    canary: 'canary'
  };

  let columns = $derived([
    { key: 'name', label: 'Name', icon: EnvironmentIcon, text: (r) => r.name || '-' },
    { key: 'deploy_strategy', label: 'Strategy' },
    {
      key: 'protected',
      label: 'Protected',
      icon: (r) => (r.protected ? ProtectedIcon : null),
      text: (r) => (r.protected ? 'Protected' : 'No')
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
      org_id: '',
      name: '',
      loom_worker_selector: '',
      runtime_config: '{}',
      pinned_worker: '',
      label_selector: '',
      rollout_from_labels: '',
      rollout_to_labels: '',
      deploy_strategy: 'rolling',
      protected: false,
      unit_enabled: false,
      unit: deploymentUnitForm()
    };
  }

  async function handleCreate() {
    const validationResult = validateForm(environmentFormSchema, createForm);
    if (!validationResult.success) {
      createError = validationResult.error;
      return;
    }

    if (!createForm.org_id.trim()) {
      createError = 'Organization is required';
      return;
    }

    let parsedRuntimeConfig;
    let deploymentUnits;
    let targeting;
    try {
      parsedRuntimeConfig = parseRuntimeConfig(createForm.runtime_config);
      const workerPolicy = buildWorkerPolicy(createForm);
      if (Object.keys(workerPolicy).length > 0) {
        parsedRuntimeConfig.worker_policy = {
          ...(parsedRuntimeConfig.worker_policy && typeof parsedRuntimeConfig.worker_policy === 'object' ? parsedRuntimeConfig.worker_policy : {}),
          ...workerPolicy
        };
      }
      if (createForm.unit_enabled) {
        const unitValidation = validateDeploymentUnitForm(createForm.unit, { protectedEnvironment: createForm.protected });
        if (!unitValidation.success) throw new Error(unitValidation.error);
        if (parsedRuntimeConfig.type && parsedRuntimeConfig.type !== DEPLOYMENT_UNIT_RUNTIME_TYPE) {
          throw new Error(`Environment runtime "${parsedRuntimeConfig.type}" conflicts with deployment unit runtime "compose".`);
        }
        const unit = deploymentUnitWriteShape({
          ...unitValidation.data,
          runtime_type: DEPLOYMENT_UNIT_RUNTIME_TYPE,
          ownership_mode: DEPLOYMENT_UNIT_OWNERSHIP_MODE,
          runtime_config: { execution_mode: unitValidation.data.execution_mode }
        });
        deploymentUnits = [unit];
        targeting = {
          default_unit_key: unit.key,
          secret_scope_mode: 'unit',
          default_reconcile_mode: unit.reconcile_mode
        };
      }
    } catch (err) {
      createError = err.message || 'Invalid worker placement policy';
      return;
    }

    creating = true;
    createError = null;

    try {
      await createEnvironmentCommand({
        org_id: createForm.org_id.trim(),
        name: createForm.name.trim(),
        loom_worker_selector: createForm.loom_worker_selector.trim(),
        runtime_config: parsedRuntimeConfig,
        ...(targeting ? { targeting, reconcile_mode: targeting.default_reconcile_mode } : {}),
        ...(deploymentUnits ? { deployment_units: deploymentUnits } : {}),
        deploy_strategy: deployStrategyApiMap[createForm.deploy_strategy] || createForm.deploy_strategy,
        protected: createForm.protected
      });
      
      closeCreateModal();
      await loadEnvironments();
    } catch (err) {
      createError = err.message || 'Failed to create environment';
    } finally {
      creating = false;
    }
  }

  function buildWorkerPolicy(form) {
    const policy = {};
    if (form.pinned_worker) policy.pinned_worker = form.pinned_worker;
    const labelSelector = parseKeyValueLines(form.label_selector, { fieldName: 'Label selector' });
    if (Object.keys(labelSelector).length > 0) policy.label_selector = labelSelector;
    const fromLabels = parseKeyValueLines(form.rollout_from_labels, { fieldName: 'Rollout source labels' });
    const toLabels = parseKeyValueLines(form.rollout_to_labels, { fieldName: 'Rollout target labels' });
    if (Object.keys(fromLabels).length > 0 || Object.keys(toLabels).length > 0) {
      policy.rollout = {};
      if (Object.keys(fromLabels).length > 0) policy.rollout.from_labels = fromLabels;
      if (Object.keys(toLabels).length > 0) policy.rollout.to_labels = toLabels;
    }
    return policy;
  }
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>
        <EnvironmentIcon size={28} strokeWidth={1.75} ariaHidden="true" />
        Environments
      </h1>
      <span class="count">{environments.length} environments</span>
    </div>
    <LoadingButton variant="primary" onclick={openCreateModal}>
      Create Environment
    </LoadingButton>
  </div>

  {#if loading.environments}
    <p class="loading">Loading...</p>
  {:else if environments.length === 0}
    <EmptyState
      iconComponent={EnvironmentIcon}
      title="No environments yet"
      message="Create your first environment to define deployment targets"
      actionLabel="Create Environment"
      onAction={openCreateModal}
    />
  {:else}
    <Table {columns} data={environments} onRowClick={(row) => goto(`/environments/${row.id}`)} />
  {/if}
</div>

<Modal bind:open={createOpen} title="Create Environment" size="lg" onClose={closeCreateModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleCreate(); }} class="create-form">
    <div class="form-field">
      <label for="env-org">Organization *</label>
      {#if organizationOptions.length > 0}
        <Select
          id="env-org"
          bind:value={createForm.org_id}
          options={organizationOptions}
          placeholder="Select organization"
          required
          disabled={creating}
        />
      {:else}
        <Input
          id="env-org"
          bind:value={createForm.org_id}
          placeholder="Organization UUID"
          required
          disabled={creating}
        />
      {/if}
      <span class="field-hint">Environment mutations are authorized against this organization.</span>
    </div>

    <div class="form-field">
      <label for="env-name">Name *</label>
      <Input
        id="env-name"
        bind:value={createForm.name}
        placeholder="production"
        required
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="worker-selector">Loom Worker Selector</label>
      <Input
        id="worker-selector"
        bind:value={createForm.loom_worker_selector}
        placeholder="region=us-west"
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="create-pinned-worker">Pin to worker (optional)</label>
      <Select
        id="create-pinned-worker"
        bind:value={createForm.pinned_worker}
        options={workerOptions}
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="create-label-selector">Worker label selector</label>
      <Textarea
        id="create-label-selector"
        bind:value={createForm.label_selector}
        placeholder={'role=inference\ntrack=stable'}
        rows={3}
        disabled={creating}
      />
    </div>

    <div class="placement-grid">
      <div class="form-field">
        <label for="create-rollout-from">Rollout from labels</label>
        <Textarea
          id="create-rollout-from"
          bind:value={createForm.rollout_from_labels}
          placeholder="track=canary"
          rows={2}
          disabled={creating}
        />
      </div>
      <div class="form-field">
        <label for="create-rollout-to">Rollout target labels</label>
        <Textarea
          id="create-rollout-to"
          bind:value={createForm.rollout_to_labels}
          placeholder="track=stable"
          rows={2}
          disabled={creating}
        />
      </div>
    </div>

    <div class="form-field">
      <label for="runtime-config">Runtime Config (JSON)</label>
      <Textarea
        id="runtime-config"
        bind:value={createForm.runtime_config}
        placeholder={'{}'}
        rows={6}
        disabled={creating}
      />
    </div>

    <div class="form-field">
      <label for="deploy-strategy">Deploy Strategy *</label>
      <Select
        id="deploy-strategy"
        bind:value={createForm.deploy_strategy}
        options={deployStrategyOptions}
        required
        disabled={creating}
      />
    </div>

    <div class="form-field target-toggle">
      <Checkbox
        id="create-explicit-unit"
        bind:checked={createForm.unit_enabled}
        disabled={creating}
        label="Create an explicit Bahia-managed Compose deployment unit"
      />
      <span class="field-hint">Use this for a named direct-runtime target such as Max. The endpoint remains a server-managed alias.</span>
    </div>

    {#if createForm.unit_enabled}
      <fieldset class="unit-fields">
        <legend>Compose deployment unit</legend>
        <div class="placement-grid">
          <div class="form-field">
            <label for="create-unit-key">Unit key *</label>
            <Input id="create-unit-key" bind:value={createForm.unit.key} placeholder="max" required disabled={creating} />
            <span class="field-hint">Stable target key and Compose project boundary.</span>
          </div>
          <div class="form-field">
            <label for="create-unit-display">Display name</label>
            <Input id="create-unit-display" bind:value={createForm.unit.display_name} placeholder="Max Compose" disabled={creating} />
          </div>
        </div>

        <div class="placement-grid">
          <div class="form-field">
            <label for="create-unit-runtime">Runtime type</label>
            <Input id="create-unit-runtime" value={DEPLOYMENT_UNIT_RUNTIME_TYPE} disabled />
          </div>
          <div class="form-field">
            <label for="create-unit-ownership">Ownership mode</label>
            <Input id="create-unit-ownership" value={DEPLOYMENT_UNIT_OWNERSHIP_MODE} disabled />
          </div>
        </div>

        <div class="form-field">
          <label for="create-unit-endpoint">Endpoint alias *</label>
          <Input id="create-unit-endpoint" bind:value={createForm.unit.endpoint_ref} placeholder="max" required disabled={creating} />
          <span class="field-hint">Server-managed alias only. Docker URLs, certificates, and credentials are never entered here.</span>
        </div>

        <div class="form-field">
          <label for="create-unit-dir">Compose directory *</label>
          <Input id="create-unit-dir" bind:value={createForm.unit.compose_dir} placeholder="/srv/bahia/compose/gastown" required disabled={creating} />
          <span class="field-hint">Absolute directory dedicated to Bahia's rendered full Compose project.</span>
        </div>

        <div class="placement-grid">
          <div class="form-field">
            <label for="create-unit-reconcile">Reconcile mode *</label>
            <Select
              id="create-unit-reconcile"
              bind:value={createForm.unit.reconcile_mode}
              options={DEPLOYMENT_UNIT_RECONCILE_OPTIONS.map((option) => ({
                ...option,
                disabled: createForm.protected && option.value === 'auto_apply'
              }))}
              required
              disabled={creating}
            />
          </div>
          <div class="form-field">
            <label for="create-unit-execution">Compose execution *</label>
            <Select id="create-unit-execution" bind:value={createForm.unit.execution_mode} options={DEPLOYMENT_UNIT_EXECUTION_OPTIONS} required disabled={creating} />
          </div>
        </div>
      </fieldset>
    {/if}

    <div class="form-field">
      <Checkbox
        id="protected"
        bind:checked={createForm.protected}
        disabled={creating}
        label="Protected (requires approval for deployments)"
      />
    </div>

    {#if createError}
      <p class="error">{createError}</p>
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

  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
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
  .placement-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 1rem;
  }
  .field-hint {
    color: var(--text-muted);
    font-size: 0.75rem;
    line-height: 1.4;
  }
  .unit-fields {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 1rem;
  }
  .unit-fields legend {
    color: var(--text-primary);
    font-weight: 600;
    padding: 0 0.4rem;
  }
  .target-toggle {
    padding: 0.75rem;
    border-radius: 6px;
    background: var(--hover-bg);
  }
  .form-actions {
    display: flex;
    gap: 0.75rem;
    justify-content: flex-end;
    margin-top: 0.5rem;
  }
  .error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0;
    padding: 0.5rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
  }
</style>
