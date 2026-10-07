<script>
  import { createDefaultPersonaSpec, patchCustomizationSection } from '$lib/stores/souls.svelte.js';

  /** @typedef {import('$lib/types/customization').SoulPersonaSpec} SoulPersonaSpec */

  let {
    value = $bindable(),
    showAdvanced = false,
    disabled = false,
    syncStore = true
  } = $props();

  const traitSuggestions = [
    'analytical',
    'curious',
    'empathetic',
    'precise',
    'patient',
    'proactive',
    'thorough',
    'creative',
    'pragmatic',
    'skeptical',
    'warm',
    'concise'
  ];

  const styleOptions = [
    { id: 'conversational', label: 'Conversational' },
    { id: 'formal', label: 'Formal' },
    { id: 'concise', label: 'Concise' },
    { id: 'playful', label: 'Playful' },
    { id: 'technical', label: 'Technical' }
  ];

  const toneOptions = [
    'friendly professional',
    'calm and reassuring',
    'direct and practical',
    'warm and curious',
    'expert and precise'
  ];

  const promptSections = [
    { id: 'role', label: 'Role', placeholder: 'You are Scout, a research assistant…' },
    { id: 'guidelines', label: 'Guidelines', placeholder: 'When answering, be specific and cite relevant context…' },
    { id: 'red_lines', label: 'Red lines', placeholder: 'Never fabricate citations or claim unverified facts…' },
    { id: 'notes', label: 'Additional notes', placeholder: 'Optional continuity, memory, or compaction hints…', advanced: true }
  ];

  let traitInput = $state('');
  let newConstraint = $state('');
  let importError = $state('');
  let importing = $state(false);
  let collapsedSections = $state(new Set(['red_lines', 'notes']));

  const spec = $derived(createDefaultPersonaSpec(value || {}));
  const sections = $derived(spec.system_prompt_sections || {});
  const matchingTraits = $derived(
    traitInput.trim()
      ? traitSuggestions.filter((trait) =>
          trait.toLowerCase().includes(traitInput.trim().toLowerCase()) && !(spec.traits || []).includes(trait)
        ).slice(0, 6)
      : []
  );
  const roleLength = $derived((sections.role || '').length);
  const validationErrors = $derived({
    role: !(sections.role || '').trim()
      ? 'Role is required.'
      : roleLength > 1200
        ? 'Role must be 1200 characters or fewer.'
        : '',
    traits: (spec.traits || []).length > 12 ? 'Use 12 traits or fewer.' : ''
  });

  function commit(next) {
    value = createDefaultPersonaSpec(next);
    if (syncStore) patchCustomizationSection('persona', value);
  }

  function patch(updates) {
    commit({ ...spec, ...updates });
  }

  function patchSection(name, content) {
    patch({
      system_prompt_sections: {
        ...sections,
        [name]: content
      }
    });
  }

  function addTrait(trait = traitInput) {
    const normalized = trait.trim().toLowerCase();
    if (!normalized || (spec.traits || []).includes(normalized)) return;
    patch({ traits: [...(spec.traits || []), normalized] });
    traitInput = '';
  }

  function removeTrait(trait) {
    patch({ traits: (spec.traits || []).filter((item) => item !== trait) });
  }

  function handleTraitKeydown(event) {
    if (event.key === 'Enter' || event.key === ',') {
      event.preventDefault();
      addTrait();
    }
  }

  function addConstraint() {
    const constraint = newConstraint.trim();
    if (!constraint) return;
    patch({ constraints: [...(spec.constraints || []), constraint] });
    newConstraint = '';
  }

  function updateConstraint(index, nextValue) {
    const constraints = [...(spec.constraints || [])];
    constraints[index] = nextValue;
    patch({ constraints: constraints.map((item) => item.trim()).filter(Boolean) });
  }

  function removeConstraint(index) {
    patch({ constraints: (spec.constraints || []).filter((_, itemIndex) => itemIndex !== index) });
  }

  function toggleSection(sectionId) {
    const next = new Set(collapsedSections);
    if (next.has(sectionId)) next.delete(sectionId);
    else next.add(sectionId);
    collapsedSections = next;
  }

  function exportPersona() {
    const blob = new Blob([JSON.stringify(spec, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'soul-persona.json';
    link.click();
    URL.revokeObjectURL(url);
  }

  async function importPersona(event) {
    const file = event.currentTarget.files?.[0];
    if (!file) return;
    importError = '';
    importing = true;
    try {
      const imported = JSON.parse(await file.text());
      commit(imported);
    } catch (err) {
      importError = err?.message || 'Could not import persona JSON';
    } finally {
      importing = false;
      event.currentTarget.value = '';
    }
  }
</script>

<section class="studio-panel" aria-busy={importing}>
  <div class="panel-header">
    <div>
      <h3>Personality Builder</h3>
      <p>Compose persona traits, tone, constraints, and system prompt sections for the draft publish flow.</p>
    </div>
    <div class="action-row">
      <label class="btn-secondary">
        {#if importing}<span class="spinner" aria-hidden="true"></span>{/if}
        {importing ? 'Loading preset…' : 'Import'}
        <input type="file" accept="application/json,.json" disabled={disabled || importing} onchange={importPersona} />
      </label>
      <button type="button" class="btn-secondary" disabled={disabled} onclick={exportPersona}>Export</button>
    </div>
  </div>

  {#if importError}
    <div class="error-message"><span>{importError}</span><label class="retry-link">Retry<input type="file" accept="application/json,.json" disabled={disabled || importing} onchange={importPersona} /></label></div>
  {/if}

  <div class="studio-grid">
    <div class="form-stack">
      <label>
        <span>Traits</span>
        <div class="tag-editor">
          {#each spec.traits || [] as trait}
            <button type="button" class="tag" disabled={disabled} onclick={() => removeTrait(trait)}>{trait}<span aria-hidden="true">×</span></button>
          {/each}
          <input value={traitInput} disabled={disabled} oninput={(event) => (traitInput = event.currentTarget.value)} onkeydown={handleTraitKeydown} placeholder="Add trait…" />
        </div>
      </label>
      {#if validationErrors.traits}<small class="validation-error">{validationErrors.traits}</small>{/if}
      {#if matchingTraits.length > 0}
        <div class="autocomplete" aria-label="Trait suggestions">
          {#each matchingTraits as trait}
            <button type="button" disabled={disabled} onclick={() => addTrait(trait)}>{trait}</button>
          {/each}
        </div>
      {/if}

      <div class="two-col">
        <label>Style
          <select value={spec.style || 'conversational'} disabled={disabled} onchange={(event) => patch({ style: event.currentTarget.value })}>
            {#each styleOptions as option}
              <option value={option.id}>{option.label}</option>
            {/each}
          </select>
        </label>
        <label>Tone
          <select value={spec.tone || 'friendly professional'} disabled={disabled} onchange={(event) => patch({ tone: event.currentTarget.value })}>
            {#each toneOptions as tone}
              <option value={tone}>{tone}</option>
            {/each}
          </select>
        </label>
      </div>

      <div class="list-editor" aria-labelledby="constraints-heading">
        <strong id="constraints-heading" class="field-label">Constraints</strong>
        {#each spec.constraints || [] as constraint, index}
          <div class="list-row">
            <input value={constraint} disabled={disabled} onblur={(event) => updateConstraint(index, event.currentTarget.value)} />
            <button type="button" disabled={disabled} onclick={() => removeConstraint(index)}>Remove</button>
          </div>
        {/each}
        <div class="list-row">
          <input value={newConstraint} disabled={disabled} oninput={(event) => (newConstraint = event.currentTarget.value)} onkeydown={(event) => event.key === 'Enter' && (event.preventDefault(), addConstraint())} placeholder="Always cite repository context" />
          <button type="button" disabled={disabled || !newConstraint.trim()} onclick={addConstraint}>Add</button>
        </div>
      </div>

      <div class="section-editors">
        <h4>System prompt sections</h4>
        {#each promptSections.filter((section) => showAdvanced || !section.advanced) as section}
          <article class="prompt-section">
            <button type="button" class="section-toggle" onclick={() => toggleSection(section.id)} aria-expanded={!collapsedSections.has(section.id)}>
              <strong>{section.label}</strong>
              <span>{collapsedSections.has(section.id) ? 'Expand' : 'Collapse'}</span>
            </button>
            {#if !collapsedSections.has(section.id)}
              <textarea rows="5" maxlength={section.id === 'role' ? 1200 : undefined} value={sections[section.id] || ''} disabled={disabled} oninput={(event) => patchSection(section.id, event.currentTarget.value)} placeholder={section.placeholder}></textarea>
              {#if section.id === 'role'}<small class:warn={roleLength > 1080}>{roleLength}/1200 characters</small>{/if}
              {#if section.id === 'role' && validationErrors.role}<small class="validation-error">{validationErrors.role}</small>{/if}
            {/if}
          </article>
        {/each}
      </div>
    </div>

    <aside class="preview-card">
      <strong>Character preview</strong>
      <div class="preview-identity">
        <span class="avatar-dot">✦</span>
        <div>
          <h4>{spec.style || 'Conversational'} soul</h4>
          <p>{spec.tone || 'friendly professional'}</p>
        </div>
      </div>
      <p>{sections.role || 'Add a role section to preview how this soul introduces itself.'}</p>
      <div class="preview-tags">
        {#each (spec.traits || []).slice(0, 6) as trait}<span>{trait}</span>{/each}
        {#if (spec.traits || []).length === 0}<span>No traits yet</span>{/if}
      </div>
      <div>
        <strong>Operating constraints</strong>
        {#if (spec.constraints || []).length === 0}
          <p>No constraints configured.</p>
        {:else}
          <ul>{#each spec.constraints || [] as constraint}<li>{constraint}</li>{/each}</ul>
        {/if}
      </div>
    </aside>
  </div>
</section>

<style>
  .studio-panel { display: grid; gap: 1rem; }
  .panel-header, .action-row, .section-toggle { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  h3, h4, p { margin: 0; }
  p, small, .section-toggle span { color: var(--text-muted); font-size: 0.85rem; }
  small.warn { color: #f59e0b; }
  .validation-error { color: #ef4444; }
  .studio-grid { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 1rem; }
  .form-stack, .section-editors, .preview-card, .list-editor { display: grid; gap: 0.85rem; }
  label, .field-label { display: grid; gap: 0.35rem; font-size: 0.85rem; font-weight: 400; }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .tag-editor { display: flex; flex-wrap: wrap; gap: 0.45rem; border: 1px solid var(--border-color); border-radius: 10px; padding: 0.45rem; background: var(--bg); }
  .tag-editor input { flex: 1 1 10rem; border: 0; padding: 0.3rem; min-width: 8rem; }
  .tag, .preview-tags span { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.3rem 0.55rem; background: rgba(255,255,255,0.04); color: var(--text-primary); }
  .tag span { margin-left: 0.35rem; color: var(--text-muted); }
  .autocomplete { display: flex; flex-wrap: wrap; gap: 0.4rem; margin-top: -0.5rem; }
  .autocomplete button, .list-row button, .btn-secondary, .retry-link { border: 1px solid var(--border-color); border-radius: 8px; background: transparent; color: var(--text-muted); padding: 0.5rem 0.7rem; font: inherit; cursor: pointer; display: inline-flex; align-items: center; justify-content: center; gap: 0.45rem; }
  .btn-secondary input, .retry-link input { display: none; }
  .list-row { display: grid; grid-template-columns: 1fr auto; gap: 0.5rem; }
  .prompt-section, .preview-card, .error-message { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  .section-toggle { width: 100%; border: 0; background: transparent; color: var(--text-primary); padding: 0; font: inherit; cursor: pointer; }
  .preview-card { align-content: start; min-width: 0; }
  .preview-identity { display: flex; gap: 0.75rem; align-items: center; }
  .avatar-dot { display: grid; place-items: center; width: 2.5rem; height: 2.5rem; border-radius: 50%; background: var(--primary, #6366f1); color: white; }
  .preview-tags { display: flex; flex-wrap: wrap; gap: 0.4rem; }
  ul { margin: 0.4rem 0 0; padding-left: 1.1rem; color: var(--text-muted); font-size: 0.85rem; }
  .error-message { border-color: rgba(239,68,68,0.45); color: #ef4444; display: flex; justify-content: space-between; gap: 0.75rem; align-items: center; }
  .spinner { width: 0.9rem; height: 0.9rem; border: 2px solid rgba(255,255,255,0.25); border-top-color: currentColor; border-radius: 50%; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  button:disabled, input:disabled, select:disabled, textarea:disabled, .btn-secondary:has(input:disabled) { opacity: 0.55; cursor: not-allowed; }
  @media (max-width: 1024px) { .studio-grid { grid-template-columns: minmax(0, 1fr) 260px; } }
  @media (max-width: 820px) { .studio-grid, .two-col, .list-row { grid-template-columns: 1fr; } .panel-header, .action-row, .error-message { flex-direction: column; align-items: stretch; } }
</style>
