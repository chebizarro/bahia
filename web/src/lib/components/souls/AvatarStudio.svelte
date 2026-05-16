<script>
  let {
    value = $bindable(),
    showAdvanced = false,
    onGenerate = null,
    disabled = false
  } = $props();

  const maxPromptLength = 1200;
  const avatarProviders = [
    { id: 'flux-comfyui', label: 'FLUX / ComfyUI' },
    { id: 'fal', label: 'Fal.ai' },
    { id: 'replicate', label: 'Replicate' }
  ];
  const stylePresets = [
    { id: 'pixel-art', label: 'Pixel art', swatch: '▦', description: 'Crisp icon-like agent portrait' },
    { id: 'corporate', label: 'Corporate', swatch: '◉', description: 'Polished assistant headshot' },
    { id: 'abstract', label: 'Abstract', swatch: '✦', description: 'Symbolic identity mark' },
    { id: 'anime', label: 'Anime', swatch: '◒', description: 'Expressive illustrated character' },
    { id: 'realistic', label: 'Realistic', swatch: '◎', description: 'Detailed cinematic portrait' }
  ];

  let generating = $state(false);
  let generationMessage = $state('');
  let generationError = $state('');
  let zoomPreview = $state(false);
  let uploadName = $state('');
  let history = $state([]);

  const spec = $derived(value || defaultAvatarSpec());
  const generation = $derived(spec.generation || defaultAvatarSpec().generation);
  const currentMode = $derived(spec.current || 'generated');
  const previewRef = $derived(resolveAvatarRef(spec));
  const promptLength = $derived((generation.prompt || '').length);
  const validationErrors = $derived({
    prompt: !(generation.prompt || '').trim()
      ? 'Avatar prompt is required.'
      : promptLength > maxPromptLength
        ? `Avatar prompt must be ${maxPromptLength} characters or fewer.`
        : '',
    width: Number(generation.width || 512) < 128 ? 'Width must be at least 128px.' : '',
    height: Number(generation.height || 512) < 128 ? 'Height must be at least 128px.' : ''
  });
  const hasValidationErrors = $derived(Object.values(validationErrors).some(Boolean));
  const canGenerate = $derived(!disabled && !generating && !hasValidationErrors);

  function defaultAvatarSpec(overrides = {}) {
    return {
      generation: {
        prompt: '',
        style_preset: 'pixel-art',
        seed: '',
        width: 512,
        height: 512,
        provider: 'flux-comfyui',
        ...(overrides.generation || {})
      },
      uploaded_ref: '',
      generated_ref: '',
      current: 'generated',
      ...overrides
    };
  }

  function resolveAvatarRef(avatar = {}) {
    const current = avatar.current || 'generated';
    if (current === 'uploaded') return avatar.uploaded_ref || avatar.generated_ref || '';
    return avatar.generated_ref || avatar.uploaded_ref || '';
  }

  function normalizedSpec(overrides = {}) {
    return defaultAvatarSpec({ ...(value || {}), ...overrides });
  }

  function rememberVariant(ref, source = currentMode) {
    if (!ref || history.some((item) => item.ref === ref)) return;
    history = [
      { ref, source, style: generation.style_preset || 'pixel-art', provider: generation.provider || 'flux-comfyui' },
      ...history
    ].slice(0, 12);
  }

  function patch(updates) {
    const next = normalizedSpec(updates);
    value = next;
    const ref = resolveAvatarRef(next);
    if (ref) rememberVariant(ref, next.current || 'generated');
  }

  function patchGeneration(updates) {
    patch({
      generation: {
        ...defaultAvatarSpec().generation,
        ...(value?.generation || {}),
        ...updates
      }
    });
  }

  function randomizeSeed() {
    patchGeneration({ seed: `avatar-${Math.random().toString(36).slice(2, 10)}` });
  }

  async function generateAvatar() {
    if (!canGenerate) return;
    generating = true;
    generationError = '';
    generationMessage = 'Publishing avatar generation request via Nostr runtime control…';

    try {
      const request = normalizedSpec({ current: 'generated' });
      let result = null;
      if (onGenerate) {
        result = await onGenerate(request);
      }

      const generatedRef = result?.ref || result?.avatar_ref || result?.avatarRef || result?.generated_ref || '';
      if (generatedRef) {
        patch({ generated_ref: generatedRef, current: 'generated' });
        generationMessage = 'Generation result received.';
      } else {
        patch({ current: 'generated' });
        generationMessage = onGenerate
          ? 'Generation request sent. Waiting for runtime result events.'
          : 'Avatar draft is ready. Save this draft to publish runtime control through SoulFactory.';
      }
    } catch (err) {
      generationError = err.message || 'Avatar generation request failed';
      generationMessage = '';
    } finally {
      generating = false;
    }
  }

  function handleUpload(event) {
    const file = event.currentTarget.files?.[0];
    if (!file) return;
    const ref = URL.createObjectURL(file);
    uploadName = file.name;
    patch({ uploaded_ref: ref, current: 'uploaded' });
  }

  function selectHistory(ref) {
    if (!ref) return;
    if (ref === spec.uploaded_ref) {
      patch({ current: 'uploaded' });
    } else {
      patch({ generated_ref: ref, current: 'generated' });
    }
  }

  function isImageRef(ref) {
    return ref?.startsWith('http') || ref?.startsWith('blob:') || ref?.startsWith('data:image/');
  }

  function handlePanelKeydown(event) {
    if (event.key === 'Escape' && zoomPreview) {
      zoomPreview = false;
    }
  }
