<script>
  let { value = $bindable(), assetRef = $bindable(''), showAdvanced = false } = $props();

  const providers = [
    { id: 'openai', label: 'OpenAI TTS' },
    { id: 'elevenlabs', label: 'ElevenLabs' },
    { id: 'azure', label: 'Azure Speech' },
    { id: 'local', label: 'Local CLI' }
  ];

  const persona = $derived(value?.persona || {});

  function patch(updates) {
    value = {
      provider: 'openai',
      persona_id: '',
      persona: {
        label: '',
        profile: '',
        style: '',
        accent: '',
        pacing: ''
      },
      auto_mode: 'tagged',
      sample_text: '',
      ...(value || {}),
      ...updates
    };
  }

  function patchPersona(updates) {
    patch({
      persona: {
        label: '',
        profile: '',
        style: '',
        accent: '',
        pacing: '',
        ...(value?.persona || {}),
        ...updates
      }
    });
  }
</script>

<section class="studio-panel">
  <div class="panel-header">
    <div>
      <h3>Voice Studio</h3>
      <p>Describe the TTS voice persona and keep any existing voice asset reference for compatibility.</p>
    </div>
  </div>

  <div class="form-stack">
    <div class="two-col">
      <label>Provider<select value={value?.provider || 'openai'} onchange={(event) => patch({ provider: event.currentTarget.value })}>{#each providers as provider}<option value={provider.id}>{provider.label}</option>{/each}</select></label>
      <label>Existing voice asset ref<input value={assetRef || ''} oninput={(event) => assetRef = event.currentTarget.value} placeholder="optional voice asset ref" /></label>
    </div>

    <div class="two-col">
      <label>Persona label<input value={persona.label || ''} oninput={(event) => patchPersona({ label: event.currentTarget.value })} placeholder="Scout Voice" /></label>
      <label>Persona profile<input value={persona.profile || ''} oninput={(event) => patchPersona({ profile: event.currentTarget.value })} placeholder="Young professional researcher" /></label>
    </div>

    <div class="three-col">
      <label>Style<input value={persona.style || ''} oninput={(event) => patchPersona({ style: event.currentTarget.value })} placeholder="articulate" /></label>
      <label>Accent<input value={persona.accent || ''} oninput={(event) => patchPersona({ accent: event.currentTarget.value })} placeholder="neutral american" /></label>
      <label>Pacing<input value={persona.pacing || ''} oninput={(event) => patchPersona({ pacing: event.currentTarget.value })} placeholder="measured" /></label>
    </div>

    <label>Sample text<textarea rows="3" value={value?.sample_text || ''} oninput={(event) => patch({ sample_text: event.currentTarget.value })} placeholder="Hello, I'm Scout. Let me help you find what you're looking for."></textarea></label>

    {#if showAdvanced}
      <div class="advanced-box">
        <h4>Advanced voice routing</h4>
        <div class="two-col">
          <label>Persona ID<input value={value?.persona_id || ''} oninput={(event) => patch({ persona_id: event.currentTarget.value })} placeholder="scout-voice" /></label>
          <label>Auto mode<select value={value?.auto_mode || 'tagged'} onchange={(event) => patch({ auto_mode: event.currentTarget.value })}><option value="off">Off</option><option value="always">Always</option><option value="tagged">Tagged turns only</option></select></label>
        </div>
        <p>Provider-specific controls are intentionally deferred; this draft stores the common persona shape first.</p>
      </div>
    {/if}

    <div class="sample-card">
      <strong>Sample preview placeholder</strong>
      <p>Voice sample generation will publish Nostr runtime-control events once backend capability work lands.</p>
      <button type="button" disabled>Generate sample</button>
    </div>
  </div>
</section>

<style>
  .studio-panel, .form-stack, .advanced-box, .sample-card { display: grid; gap: 0.9rem; }
  .panel-header { display: flex; justify-content: space-between; gap: 1rem; }
  h3, h4, p { margin: 0; }
  p { color: var(--text-muted); font-size: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .three-col { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.75rem; }
  .advanced-box, .sample-card { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  button { justify-self: start; border: 1px solid var(--border-color); border-radius: 8px; background: transparent; color: var(--text-muted); padding: 0.55rem 0.8rem; }
  button:disabled { opacity: 0.55; cursor: not-allowed; }
  @media (max-width: 760px) { .two-col, .three-col { grid-template-columns: 1fr; } }
</style>
