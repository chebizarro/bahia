<script>
  import { toast } from '$lib/components/toast.js';
  import {
    applyBackupDefinition,
    applyBackupPolicy,
    applyBackupRecipe,
    registerBackupRepository,
    resultContent
  } from '$lib/stores/public-controlplane.svelte.js';

  let { section = '', repositories = [], policies = [], recipes = [], onAccepted = async () => {} } = $props();

  let expanded = $state(false);
  let submitting = $state(false);
  let repoForm = $state({ name: '', backend: 'kopia', repository_uri: '', credential_profile: '' });
  let policyForm = $state({ name: '', require_verification: true, verification_mode: 'kopia_snapshot_verify', retention: '' });
  let recipeForm = $state({ name: '', version: 'v1', backend: 'kopia', repository_id: '', policy_id: '', target_ref: '', verification_mode: 'none' });
  let definitionForm = $state({ name: '', repository_id: '', policy_id: '', recipe_id: '', schedule_enabled: false, schedule_expression: '', requires_approval: false, approval_policy: '' });

  const supportsMutation = $derived(['repositories', 'policies', 'recipes', 'definitions'].includes(section));
  const actionLabel = $derived({
    repositories: 'Register repository',
    policies: 'Apply policy',
    recipes: 'Apply recipe',
    definitions: 'Apply definition'
  }[section] || 'Backup mutation');

  function resetForm() {
    if (section === 'repositories') repoForm = { name: '', backend: 'kopia', repository_uri: '', credential_profile: '' };
    if (section === 'policies') policyForm = { name: '', require_verification: true, verification_mode: 'kopia_snapshot_verify', retention: '' };
    if (section === 'recipes') recipeForm = { name: '', version: 'v1', backend: 'kopia', repository_id: '', policy_id: '', target_ref: '', verification_mode: 'none' };
    if (section === 'definitions') definitionForm = { name: '', repository_id: '', policy_id: '', recipe_id: '', schedule_enabled: false, schedule_expression: '', requires_approval: false, approval_policy: '' };
  }

  async function submitMutation(event) {
    event.preventDefault();
    submitting = true;
    try {
      let result;
      if (section === 'repositories') {
        result = await registerBackupRepository(repoForm);
      } else if (section === 'policies') {
        const metadata = policyForm.retention ? { retention: policyForm.retention } : {};
        result = await applyBackupPolicy({ ...policyForm, metadata });
      } else if (section === 'recipes') {
        result = await applyBackupRecipe({ ...recipeForm, policy_id: recipeForm.policy_id || undefined });
      } else if (section === 'definitions') {
        result = await applyBackupDefinition({
          ...definitionForm,
          schedule_expression: definitionForm.schedule_enabled ? definitionForm.schedule_expression : '',
          approval_policy: definitionForm.requires_approval ? definitionForm.approval_policy : ''
        });
      }
      const content = resultContent(result);
      toast.success(content.message || `${actionLabel} command accepted`);
      resetForm();
      await onAccepted?.();
    } catch (err) {
      toast.error(err?.message || `Failed to publish ${actionLabel.toLowerCase()} command`);
    } finally {
      submitting = false;
    }
  }
</script>

