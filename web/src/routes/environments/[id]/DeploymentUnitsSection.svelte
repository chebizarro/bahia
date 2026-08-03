<script>
  import Modal from '$lib/components/Modal.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { authState } from '$lib/stores/auth.js';
  import { updateEnvironment } from '$lib/stores/public-controlplane.svelte.js';
  import {
    DEPLOYMENT_UNIT_EXECUTION_OPTIONS,
    DEPLOYMENT_UNIT_OWNERSHIP_MODE,
    DEPLOYMENT_UNIT_RECONCILE_OPTIONS,
    DEPLOYMENT_UNIT_RUNTIME_TYPE,
    buildDeploymentUnitSetUpdate,
    deploymentUnitErrorMessage,
    deploymentUnitForm,
    deploymentUnitsForEnvironment,
    explicitDeploymentUnits
  } from '$lib/deployment-units.js';

  let { environment, onRefresh = null } = $props();

  let editorOpen = $state(false);
  let confirmOpen = $state(false);
  let saving = $state(false);
  let formError = $state('');
  let originalKey = $state('');
  let baseRevision = $state('');
  let pendingPayload = $state(null);
  let form = $state(deploymentUnitForm());

  let units = $derived(deploymentUnitsForEnvironment(environment));
  let explicitUnits = $derived(explicitDeploymentUnits(environment));
  let currentRevision = $derived(String(environment?.updated_at || environment?.updatedAt || ''));
  let draftStale = $derived(Boolean(editorOpen && baseRevision && currentRevision && baseRevision !== currentRevision));
  let canMutate = $derived(authState.status === 'authenticated');
  let unsupportedSet = $derived(explicitUnits.some((unit) =>
    unit.runtime_type !== DEPLOYMENT_UNIT_RUNTIME_TYPE || unit.ownership_mode !== DEPLOYMENT_UNIT_OWNERSHIP_MODE
  ));

  function isDefault(unit) {
    return unit?.key === environment?.targeting?.default_unit_key;
  }

  function executionMode(unit) {
    return unit?.runtime_config?.execution_mode || 'sdk';
  }

  function unitEditable(unit) {
    return unit?.implicit !== true
      && unit?.runtime_type === DEPLOYMENT_UNIT_RUNTIME_TYPE
      && unit?.ownership_mode === DEPLOYMENT_UNIT_OWNERSHIP_MODE;
  }

  function openCreate() {
    originalKey = '';
    form = deploymentUnitForm({
      reconcile_mode: environment?.protected ? 'approval_required' : (environment?.targeting?.default_reconcile_mode || 'approval_required')
    });
    baseRevision = currentRevision;
    pendingPayload = null;
    formError = '';
    editorOpen = true;
  }

  function openEdit(unit) {
    originalKey = String(unit?.key || '');
    form = deploymentUnitForm(unit);
    baseRevision = currentRevision;
    pendingPayload = null;
    formError = '';
    editorOpen = true;
  }

  function closeEditor() {
    if (saving) return;
    editorOpen = false;
    confirmOpen = false;
    pendingPayload = null;
    formError = '';
  }

  async function prepareSave() {
    if (draftStale) {
      formError = 'This environment changed while you were editing. Reload the latest target set and review your changes.';
      return;
    }
    try {
      pendingPayload = buildDeploymentUnitSetUpdate(environment, { originalKey, form });
      if (environment?.protected) {
        editorOpen = false;
        confirmOpen = true;
        return;
      }
      await publishPending();
    } catch (error) {
      formError = deploymentUnitErrorMessage(error);
    }
  }

  async function publishPending() {
    if (!pendingPayload) return;
    if (draftStale || pendingPayload.expected_updated_at !== currentRevision) {
      confirmOpen = false;
      pendingPayload = null;
      formError = 'This environment changed while you were editing. Reload the latest target set and review your changes.';
      return;
    }

    saving = true;
    formError = '';
    try {
      await updateEnvironment(environment.id, pendingPayload);
      confirmOpen = false;
      editorOpen = false;
      pendingPayload = null;
      await onRefresh?.();
    } catch (error) {
      confirmOpen = false;
      editorOpen = true;
      formError = deploymentUnitErrorMessage(error);
      if (error?.code === -32009) await onRefresh?.();
    } finally {
      saving = false;
    }
  }
</script>

