<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Select from '$lib/components/Select.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import {
    policies as policyStore,
    environments as environmentStore,
    loadPolicies,
    loadEnvironments
  } from '$lib/stores';
  import { updatePolicy, deletePolicy, evaluatePolicy } from '$lib/stores/public-controlplane.svelte.js';
  import { policyFormSchema, validateForm } from '$lib/validation/forms.js';

  let policy = $state(null);
  let environments = $state([]);
  let loading = $state(true);
  let error = $state(null);

  let policyId = $derived(page.params.id);

  // Edit modal state
  let editOpen = $state(false);
  let editing = $state(false);
  let editError = $state(null);
  let editForm = $state({
    name: '',
    environment_id: '',
    rules: '[]',
    enforcement: 'warn',
    enabled: true
  });
  let editRulesMode = $state('visual');
  let visualRules = $state([]);

  // Evaluate tool state
  let evalForm = $state({
    environment_id: '',
    artifact_id: ''
  });
  let evaluating = $state(false);
  let evaluation = $state(null);
  let evaluationError = $state(null);

  // Delete modal state
  let deleteOpen = $state(false);
  let deleting = $state(false);
  let deleteError = $state(null);

  const enforcementOptions = [
    { value: 'warn', label: 'Warn' },
    { value: 'block', label: 'Block' }
  ];

  const ruleTypeOptions = [
    { value: 'require_signature', label: 'Require signature' },
    { value: 'require_sbom', label: 'Require SBOM' },
    { value: 'max_critical_vulns', label: 'Max critical vulns' },
    { value: 'max_high_vulns', label: 'Max high vulns' },
    { value: 'require_scan_status', label: 'Require scan status' },
    { value: 'block_package', label: 'Block package' },
    { value: 'require_approval', label: 'Require approval' }
  ];

  $effect(() => {
    const id = policyId;
    if (!id) return;
    void loadPolicy(id);
  });

  async function loadPolicy(id) {
    loading = true;
    error = null;
    policy = null;

    try {
      await Promise.all([loadPolicies(), loadEnvironments()]);
      policy = policyStore.find((candidate) => candidate.id === id) || null;
      if (!policy) {
        throw new Error('Policy not found');
      }
      environments = [...environmentStore];
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  let environmentOptions = $derived([
    { value: '', label: 'Global policy' },
    ...environments.map(env => ({ value: env.id, label: env.name }))
  ]);

  let environmentName = $derived(policy?.environment_id 
    ? (environments.find(e => e.id === policy.environment_id)?.name || policy.environment_id)
    : 'Global');

  let formattedRules = $derived(policy?.rules 
    ? JSON.stringify(policy.rules, null, 2)
    : '[]');

  function openEditModal() {
    if (!policy) return;

    editForm = {
      name: policy.name,
      environment_id: policy.environment_id || '',
      rules: JSON.stringify(policy.rules, null, 2),
      enforcement: policy.enforcement || 'warn',
      enabled: policy.enabled !== undefined ? policy.enabled : true
    };
    visualRules = (policy.rules || []).map((rule) => ({
      type: rule?.type || 'require_sbom',
      params: JSON.stringify(rule?.params || {}, null, 2)
    }));
    editRulesMode = 'visual';
    editError = null;
    editOpen = true;
  }

  function closeEditModal() {
    editOpen = false;
    editError = null;
  }

  function addVisualRule() {
    visualRules = [...visualRules, { type: 'require_sbom', params: '{}' }];
  }

  function removeVisualRule(index) {
    visualRules = visualRules.filter((_, i) => i !== index);
  }

  function syncJsonFromVisual() {
    const rules = visualRules.map((rule) => {
      let params = {};
      const raw = (rule.params || '').trim();
      if (raw) {
        params = JSON.parse(raw);
        if (params === null || Array.isArray(params) || typeof params !== 'object') {
          throw new Error('Rule params must be a JSON object');
        }
      }

      const entry = { type: rule.type };
      if (Object.keys(params).length > 0) {
        entry.params = params;
      }
      return entry;
    });

    editForm.rules = JSON.stringify(rules, null, 2);
    return rules;
  }

  async function handleEdit() {
    // Validate and parse rules JSON
    let parsedRules;
    try {
      if (editRulesMode === 'visual') {
        parsedRules = syncJsonFromVisual();
      } else {
        parsedRules = JSON.parse(editForm.rules);
      }
      if (!Array.isArray(parsedRules)) {
        editError = 'Rules must be a JSON array';
        return;
      }
    } catch (err) {
      editError = err?.message || 'Rules must be valid JSON';
      return;
    }

    const validationResult = validateForm(policyFormSchema, {
      ...editForm,
      rules: JSON.stringify(parsedRules)
    });
    if (!validationResult.success) {
      editError = validationResult.error;
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

      await updatePolicy(policyId, payload);
      policy = { ...policy, ...payload };
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

  async function runEvaluation() {
    if (!evalForm.environment_id) {
      evaluationError = 'Environment is required';
      return;
    }
    if (!evalForm.artifact_id.trim()) {
      evaluationError = 'Artifact ID is required';
      return;
    }

    evaluating = true;
    evaluationError = null;
    evaluation = null;

    try {
      evaluation = await evaluatePolicy({
        environment_id: evalForm.environment_id,
        artifact_id: evalForm.artifact_id.trim()
      });
    } catch (err) {
      evaluationError = err.message || 'Failed to evaluate policy';
    } finally {
      evaluating = false;
    }
  }

  let currentPolicyEvaluation = $derived(
    evaluation?.results?.find((r) => r.policy_id === policy?.id) || null
  );

  async function togglePolicyEnabled() {
    if (!policy) return;
    const nextEnabled = !policy.enabled;

    try {
      await updatePolicy(policyId, {
        name: policy.name,
        rules: policy.rules || [],
        enforcement: policy.enforcement || 'warn',
        enabled: nextEnabled,
        environment_id: policy.environment_id || null
      });
      policy = { ...policy, enabled: nextEnabled };
    } catch (err) {
      error = err.message || 'Failed to update policy status';
    }
  }

  function closeDeleteModal() {
    deleteOpen = false;
    deleteError = null;
  }

  async function handleDelete() {
    deleting = true;
    deleteError = null;

    try {
      await deletePolicy(policyId);
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
        <LoadingButton variant="secondary" onclick={togglePolicyEnabled}>
          {policy.enabled ? 'Disable' : 'Enable'}
        </LoadingButton>
        <LoadingButton variant="secondary" onclick={openEditModal}>
          Edit
        </LoadingButton>
        <LoadingButton variant="danger" onclick={openDeleteModal}>
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

    <section>
      <h2>Policy Evaluation Test</h2>
      <div class="eval-grid">
        <div class="form-field">
          <label for="eval-environment">Environment</label>
          <Select id="eval-environment" bind:value={evalForm.environment_id} options={environmentOptions} disabled={evaluating} />
        </div>
        <div class="form-field">
          <label for="eval-artifact">Artifact ID</label>
          <Input id="eval-artifact" bind:value={evalForm.artifact_id} placeholder="artifact UUID" disabled={evaluating} />
        </div>
      </div>
      <div class="eval-actions">
        <LoadingButton variant="primary" onclick={runEvaluation} loading={evaluating}>Run Evaluation</LoadingButton>
      </div>
      {#if evaluationError}
        <p class="error-message">{evaluationError}</p>
      {/if}
      {#if evaluation && currentPolicyEvaluation}
        <div class="eval-result">
          <p><strong>Result:</strong> {currentPolicyEvaluation.passed ? 'Pass' : 'Fail'} ({currentPolicyEvaluation.enforcement || 'warn'})</p>
          {#if currentPolicyEvaluation.violations?.length > 0}
            <ul>
              {#each currentPolicyEvaluation.violations as violation}
                <li><code>{violation.rule}</code>: {violation.message}</li>
              {/each}
            </ul>
          {/if}
        </div>
      {:else if evaluation}
        <p class="help-text">This policy did not apply to the selected environment.</p>
      {/if}
    </section>
  {/if}
</div>

<!-- Edit Modal -->
<Modal bind:open={editOpen} title="Edit Policy" onClose={closeEditModal}>
  <form onsubmit={(event) => { event.preventDefault(); handleEdit(); }} class="edit-form">
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
      <label for="edit-rules-mode">Rule Editor</label>
      <Select
        id="edit-rules-mode"
        bind:value={editRulesMode}
        options={[{ value: 'visual', label: 'Visual editor' }, { value: 'json', label: 'JSON editor' }]}
        disabled={editing}
      />
    </div>

    {#if editRulesMode === 'json'}
      <div class="form-field">
        <label for="edit-rules">Rules (JSON Array) *</label>
        <Textarea
          id="edit-rules"
          bind:value={editForm.rules}
          placeholder={'[{"type": "require_sbom"}]'}
          rows={10}
          required
          disabled={editing}
        />
        <span class="help-text">Enter policy rules as a JSON array</span>
      </div>
    {:else}
      <div class="form-field">
        <span class="field-label">Rules *</span>
        {#if visualRules.length === 0}
          <p class="help-text">No rules configured yet.</p>
        {/if}
        {#each visualRules as rule, index}
          <div class="visual-rule-row">
            <select
              id={`rule-type-${index}`}
              value={rule.type}
              disabled={editing}
              onchange={(e) => {
                visualRules[index].type = e.currentTarget.value;
                visualRules = [...visualRules];
              }}
            >
              {#each ruleTypeOptions as option}
                <option value={option.value}>{option.label}</option>
              {/each}
            </select>
            <textarea
              id={`rule-params-${index}`}
              rows="3"
              placeholder={'{}'}
              disabled={editing}
              value={rule.params}
              oninput={(e) => {
                visualRules[index].params = e.currentTarget.value;
                visualRules = [...visualRules];
              }}
            ></textarea>
            <LoadingButton type="button" variant="danger" onclick={() => removeVisualRule(index)} disabled={editing}>
              Remove
            </LoadingButton>
          </div>
        {/each}
        <LoadingButton type="button" variant="secondary" onclick={addVisualRule} disabled={editing}>
          Add Rule
        </LoadingButton>
        <span class="help-text">Use params JSON object for rule-specific configuration (for example: <code>{'{"max": 0}'}</code>).</span>
      </div>
    {/if}

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
        onclick={closeEditModal}
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
  onConfirm={handleDelete}
  onCancel={closeDeleteModal}
  onClose={closeDeleteModal}
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

  .eval-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .eval-actions {
    margin-bottom: 1rem;
  }
  .eval-result {
    padding: 0.75rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--hover-bg);
  }
  .eval-result p {
    margin: 0 0 0.5rem 0;
  }
  .eval-result ul {
    margin: 0;
    padding-left: 1.25rem;
  }

  .visual-rule-row {
    display: grid;
    grid-template-columns: minmax(180px, 220px) 1fr auto;
    gap: 0.75rem;
    align-items: start;
    margin-bottom: 0.75rem;
  }
  .visual-rule-row select,
  .visual-rule-row textarea {
    width: 100%;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--input-bg);
    color: var(--text-primary);
    padding: 0.5rem;
    font-size: 0.875rem;
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