{#if supportsMutation}
  <section class="mutation-panel">
    <div class="mutation-heading">
      <div>
        <h2>{actionLabel}</h2>
        <p>Publishes a signed ContextVM command; status and terminal truth arrive through canonical backup read models.</p>
      </div>
      <button type="button" onclick={() => (expanded = !expanded)}>{expanded ? 'Close' : actionLabel}</button>
    </div>

    {#if expanded}
      <form onsubmit={submitMutation}>
        {#if section === 'repositories'}
          <label><span>Name</span><input bind:value={repoForm.name} required placeholder="primary-kopia" /></label>
          <label><span>Backend</span><select bind:value={repoForm.backend}><option value="kopia">Kopia</option><option value="velero">Velero</option></select></label>
          <label class="wide"><span>Repository URI</span><input bind:value={repoForm.repository_uri} required placeholder="kopia://primary" /></label>
          <label class="wide"><span>Credential profile</span><input bind:value={repoForm.credential_profile} placeholder="secret://backup/kopia" /></label>
        {:else if section === 'policies'}
          <label><span>Name</span><input bind:value={policyForm.name} required placeholder="verified-daily" /></label>
          <label><span>Verification</span><select bind:value={policyForm.verification_mode}><option value="kopia_snapshot_verify">Kopia snapshot verify</option><option value="none">None</option></select></label>
          <label class="checkbox"><input type="checkbox" bind:checked={policyForm.require_verification} /> <span>Require verification</span></label>
          <label class="wide"><span>Retention note</span><input bind:value={policyForm.retention} placeholder="30d daily / 12 monthly" /></label>
        {:else if section === 'recipes'}
          <label><span>Name</span><input bind:value={recipeForm.name} required placeholder="service-daily" /></label>
          <label><span>Version</span><input bind:value={recipeForm.version} required placeholder="v1" /></label>
          <label><span>Backend</span><select bind:value={recipeForm.backend}><option value="kopia">Kopia</option><option value="velero">Velero</option></select></label>
          <label><span>Repository</span><select bind:value={recipeForm.repository_id} required><option value="">Select repository</option>{#each repositories as repo}<option value={repo.id}>{repo.name || repo.id}</option>{/each}</select></label>
          <label><span>Policy</span><select bind:value={recipeForm.policy_id}><option value="">No policy</option>{#each policies as policy}<option value={policy.id}>{policy.name || policy.id}</option>{/each}</select></label>
          <label><span>Verification mode</span><select bind:value={recipeForm.verification_mode}><option value="none">None</option><option value="kopia_snapshot_verify">Kopia snapshot verify</option></select></label>
          <label class="wide"><span>Target ref</span><input bind:value={recipeForm.target_ref} required placeholder="fs:/srv/app" /></label>
        {:else if section === 'definitions'}
          <label><span>Name</span><input bind:value={definitionForm.name} required placeholder="service-prod" /></label>
          <label><span>Repository</span><select bind:value={definitionForm.repository_id} required><option value="">Select repository</option>{#each repositories as repo}<option value={repo.id}>{repo.name || repo.id}</option>{/each}</select></label>
          <label><span>Policy</span><select bind:value={definitionForm.policy_id} required><option value="">Select policy</option>{#each policies as policy}<option value={policy.id}>{policy.name || policy.id}</option>{/each}</select></label>
          <label><span>Recipe</span><select bind:value={definitionForm.recipe_id} required><option value="">Select recipe</option>{#each recipes as recipe}<option value={recipe.id}>{recipe.name || recipe.id}{recipe.version ? `:${recipe.version}` : ''}</option>{/each}</select></label>
          <label class="checkbox"><input type="checkbox" bind:checked={definitionForm.schedule_enabled} /> <span>Enable schedule</span></label>
          <label><span>Schedule</span><input bind:value={definitionForm.schedule_expression} disabled={!definitionForm.schedule_enabled} placeholder="0 2 * * *" /></label>
          <label class="checkbox"><input type="checkbox" bind:checked={definitionForm.requires_approval} /> <span>Require restore approval</span></label>
          <label><span>Approval policy</span><input bind:value={definitionForm.approval_policy} disabled={!definitionForm.requires_approval} placeholder="operator" /></label>
        {/if}
        <div class="form-actions">
          <button type="submit" disabled={submitting}>{submitting ? 'Publishing…' : actionLabel}</button>
          <button type="button" class="secondary" disabled={submitting} onclick={resetForm}>Reset</button>
        </div>
      </form>
    {/if}
  </section>
{/if}

<style>
  .mutation-panel { border: 1px solid var(--border-color); border-radius: 0.75rem; background: var(--card-bg); padding: 1rem; }
  .mutation-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  h2 { font-size: 1rem; margin: 0 0 0.25rem; }
  p { color: var(--text-muted); margin: 0; }
  form { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); margin-top: 1rem; }
  label { display: flex; flex-direction: column; gap: 0.3rem; color: var(--text-muted); font-size: 0.85rem; }
  label.wide { grid-column: 1 / -1; }
  label.checkbox { align-items: center; flex-direction: row; margin-top: 1.7rem; }
  input, select { border: 1px solid var(--border-color); border-radius: 0.45rem; background: var(--bg-primary); color: var(--text-primary); padding: 0.55rem 0.65rem; }
  input:disabled { opacity: 0.55; }
  button { border: 1px solid var(--border-color); border-radius: 0.45rem; background: transparent; color: var(--text-primary); cursor: pointer; padding: 0.45rem 0.7rem; }
  button:hover:not(:disabled) { border-color: var(--primary); }
  button:disabled { cursor: not-allowed; opacity: 0.55; }
  .form-actions { display: flex; gap: 0.5rem; grid-column: 1 / -1; }
  .form-actions button:first-child, .mutation-heading button { border-color: var(--primary); color: var(--primary); }
  .secondary { color: var(--text-muted); }
</style>