<section class="deployment-units" aria-labelledby="deployment-units-title">
  <div class="section-header">
    <div>
      <h2 id="deployment-units-title">Deployment Units ({units.length})</h2>
      <p class="section-copy">Explicit units are Bahia-owned runtime boundaries. Endpoint aliases are shown; Docker hosts, certificates, and credentials remain server-side.</p>
    </div>
    <LoadingButton
      variant="secondary"
      onclick={openCreate}
      disabled={!canMutate || unsupportedSet || !currentRevision}
    >
      Create Compose Unit
    </LoadingButton>
  </div>

  {#if !canMutate}
    <p class="notice">Sign in with an authorized Nostr signer to create or edit deployment units.</p>
  {:else if !currentRevision}
    <p class="notice error-notice">The environment revision is unavailable. Refresh before changing deployment units.</p>
  {:else if unsupportedSet}
    <p class="notice error-notice">This environment contains a non-Compose or non-Bahia-managed explicit unit. The Compose editor is read-only so it cannot replace that mixed target set.</p>
  {/if}

  {#if environment?.protected}
    <p class="notice protected-notice">Protected environment: target changes require confirmation, and automatic reconciliation cannot be enabled.</p>
  {/if}

  {#if units.length === 0}
    <div class="empty-targets">
      <strong>No deployment units projected</strong>
      <span>Create an explicit Bahia-managed Compose target before direct-runtime deployment.</span>
    </div>
  {:else}
    <div class="unit-list">
      {#each units as unit (unit.id || unit.key)}
        <article class="unit-card" class:implicit={unit.implicit === true}>
          <div class="unit-heading">
            <div>
              <h3>{unit.display_name || unit.key || 'Unnamed unit'}</h3>
              <code>{unit.key || 'missing-key'}</code>
            </div>
            <div class="badges">
              {#if isDefault(unit)}<span class="badge">Default</span>{/if}
              {#if unit.implicit === true}<span class="badge muted">Implicit default</span>{/if}
            </div>
          </div>
          <dl>
            <div><dt>Runtime</dt><dd>{unit.runtime_type || 'Missing'}</dd></div>
            <div><dt>Endpoint alias</dt><dd><code>{unit.endpoint_ref || 'Missing'}</code></dd></div>
            <div><dt>Compose directory</dt><dd><code>{unit.compose_dir || 'Missing'}</code></dd></div>
            <div><dt>Ownership</dt><dd>{unit.ownership_mode || 'Missing'}</dd></div>
            <div><dt>Reconcile</dt><dd>{unit.reconcile_mode || 'Missing'}</dd></div>
            <div><dt>Execution</dt><dd>{executionMode(unit)}</dd></div>
          </dl>
          {#if unit.implicit === true}
            <p class="unit-note">Read-only resolved default. Deploy/apply does not persist an ID for this target.</p>
          {:else if unitEditable(unit)}
            <LoadingButton variant="secondary" onclick={() => openEdit(unit)} disabled={!canMutate || unsupportedSet}>
              Edit Unit
            </LoadingButton>
          {:else}
            <p class="unit-note">This unit is outside the Bahia-managed Compose editor contract.</p>
          {/if}
        </article>
      {/each}
    </div>
  {/if}
</section>

<Modal
  bind:open={editorOpen}
  title={originalKey ? 'Edit Deployment Unit' : 'Create Deployment Unit'}
  size="lg"
  onClose={closeEditor}
>
  <form onsubmit={(event) => { event.preventDefault(); prepareSave(); }} class="unit-form">
    <p class="form-intro">Bahia renders the full Compose project into this dedicated server directory. Enter only a server-managed endpoint alias, never a Docker URL or credential.</p>

    <div class="field-grid">
      <div class="form-field">
        <label for="unit-key">Unit key *</label>
        <Input id="unit-key" bind:value={form.key} placeholder="max" required disabled={saving} />
        <span>Stable targeting key and Compose project boundary.</span>
      </div>
      <div class="form-field">
        <label for="unit-display-name">Display name</label>
        <Input id="unit-display-name" bind:value={form.display_name} placeholder="Max Compose" disabled={saving} />
      </div>
    </div>

    <div class="field-grid">
      <div class="form-field">
        <label for="unit-runtime">Runtime type</label>
        <Input id="unit-runtime" value={DEPLOYMENT_UNIT_RUNTIME_TYPE} disabled />
        <span>Arcana direct-runtime targets use Compose.</span>
      </div>
      <div class="form-field">
        <label for="unit-ownership">Ownership mode</label>
        <Input id="unit-ownership" value={DEPLOYMENT_UNIT_OWNERSHIP_MODE} disabled />
        <span>Required for full-project rendering.</span>
      </div>
    </div>

    <div class="form-field">
      <label for="unit-endpoint">Endpoint alias *</label>
      <Input id="unit-endpoint" bind:value={form.endpoint_ref} placeholder="max" required disabled={saving} />
      <span>Alias configured under server-managed runtime endpoints. URLs and credentials are rejected.</span>
    </div>

    <div class="form-field">
      <label for="unit-compose-dir">Compose directory *</label>
      <Input id="unit-compose-dir" bind:value={form.compose_dir} placeholder="/srv/bahia/compose/gastown" required disabled={saving} />
      <span>Absolute directory dedicated to Bahia's rendered full Compose project.</span>
    </div>

    <div class="field-grid">
      <div class="form-field">
        <label for="unit-reconcile">Reconcile mode *</label>
        <Select
          id="unit-reconcile"
          bind:value={form.reconcile_mode}
          options={DEPLOYMENT_UNIT_RECONCILE_OPTIONS.map((option) => ({
            ...option,
            disabled: environment?.protected && option.value === 'auto_apply'
          }))}
          required
          disabled={saving}
        />
      </div>
      <div class="form-field">
        <label for="unit-execution">Compose execution *</label>
        <Select id="unit-execution" bind:value={form.execution_mode} options={DEPLOYMENT_UNIT_EXECUTION_OPTIONS} required disabled={saving} />
      </div>
    </div>

    {#if originalKey && originalKey !== form.key}
      <p class="notice protected-notice">Renaming a key atomically removes and recreates that target boundary. Bahia will reject the change if deployments still reference it.</p>
    {/if}
    {#if draftStale}
      <p class="notice error-notice">This environment changed while you were editing. Reload the latest target set and review your changes.</p>
    {/if}
    {#if formError}
      <p class="error" role="alert">{formError}</p>
    {/if}

    <div class="form-actions">
      <LoadingButton type="button" variant="secondary" onclick={closeEditor} disabled={saving}>Cancel</LoadingButton>
      <LoadingButton type="submit" variant="primary" loading={saving} disabled={draftStale}>
        {environment?.protected ? 'Review Protected Change' : 'Save Unit'}
      </LoadingButton>
    </div>
  </form>
</Modal>

<ConfirmDialog
  bind:open={confirmOpen}
  title="Confirm Protected Target Change"
  confirmLabel="Sign Target Update"
  loading={saving}
  onConfirm={publishPending}
  onCancel={() => { confirmOpen = false; editorOpen = true; pendingPayload = null; }}
  onClose={() => { confirmOpen = false; editorOpen = true; pendingPayload = null; }}
>
  <div class="confirm-summary">
    <p><strong>Environment:</strong> {environment?.name}</p>
    <p><strong>Unit:</strong> {form.display_name || form.key} (<code>{form.key}</code>)</p>
    <p><strong>Endpoint alias:</strong> <code>{form.endpoint_ref}</code></p>
    <p><strong>Compose directory:</strong> <code>{form.compose_dir}</code></p>
    <p><strong>Reconcile:</strong> {form.reconcile_mode}</p>
    <p>No endpoint credentials are included in this signed update.</p>
  </div>
</ConfirmDialog>

<style>
  .deployment-units {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    margin-bottom: 1.5rem;
    border: 1px solid var(--border-color);
  }
  .section-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  h2, h3, p { margin-top: 0; }
  h2 { font-size: 1rem; color: var(--text-muted); margin-bottom: 0.35rem; }
  h3 { font-size: 1rem; margin-bottom: 0.25rem; }
  .section-copy, .form-intro, .unit-note, .form-field span {
    color: var(--text-muted);
    font-size: 0.8rem;
    line-height: 1.5;
  }
  .unit-list {
    display: grid;
    gap: 1rem;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  }
  .unit-card {
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 1rem;
    background: var(--hover-bg);
  }
  .unit-card.implicit { border-style: dashed; }
  .unit-heading, .badges, .form-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .badges { justify-content: flex-end; flex-wrap: wrap; }
  .badge {
    background: var(--primary);
    color: white;
    border-radius: 999px;
    padding: 0.2rem 0.55rem;
    font-size: 0.7rem;
  }
  .badge.muted { background: var(--bg); color: var(--text-muted); }
  dl { display: grid; gap: 0.5rem; margin: 1rem 0; }
  dl > div { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 0.75rem; }
  dt { color: var(--text-muted); font-size: 0.75rem; }
  dd { margin: 0; overflow-wrap: anywhere; font-size: 0.85rem; }
  .notice, .empty-targets {
    padding: 0.75rem;
    border-radius: 6px;
    background: var(--hover-bg);
    margin-bottom: 1rem;
    color: var(--text-muted);
    font-size: 0.85rem;
  }
  .protected-notice { border: 1px solid var(--warning, #d97706); color: var(--warning, #d97706); }
  .error-notice, .error { border: 1px solid var(--error); color: var(--error); }
  .empty-targets { display: flex; flex-direction: column; gap: 0.25rem; }
  .unit-form { display: flex; flex-direction: column; gap: 1rem; }
  .field-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
  .form-field { display: flex; flex-direction: column; gap: 0.4rem; }
  .form-field label { font-size: 0.875rem; font-weight: 500; }
  .form-actions { justify-content: flex-end; margin-top: 0.5rem; }
  .error { margin: 0; padding: 0.75rem; border-radius: 6px; background: rgba(239, 68, 68, 0.1); }
  .confirm-summary { display: flex; flex-direction: column; gap: 0.5rem; }
  .confirm-summary p { margin: 0; overflow-wrap: anywhere; }
  @media (max-width: 720px) {
    .section-header, .field-grid { display: flex; flex-direction: column; }
  }
</style>
