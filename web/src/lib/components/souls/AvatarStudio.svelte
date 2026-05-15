<script>
  let { value = $bindable(), showAdvanced = false } = $props();

  const stylePresets = [
    { id: 'pixel-art', label: 'Pixel art', description: 'Crisp icon-like agent portrait' },
    { id: 'corporate', label: 'Corporate', description: 'Polished assistant headshot' },
    { id: 'abstract', label: 'Abstract', description: 'Symbolic identity mark' },
    { id: 'anime', label: 'Anime', description: 'Expressive illustrated character' }
  ];

  const currentMode = $derived(value?.current || 'generated');
  const generation = $derived(value?.generation || {});
  const previewRef = $derived(
    currentMode === 'uploaded'
      ? value?.uploaded_ref || ''
      : value?.generated_ref || value?.uploaded_ref || ''
  );

  function patch(updates) {
    value = {
      generation: {
        prompt: '',
        style_preset: 'pixel-art',
        seed: '',
        width: 512,
        height: 512,
        provider: 'flux-comfyui'
      },
      uploaded_ref: '',
      generated_ref: '',
      current: 'generated',
      ...(value || {}),
      ...updates
    };
  }

  function patchGeneration(updates) {
    patch({
      generation: {
        prompt: '',
        style_preset: 'pixel-art',
        seed: '',
        width: 512,
        height: 512,
        provider: 'flux-comfyui',
        ...(value?.generation || {}),
        ...updates
      }
    });
  }

  function randomizeSeed() {
    patchGeneration({ seed: `avatar-${Math.random().toString(36).slice(2, 10)}` });
  }
</script>

<section class="studio-panel">
  <div class="panel-header">
    <div>
      <h3>Avatar Studio</h3>
      <p>Draft an avatar prompt and reference. Generation hooks will attach to SoulFactory runtime control later.</p>
    </div>
    <span class="mode-pill">{currentMode}</span>
  </div>

  <div class="studio-grid">
    <div class="form-stack">
      <label>Avatar prompt<textarea rows="4" value={generation.prompt || ''} oninput={(event) => patchGeneration({ prompt: event.currentTarget.value })} placeholder="Pixel art owl researcher with a magnifying glass"></textarea></label>

      <div class="preset-grid" aria-label="Avatar style presets">
        {#each stylePresets as preset}
          <button type="button" class="preset-card" class:selected={generation.style_preset === preset.id} onclick={() => patchGeneration({ style_preset: preset.id })}>
            <strong>{preset.label}</strong>
            <span>{preset.description}</span>
          </button>
        {/each}
      </div>

      <div class="two-col">
        <label>Use ref<select value={currentMode} onchange={(event) => patch({ current: event.currentTarget.value })}><option value="generated">Generated</option><option value="uploaded">Uploaded</option></select></label>
        <label>Generated ref<input value={value?.generated_ref || ''} oninput={(event) => patch({ generated_ref: event.currentTarget.value })} placeholder="blossom:… or https://…" /></label>
      </div>
      <label>Uploaded ref<input value={value?.uploaded_ref || ''} oninput={(event) => patch({ uploaded_ref: event.currentTarget.value })} placeholder="blob:, blossom:, https://…" /></label>

      {#if showAdvanced}
        <div class="advanced-box">
          <h4>Advanced generation</h4>
          <div class="two-col">
            <label>Provider<input value={generation.provider || ''} oninput={(event) => patchGeneration({ provider: event.currentTarget.value })} placeholder="flux-comfyui" /></label>
            <label>Seed<div class="inline-field"><input value={generation.seed || ''} oninput={(event) => patchGeneration({ seed: event.currentTarget.value })} placeholder="optional" /><button type="button" class="mini-button" onclick={randomizeSeed}>Random</button></div></label>
          </div>
          <div class="two-col">
            <label>Width<input type="number" min="128" step="64" value={generation.width || 512} oninput={(event) => patchGeneration({ width: Number(event.currentTarget.value) || 512 })} /></label>
            <label>Height<input type="number" min="128" step="64" value={generation.height || 512} oninput={(event) => patchGeneration({ height: Number(event.currentTarget.value) || 512 })} /></label>
          </div>
        </div>
      {/if}
    </div>

    <aside class="preview-card">
      <div class="preview-frame">
        {#if previewRef && previewRef.startsWith('http')}
          <img src={previewRef} alt="Avatar preview" />
        {:else}
          <span>{previewRef ? 'Avatar ref saved' : 'No avatar preview yet'}</span>
        {/if}
      </div>
      <p>Preview uses direct HTTP refs today. Blossom/blob resolution will be handled by the generation service.</p>
      <button type="button" class="btn-secondary" disabled>Regenerate placeholder</button>
    </aside>
  </div>
</section>

<style>
  .studio-panel { display: grid; gap: 1rem; }
  .panel-header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  h3, h4, p { margin: 0; }
  p { color: var(--text-muted); font-size: 0.85rem; }
  .mode-pill { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.25rem 0.6rem; color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; }
  .studio-grid { display: grid; grid-template-columns: minmax(0, 1fr) 260px; gap: 1rem; }
  .form-stack, .advanced-box { display: grid; gap: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .preset-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.6rem; }
  .preset-card { text-align: left; display: grid; gap: 0.2rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 10px; padding: 0.75rem; cursor: pointer; }
  .preset-card span { color: var(--text-muted); font-size: 0.78rem; }
  .preset-card.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .inline-field { display: flex; gap: 0.45rem; }
  .inline-field input { min-width: 0; }
  .mini-button, .btn-secondary { border: 1px solid var(--border-color); border-radius: 8px; background: transparent; color: var(--text-muted); cursor: pointer; padding: 0.55rem 0.75rem; }
  .advanced-box, .preview-card { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  .preview-card { display: grid; gap: 0.75rem; align-content: start; }
  .preview-frame { aspect-ratio: 1; border-radius: 12px; border: 1px solid var(--border-color); background: var(--bg); display: grid; place-items: center; overflow: hidden; color: var(--text-muted); text-align: center; padding: 0.75rem; }
  .preview-frame img { width: 100%; height: 100%; object-fit: cover; }
  button:disabled { opacity: 0.55; cursor: not-allowed; }
  @media (max-width: 760px) { .studio-grid, .two-col, .preset-grid { grid-template-columns: 1fr; } }
</style>
