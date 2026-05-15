<script>
  let { value = $bindable(), showAdvanced = false } = $props();

  const sections = $derived(value?.system_prompt_sections || {});

  function splitCommaList(text) {
    return text.split(',').map((item) => item.trim()).filter(Boolean);
  }

  function splitLineList(text) {
    return text.split('\n').map((item) => item.trim()).filter(Boolean);
  }

  function patch(updates) {
    value = {
      traits: [],
      style: 'conversational',
      tone: 'friendly professional',
      constraints: [],
      system_prompt_sections: {
        role: '',
        guidelines: '',
        red_lines: ''
      },
      ...(value || {}),
      ...updates
    };
  }

  function patchSection(name, content) {
    patch({
      system_prompt_sections: {
        role: '',
        guidelines: '',
        red_lines: '',
        ...(value?.system_prompt_sections || {}),
        [name]: content
      }
    });
  }
</script>

<section class="studio-panel">
  <div>
    <h3>Personality Builder</h3>
    <p>Compose the persona tags, tone, constraints, and prompt sections stored in the soul draft.</p>
  </div>

  <div class="form-stack">
    <label>Traits <span class="hint">comma separated</span><input value={(value?.traits || []).join(', ')} oninput={(event) => patch({ traits: splitCommaList(event.currentTarget.value) })} placeholder="curious, thorough, patient" /></label>

    <div class="two-col">
      <label>Style<select value={value?.style || 'conversational'} onchange={(event) => patch({ style: event.currentTarget.value })}><option value="conversational">Conversational</option><option value="formal">Formal</option><option value="concise">Concise</option><option value="playful">Playful</option></select></label>
      <label>Tone<input value={value?.tone || ''} oninput={(event) => patch({ tone: event.currentTarget.value })} placeholder="friendly professional" /></label>
    </div>

    <label>Constraints <span class="hint">one per line</span><textarea rows="3" value={(value?.constraints || []).join('\n')} oninput={(event) => patch({ constraints: splitLineList(event.currentTarget.value) })} placeholder="Always cite sources&#10;Never speculate beyond data"></textarea></label>

    <label>Role section<textarea rows="4" value={sections.role || ''} oninput={(event) => patchSection('role', event.currentTarget.value)} placeholder="You are Scout, a research assistant..."></textarea></label>
    <label>Guidelines section<textarea rows="4" value={sections.guidelines || ''} oninput={(event) => patchSection('guidelines', event.currentTarget.value)} placeholder="When answering questions..."></textarea></label>

    {#if showAdvanced}
      <div class="advanced-box">
        <h4>Advanced prompt controls</h4>
        <label>Red lines section<textarea rows="4" value={sections.red_lines || ''} oninput={(event) => patchSection('red_lines', event.currentTarget.value)} placeholder="Never fabricate citations..."></textarea></label>
        <label>Additional notes<textarea rows="3" value={sections.notes || ''} oninput={(event) => patchSection('notes', event.currentTarget.value)} placeholder="Optional continuity or compaction hints"></textarea></label>
      </div>
    {/if}

    <aside class="preview-card">
      <strong>Character preview</strong>
      <p>{value?.tone || 'Friendly professional'} · {(value?.traits || []).slice(0, 4).join(', ') || 'No traits yet'}</p>
      <p>{sections.role || 'Add a role section to preview how this soul introduces itself.'}</p>
    </aside>
  </div>
</section>

<style>
  .studio-panel, .form-stack, .advanced-box, .preview-card { display: grid; gap: 0.9rem; }
  h3, h4, p { margin: 0; }
  p, .hint { color: var(--text-muted); font-size: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .advanced-box, .preview-card { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  @media (max-width: 760px) { .two-col { grid-template-columns: 1fr; } }
</style>
