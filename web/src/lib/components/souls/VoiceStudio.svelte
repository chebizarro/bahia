<script>
  import {
    createDefaultVoiceSpec,
    publishSoulAction,
    voiceProviders
  } from '$lib/stores/souls.svelte.js';
  import { KINDS } from '$lib/nostr/client.js';

  /** @typedef {import('$lib/types/customization').SoulVoiceSpec} SoulVoiceSpec */

  let {
    value = $bindable(),
    assetRef = $bindable(''),
    showAdvanced = false,
    disabled = false,
    soul = null,
    draft = null,
    onPlay = null
  } = $props();

  const providerVoices = {
    openai: ['alloy', 'ash', 'coral', 'echo', 'fable', 'nova', 'onyx', 'sage', 'shimmer'],
    elevenlabs: ['Rachel', 'Domi', 'Bella', 'Antoni', 'Elli', 'Josh'],
    azure: ['en-US-JennyNeural', 'en-US-GuyNeural', 'en-GB-SoniaNeural', 'en-AU-NatashaNeural'],
    local: ['system-default', 'piper-en-us', 'say-compact']
  };

  const autoModes = [
    { id: 'off', label: 'Off' },
    { id: 'always', label: 'Always' },
    { id: 'tagged', label: 'Tagged turns only' }
  ];

  let playing = $state(false);
  let statusMessage = $state('');
  let errorMessage = $state('');

  const spec = $derived(createDefaultVoiceSpec(value || {}));
  const persona = $derived(spec.persona || {});
  const selectedProvider = $derived(spec.provider || 'openai');
  const providerSettings = $derived(spec.providers?.[selectedProvider] || {});
  const voices = $derived(providerVoices[selectedProvider] || providerVoices.openai);
  const selectedVoice = $derived(providerSettings.voice || providerSettings.voice_id || voices[0]);
  const sampleLength = $derived((spec.sample_text || '').length);
  const validationErrors = $derived({
    sample_text: !(spec.sample_text || '').trim()
      ? 'Sample text is required.'
      : sampleLength > 500
        ? 'Sample text must be 500 characters or fewer.'
        : '',
    speed: Number(providerSettings.speed || 1) <= 0 ? 'Speed must be greater than 0.' : '',
    style_degree: selectedProvider === 'azure' && Number(providerSettings.style_degree || 1) < 0 ? 'Style degree cannot be negative.' : ''
  });
  const hasValidationErrors = $derived(Object.values(validationErrors).some(Boolean));
  const hasSampleDispatcher = $derived(typeof onPlay === 'function' || Boolean(soul));
  const canPlay = $derived(hasSampleDispatcher && !disabled && !playing && !hasValidationErrors);

  function patch(updates) {
    /** @type {SoulVoiceSpec} */
    const next = createDefaultVoiceSpec({
      ...(value || {}),
      ...updates
    });
    value = next;
  }

  function patchPersona(updates) {
    patch({
      persona: {
        ...(spec.persona || {}),
        ...updates
      }
    });
  }

  function patchProviderSettings(updates) {
    patch({
      providers: {
        ...(spec.providers || {}),
        [selectedProvider]: {
          ...providerSettings,
          ...updates
        }
      }
    });
  }

  function selectProvider(provider) {
    const nextVoices = providerVoices[provider] || providerVoices.openai;
    patch({
      provider,
      providers: {
        ...(spec.providers || {}),
        [provider]: {
          ...(spec.providers?.[provider] || {}),
          voice: spec.providers?.[provider]?.voice || nextVoices[0]
        }
      }
    });
  }

  function draftRef() {
    return draft?.coordinate || draft?.draftRef || soul?.draftRef || '';
  }

  function draftEventId() {
    return draft?.event?.id || draft?.id || '';
  }

  async function playSample() {
    if (!canPlay) return;
    playing = true;
    statusMessage = 'Preparing voice sample request…';
    errorMessage = '';

    try {
      const payload = {
        schema: 'soulfactory-action/v1',
        action: 'voice-sample',
        method: 'soulfactory.voice.sample',
        draft: draftRef(),
        draft_ref: draftRef(),
        draft_event_id: draftEventId(),
        params: {
          voice: spec,
          sample_text: spec.sample_text,
          provider: selectedProvider,
          provider_settings: providerSettings
        }
      };

      if (onPlay) {
        await onPlay(payload);
        statusMessage = 'Voice sample request sent.';
        return;
      }

      const extraTags = [
        ['method', 'soulfactory.voice.sample'],
        ['request-kind', String(KINDS.SOUL_ACTION)],
        ['section', 'voice'],
        ['provider', selectedProvider]
      ];
      if (draftRef()) extraTags.push(['draft', draftRef()]);
      if (draftEventId()) extraTags.push(['draft-event', draftEventId()], ['e', draftEventId(), '', 'draft']);

      await publishSoulAction({
        soul,
        action: 'voice-sample',
        reason: 'Voice sample requested from Voice Studio',
        content: payload,
        extraTags
      });
      statusMessage = 'Voice sample action accepted by relay. Runtime result will arrive by event.';
    } catch (err) {
      errorMessage = err?.message || 'Failed to request voice sample';
      statusMessage = '';
    } finally {
      playing = false;
    }
  }