</script>

<svelte:window onkeydown={handlePanelKeydown} />

<section class="studio-panel" aria-busy={generating}>
  <div class="panel-header">
    <div>
      <h3>Avatar Studio</h3>
      <p>Create or attach avatar refs through the draft. Runtime generation is submitted with Nostr control events by the parent flow.</p>
    </div>
    <span class="mode-pill">{currentMode}</span>
  </div>

  <div class="studio-grid">
    <div class="form-stack">
      <label>
        <span>Avatar prompt</span>
        <textarea rows="5" maxlength={maxPromptLength} value={generation.prompt || ''} disabled={disabled || generating} oninput={(event) => patchGeneration({ prompt: event.currentTarget.value })} placeholder="Pixel art owl researcher with a magnifying glass, warm amber eyes, transparent background"></textarea>
        <small class:warn={promptLength > maxPromptLength * 0.9}>{promptLength}/{maxPromptLength} characters</small>
        {#if validationErrors.prompt}<small class="validation-error">{validationErrors.prompt}</small>{/if}
      </label>

      <label>
        <span>Provider</span>
        <select value={generation.provider || 'flux-comfyui'} disabled={disabled || generating} onchange={(event) => patchGeneration({ provider: event.currentTarget.value })}>
          {#each avatarProviders as provider}
            <option value={provider.id}>{provider.label}</option>
          {/each}
        </select>
      </label>

      <div class="preset-grid" aria-label="Avatar style presets">
        {#each stylePresets as preset}
          <button type="button" class="preset-card" class:selected={generation.style_preset === preset.id} disabled={disabled || generating} onclick={() => patchGeneration({ style_preset: preset.id })}>
            <span class="preset-swatch" aria-hidden="true">{preset.swatch}</span>
            <strong>{preset.label}</strong>
            <span>{preset.description}</span>
          </button>
        {/each}
      </div>

      <div class="action-row">
        <button type="button" class="btn-primary" disabled={!canGenerate} onclick={generateAvatar}>{#if generating}<span class="spinner" aria-hidden="true"></span>{/if}{generating ? 'Generating…' : 'Generate avatar'}</button>
        <label class="upload-button">
          Upload image
          <input type="file" accept="image/*" disabled={disabled || generating} onchange={handleUpload} />
        </label>
      </div>

      {#if generationMessage}
        <div class="status-message">{generationMessage}</div>
      {/if}
      {#if generationError}
        <div class="error-message"><span>{generationError}</span><button type="button" disabled={!canGenerate} onclick={generateAvatar}>Retry</button></div>
      {/if}

      <div class="two-col">
        <label>Use ref<select value={currentMode} disabled={disabled || generating} onchange={(event) => patch({ current: event.currentTarget.value })}><option value="generated">Generated</option><option value="uploaded">Uploaded</option></select></label>
        <label>Generated ref<input value={spec.generated_ref || ''} disabled={disabled || generating} oninput={(event) => patch({ generated_ref: event.currentTarget.value })} placeholder="blossom:… or https://…" /></label>
      </div>
      <label>Uploaded/blob ref<input value={spec.uploaded_ref || ''} disabled={disabled || generating} oninput={(event) => patch({ uploaded_ref: event.currentTarget.value })} placeholder="blob:, blossom:, https://…" /></label>
      {#if uploadName}<small>Selected upload: {uploadName}</small>{/if}

      {#if showAdvanced}
        <div class="advanced-box">
          <h4>Advanced generation</h4>
          <div class="two-col">
            <label>Seed<div class="inline-field"><input value={generation.seed || ''} disabled={disabled || generating} oninput={(event) => patchGeneration({ seed: event.currentTarget.value })} placeholder="optional" /><button type="button" class="mini-button" disabled={disabled || generating} onclick={randomizeSeed}>Random</button></div></label>
            <label>Size<div class="inline-field"><input aria-label="Width" type="number" min="128" step="64" value={generation.width || 512} disabled={disabled || generating} oninput={(event) => patchGeneration({ width: Number(event.currentTarget.value) || 512 })} /><input aria-label="Height" type="number" min="128" step="64" value={generation.height || 512} disabled={disabled || generating} oninput={(event) => patchGeneration({ height: Number(event.currentTarget.value) || 512 })} /></div>{#if validationErrors.width}<small class="validation-error">{validationErrors.width}</small>{/if}{#if validationErrors.height}<small class="validation-error">{validationErrors.height}</small>{/if}</label>
          </div>
        </div>
      {/if}
    </div>

    <aside class="preview-card">
      <div class="preview-header">
        <strong>Preview</strong>
        <button type="button" class="mini-button" disabled={!previewRef || !isImageRef(previewRef)} onclick={() => (zoomPreview = !zoomPreview)}>{zoomPreview ? 'Fit' : 'Zoom'}</button>
      </div>
      <div class="preview-frame" class:zoomed={zoomPreview}>
        {#if previewRef && isImageRef(previewRef)}
          <img src={previewRef} alt="Avatar preview" />
        {:else}
          <span>{previewRef ? 'Avatar ref saved' : 'No avatar preview yet'}</span>
        {/if}
      </div>
      {#if previewRef}<code class="ref-chip">{previewRef}</code>{/if}

      <div class="history-panel">
        <strong>Variant history</strong>
        {#if history.length === 0}
          <p>No generated or uploaded variants yet.</p>
        {:else}
          <div class="history-strip">
            {#each history as item}
              <button type="button" title={item.ref} class:selected={item.ref === previewRef} onclick={() => selectHistory(item.ref)}>
                {#if isImageRef(item.ref)}<img src={item.ref} alt="Avatar variant" />{:else}<span>{item.source}</span>{/if}
              </button>
            {/each}
          </div>
        {/if}
      </div>
    </aside>
  </div>
</section>

<style>
  .studio-panel { display: grid; gap: 1rem; }
  .panel-header, .preview-header, .action-row { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  h3, h4, p { margin: 0; }
  p, small { color: var(--text-muted); font-size: 0.85rem; }
  small.warn { color: #f59e0b; }
  .mode-pill, .ref-chip { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.25rem 0.6rem; color: var(--text-muted); font-size: 0.75rem; }
  .mode-pill { text-transform: uppercase; }
  .ref-chip { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; border-radius: 8px; }
  .studio-grid { display: grid; grid-template-columns: minmax(0, 1fr) 300px; gap: 1rem; }
  .form-stack, .advanced-box, .preview-card, .history-panel { display: grid; gap: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .preset-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.6rem; }
  .preset-card { text-align: left; display: grid; grid-template-columns: auto 1fr; gap: 0.2rem 0.55rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 10px; padding: 0.75rem; cursor: pointer; }
  .preset-card span:last-child { grid-column: 2; color: var(--text-muted); font-size: 0.78rem; }
  .preset-swatch { color: var(--primary); font-size: 1.1rem; }
  .preset-card.selected, .history-strip button.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .inline-field { display: flex; gap: 0.45rem; }
  .inline-field input { min-width: 0; }
  .mini-button, .btn-primary, .upload-button { border: 1px solid var(--border-color); border-radius: 8px; cursor: pointer; padding: 0.55rem 0.75rem; font: inherit; display: inline-flex; align-items: center; justify-content: center; gap: 0.45rem; }
  .btn-primary { background: var(--primary); color: white; border-color: var(--primary); }
  .mini-button, .upload-button { background: transparent; color: var(--text-muted); }
  .upload-button input { display: none; }
  .advanced-box, .preview-card { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  .preview-card { align-content: start; min-width: 0; }
  .preview-frame { aspect-ratio: 1; border-radius: 12px; border: 1px solid var(--border-color); background: var(--bg); display: grid; place-items: center; overflow: hidden; color: var(--text-muted); text-align: center; padding: 0.75rem; }
  .preview-frame img { width: 100%; height: 100%; object-fit: cover; transition: transform 0.2s ease; }
  .preview-frame.zoomed img { transform: scale(1.45); }
  .history-strip { display: grid; grid-template-columns: repeat(4, 1fr); gap: 0.4rem; }
  .history-strip button { aspect-ratio: 1; border: 1px solid var(--border-color); border-radius: 8px; background: var(--bg); overflow: hidden; color: var(--text-muted); padding: 0; cursor: pointer; }
  .history-strip img { width: 100%; height: 100%; object-fit: cover; }
  .status-message, .error-message { border-radius: 8px; padding: 0.65rem 0.75rem; font-size: 0.85rem; display: flex; justify-content: space-between; gap: 0.75rem; align-items: center; }
  .status-message { background: rgba(59,130,246,0.12); color: #60a5fa; }
  .error-message { background: rgba(239,68,68,0.12); color: #ef4444; }
  .error-message button { border: 1px solid currentColor; border-radius: 8px; background: transparent; color: inherit; padding: 0.35rem 0.55rem; }
  .validation-error { color: #ef4444; }
  .spinner { width: 0.9rem; height: 0.9rem; border: 2px solid rgba(255,255,255,0.45); border-top-color: currentColor; border-radius: 50%; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  button:disabled, input:disabled, select:disabled, textarea:disabled, .upload-button:has(input:disabled) { opacity: 0.55; cursor: not-allowed; }
  @media (max-width: 1024px) { .studio-grid { grid-template-columns: minmax(0, 1fr) 240px; } }
  @media (max-width: 760px) { .studio-grid, .two-col, .preset-grid { grid-template-columns: 1fr; } .panel-header, .preview-header, .action-row, .error-message { flex-direction: column; align-items: stretch; } }
</style>