</script>

<section class="studio-panel" aria-busy={playing}>
  <div class="panel-header">
    <div>
      <h3>Voice Studio</h3>
      <p>Configure the soul voice draft. Runtime playback is published through the SoulFactory event flow when a soul is available.</p>
    </div>
    <span class="mode-pill">{spec.auto_mode || 'tagged'}</span>
  </div>

  <div class="studio-grid">
    <div class="form-stack">
      <div class="two-col">
        <label>
          <span>Provider</span>
          <select value={selectedProvider} disabled={disabled || playing} onchange={(event) => selectProvider(event.currentTarget.value)}>
            {#each voiceProviders as provider}
              <option value={provider.id}>{provider.label}</option>
            {/each}
          </select>
        </label>
        <label>
          <span>Voice</span>
          <select value={selectedVoice} disabled={disabled || playing} onchange={(event) => patchProviderSettings({ voice: event.currentTarget.value, voice_id: event.currentTarget.value })}>
            {#each voices as voice}
              <option value={voice}>{voice}</option>
            {/each}
          </select>
        </label>
      </div>

      <div class="provider-list" aria-label="Available voice providers">
        {#each voiceProviders as provider}
          <button type="button" class:selected={selectedProvider === provider.id} disabled={disabled || playing} onclick={() => selectProvider(provider.id)}>
            <strong>{provider.label}</strong>
            <span>{provider.description || 'Voice provider'}</span>
          </button>
        {/each}
      </div>

      <div class="two-col">
        <label><span>Persona label</span><input value={persona.label || ''} disabled={disabled || playing} oninput={(event) => patchPersona({ label: event.currentTarget.value })} placeholder="Scout Voice" /></label>
        <label><span>Persona profile</span><input value={persona.profile || ''} disabled={disabled || playing} oninput={(event) => patchPersona({ profile: event.currentTarget.value })} placeholder="Young professional researcher" /></label>
      </div>

      <div class="three-col">
        <label><span>Style</span><input value={persona.style || ''} disabled={disabled || playing} oninput={(event) => patchPersona({ style: event.currentTarget.value })} placeholder="articulate" /></label>
        <label><span>Accent</span><input value={persona.accent || ''} disabled={disabled || playing} oninput={(event) => patchPersona({ accent: event.currentTarget.value })} placeholder="neutral american" /></label>
        <label><span>Pacing</span><input value={persona.pacing || ''} disabled={disabled || playing} oninput={(event) => patchPersona({ pacing: event.currentTarget.value })} placeholder="measured" /></label>
      </div>

      <div class="two-col">
        <label>
          <span>Auto mode</span>
          <select value={spec.auto_mode || 'tagged'} disabled={disabled || playing} onchange={(event) => patch({ auto_mode: event.currentTarget.value })}>
            {#each autoModes as mode}
              <option value={mode.id}>{mode.label}</option>
            {/each}
          </select>
        </label>
        <label><span>Existing voice asset ref</span><input value={assetRef || ''} disabled={disabled || playing} oninput={(event) => assetRef = event.currentTarget.value} placeholder="optional voice asset ref" /></label>
      </div>

      <label>
        <span>Sample text</span>
        <textarea rows="4" maxlength="500" value={spec.sample_text || ''} disabled={disabled || playing} oninput={(event) => patch({ sample_text: event.currentTarget.value })} placeholder="Hello, I'm Scout. Let me help you find what you're looking for."></textarea>
        <small class:warn={sampleLength > 450}>{sampleLength}/500 characters</small>
        {#if validationErrors.sample_text}<small class="validation-error">{validationErrors.sample_text}</small>{/if}
      </label>
      <div class="action-row">
        <button type="button" class="btn-primary" disabled={!canPlay} onclick={playSample}>{#if playing}<span class="spinner" aria-hidden="true"></span>{/if}{playing ? 'Requesting…' : 'Play sample'}</button>
      </div>
      {#if !hasSampleDispatcher}
        <small>Sample playback is available after deployment or when this page provides a runtime sample dispatcher. Voice settings are saved to the draft and applied on provision.</small>
      {/if}

      {#if statusMessage}<div class="status-message">{statusMessage}</div>{/if}
      {#if errorMessage}<div class="error-message"><span>{errorMessage}</span><button type="button" disabled={!canPlay} onclick={playSample}>Retry</button></div>{/if}
    </div>

    <aside class="settings-card">
      <h4>{selectedProvider} settings</h4>
      <label><span>Model</span><input value={providerSettings.model || ''} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ model: event.currentTarget.value })} placeholder={selectedProvider === 'openai' ? 'gpt-4o-mini-tts' : 'provider default'} /></label>
      <label><span>Speed</span><input type="number" min="0.25" max="4" step="0.05" value={providerSettings.speed || 1} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ speed: Number(event.currentTarget.value) || 1 })} />{#if validationErrors.speed}<small class="validation-error">{validationErrors.speed}</small>{/if}</label>
      <label><span>Format</span><select value={providerSettings.format || 'mp3'} disabled={disabled || playing} onchange={(event) => patchProviderSettings({ format: event.currentTarget.value })}><option value="mp3">MP3</option><option value="wav">WAV</option><option value="opus">Opus</option></select></label>

      {#if selectedProvider === 'elevenlabs'}
        <label><span>Stability</span><input type="number" min="0" max="1" step="0.05" value={providerSettings.stability || 0.5} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ stability: Number(event.currentTarget.value) })} /></label>
        <label><span>Similarity boost</span><input type="number" min="0" max="1" step="0.05" value={providerSettings.similarity_boost || 0.75} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ similarity_boost: Number(event.currentTarget.value) })} /></label>
      {:else if selectedProvider === 'azure'}
        <label><span>Locale</span><input value={providerSettings.locale || 'en-US'} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ locale: event.currentTarget.value })} /></label>
        <label><span>Style degree</span><input type="number" min="0" max="2" step="0.1" value={providerSettings.style_degree || 1} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ style_degree: Number(event.currentTarget.value) })} />{#if validationErrors.style_degree}<small class="validation-error">{validationErrors.style_degree}</small>{/if}</label>
      {:else if selectedProvider === 'local'}
        <label><span>Command</span><input value={providerSettings.command || ''} disabled={disabled || playing} oninput={(event) => patchProviderSettings({ command: event.currentTarget.value })} placeholder="piper --model …" /></label>
      {/if}

      {#if showAdvanced}
        <div class="advanced-box">
          <h4>Advanced routing</h4>
          <label><span>Persona ID</span><input value={spec.persona_id || ''} disabled={disabled || playing} oninput={(event) => patch({ persona_id: event.currentTarget.value })} placeholder="scout-voice" /></label>
        </div>
      {/if}
    </aside>
  </div>
</section>

<style>
  .studio-panel { display: grid; gap: 1rem; }
  .panel-header, .action-row { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  h3, h4, p { margin: 0; }
  p, small, .provider-list span { color: var(--text-muted); font-size: 0.85rem; }
  small.warn { color: #f59e0b; }
  .validation-error { color: #ef4444; }
  .mode-pill { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.25rem 0.6rem; color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; white-space: nowrap; }
  .studio-grid { display: grid; grid-template-columns: minmax(0, 1fr) 300px; gap: 1rem; }
  .form-stack, .settings-card, .advanced-box { display: grid; gap: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .three-col { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.75rem; }
  .provider-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.6rem; }
  .provider-list button { display: grid; gap: 0.25rem; text-align: left; }
  .provider-list button.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .settings-card, .advanced-box { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); align-content: start; }
  button { border: 1px solid var(--border-color); border-radius: 8px; background: transparent; color: var(--text-primary); padding: 0.55rem 0.8rem; font: inherit; cursor: pointer; display: inline-flex; align-items: center; justify-content: center; gap: 0.45rem; }
  .btn-primary { background: var(--primary, #6366f1); color: white; border-color: var(--primary, #6366f1); }
  .status-message, .error-message { border-radius: 8px; padding: 0.65rem 0.75rem; font-size: 0.85rem; display: flex; justify-content: space-between; gap: 0.75rem; align-items: center; }
  .status-message { background: rgba(59,130,246,0.12); color: #60a5fa; }
  .error-message { background: rgba(239,68,68,0.12); color: #ef4444; }
  .error-message button { border-color: currentColor; color: inherit; padding: 0.35rem 0.55rem; }
  .spinner { width: 0.9rem; height: 0.9rem; border: 2px solid rgba(255,255,255,0.45); border-top-color: currentColor; border-radius: 50%; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  button:disabled, input:disabled, select:disabled, textarea:disabled { opacity: 0.55; cursor: not-allowed; }
  @media (max-width: 1024px) { .studio-grid { grid-template-columns: minmax(0, 1fr) 260px; } }
  @media (max-width: 820px) { .studio-grid, .two-col, .three-col, .provider-list { grid-template-columns: 1fr; } .panel-header, .action-row, .error-message { flex-direction: column; align-items: stretch; } }
</style>
