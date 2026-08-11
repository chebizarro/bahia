<script>
  import { diffDraftContent, publishSoulAction, trackLifecycleRun, supportedRuntimeMethods } from '$lib/stores/souls.svelte.js';
  import { KINDS, SOUL_LIFECYCLE_ACTIONS, SOUL_RUNTIME_METHODS } from '$lib/nostr/client.js';

  let {
    soul = null,
    currentConfig = {},
    pendingConfig = {},
    draft = null,
    versions = [],
    disabled = false,
    onComplete = null
  } = $props();

  const sectionOptions = [
    { id: 'avatar', label: 'Avatar', method: 'soulfactory.avatar.set' },
    { id: 'voice', label: 'Voice', method: 'soulfactory.voice.configure' },
    { id: 'memory', label: 'Memory', method: 'soulfactory.memory.configure' },
    { id: 'persona', label: 'Persona', method: 'soulfactory.persona.update' }
  ];

  let selectedSections = $state(new Set(sectionOptions.map((section) => section.id)));
  let updateMode = $state('hot-reload');
  let selectedVersion = $state('');
  let running = $state(false);
  let statusMessage = $state('');
  let errorMessage = $state('');
  let rollbackAvailable = $state(false);
  let progress = $state({ current: 0, total: 0 });
  let stopTracking = null;

  const current = $derived(normalizeContent(currentConfig || soul?.content || {}));
  const pending = $derived(normalizeContent(pendingConfig || draft?.content || {}));
  const advertisedMethods = $derived(supportedRuntimeMethods({ runtime: soul?.runtime?.target || '', runtimePubkey: soul?.runtime?.runtime_pubkey || soul?.runtime?.runtimePubkey || '' }));
  const methodAdvertised = $derived((method) => advertisedMethods === null || advertisedMethods.includes(method));
  const unavailableSections = $derived(sectionOptions.filter((section) => !methodAdvertised(section.method)));
  const redeployAdvertised = $derived(methodAdvertised(SOUL_RUNTIME_METHODS.REDEPLOY));
  const reloadAdvertised = $derived(methodAdvertised('soulfactory.config.reload'));
  const changes = $derived(diffDraftContent(current, pending).filter((change) => sectionFromPath(change.path)));
  const visibleChanges = $derived(changes.filter((change) => selectedSections.has(sectionFromPath(change.path))));
  const selectedList = $derived(sectionOptions.filter((section) => selectedSections.has(section.id)));
  const hasPendingChanges = $derived(visibleChanges.length > 0);
  const validationError = $derived(!soul ? 'A deployed soul is required for live updates.' : selectedList.length === 0 ? 'Select at least one section.' : '');
  const canSubmit = $derived(Boolean(soul) && !disabled && !running && !validationError && (hasPendingChanges || updateMode !== 'hot-reload'));
  const progressPercent = $derived(progress.total > 0 ? Math.min(100, Math.round((progress.current / progress.total) * 100)) : running ? 25 : 0);

  function normalizeContent(value = {}) {
    return value?.content && typeof value.content === 'object' ? value.content : value || {};
  }

  function sectionFromPath(path = '') {
    return sectionOptions.find((section) => path === section.id || path.startsWith(`${section.id}.`))?.id || '';
  }

  function displayValue(value) {
    if (value === undefined) return '—';
    if (value === null) return 'null';
    if (typeof value === 'string') return value || '""';
    return JSON.stringify(value, null, 2);
  }

  function toggleSection(sectionId) {
    const next = new Set(selectedSections);
    if (next.has(sectionId)) next.delete(sectionId);
    else next.add(sectionId);
    selectedSections = next;
  }

  function buildPatch() {
    return selectedList.reduce((patch, section) => {
      patch[section.id] = pending?.[section.id] || null;
      return patch;
    }, {});
  }

  function draftRef() {
    return draft?.coordinate || draft?.draftRef || soul?.draftRef || '';
  }

  function draftEventId() {
    return draft?.event?.id || draft?.id || '';
  }

  function startTracking(event, action) {
    stopTracking?.();
    stopTracking = trackLifecycleRun(event.id, {
      type: 'lifecycle',
      action,
      onProgress: ({ progress: nextProgress, message }) => {
        progress = nextProgress || progress;
        statusMessage = message || 'Runtime update in progress…';
      },
      onComplete: (result) => {
        running = false;
        rollbackAvailable = true;
        statusMessage = 'Runtime update completed.';
        errorMessage = '';
        onComplete?.(result);
      },
      onError: (message) => {
        running = false;
        rollbackAvailable = true;
        errorMessage = message || 'Runtime update failed';
        statusMessage = '';
      }
    });
  }

  async function publishUpdate(action, method, extraParams = {}) {
    running = true;
    errorMessage = '';
    rollbackAvailable = false;
    progress = { current: 0, total: selectedList.length || 1 };
    statusMessage = 'Publishing SoulFactory action via Nostr…';

    try {
      const payload = {
        schema: 'soulfactory-action/v1',
        action,
        method,
        draft: draftRef(),
        draft_ref: draftRef(),
        draft_event_id: draftEventId(),
        spec_hash: draft?.specHash || pending?.spec_hash || '',
        previous_spec_hash: soul?.specHash || pending?.previous_spec_hash || '',
        requested_at: Math.floor(Date.now() / 1000),
        params: {
          sections: selectedList.map((section) => section.id),
          methods: selectedList.map((section) => section.method),
          patch: buildPatch(),
          ...extraParams
        }
      };

      const extraTags = [
        ['method', method],
        ['request-kind', String(KINDS.SOUL_ACTION)]
      ];
      if (draftRef()) extraTags.push(['draft', draftRef()]);
      if (draftEventId()) {
        extraTags.push(['draft-event', draftEventId()]);
        extraTags.push(['e', draftEventId(), '', 'draft']);
      }
      for (const section of selectedList) extraTags.push(['section', section.id]);
      if (payload.spec_hash) extraTags.push(['spec-hash', payload.spec_hash]);
      if (payload.previous_spec_hash) extraTags.push(['previous-spec-hash', payload.previous_spec_hash]);

      const result = await publishSoulAction({
        soul,
        action,
        reason: `${action} requested from Live Update panel`,
        content: payload,
        extraTags,
        beforePublish: (event) => startTracking(event, action)
      });

      statusMessage = 'Action accepted by relay. Waiting for explicit progress/result events…';
      return result;
    } catch (err) {
      running = false;
      rollbackAvailable = true;
      errorMessage = err?.message || 'Failed to publish runtime update';
      statusMessage = '';
    }
  }

  function applySelectedUpdate() {
    if (updateMode === 'restart') {
      if (!redeployAdvertised) {
        errorMessage = `Full redeploy is not advertised by the live ${soul?.runtime?.target || 'runtime'} capability.`;
        return;
      }
      if (!confirm('Full redeploy may restart the running agent. Continue?')) return;
      return publishUpdate(SOUL_LIFECYCLE_ACTIONS.REDEPLOY, SOUL_RUNTIME_METHODS.REDEPLOY, { update_mode: 'redeploy' });
    }

    if (!reloadAdvertised) {
      errorMessage = `Hot-reload is not advertised by the live ${soul?.runtime?.target || 'runtime'} capability.`;
      return;
    }
    return publishUpdate('hot-reload', 'soulfactory.config.reload', { update_mode: 'hot-reload' });
  }

  function rollback() {
    const version = selectedVersion || versions[0]?.id || versions[0]?.ref || '';
    return publishUpdate('rollback', 'soulfactory.config.reload', { rollback_ref: version, update_mode: 'rollback' });
  }

  function retryUpdate() {
    return applySelectedUpdate();
  }
</script>

<section class="live-update-panel" aria-busy={running}>
  <div class="panel-header">
    <div>
      <h3>Live Update</h3>
      <p>Review draft changes and publish event-driven runtime control actions. Completion is driven by progress/result events.</p>
    </div>
    <span class="status-pill" class:running>{running ? 'Updating' : hasPendingChanges ? 'Pending changes' : 'Up to date'}</span>
  </div>

  <div class="status-grid">
    <div><span>Current spec</span><strong>{soul?.specHash || current?.spec_hash || 'unknown'}</strong></div>
    <div><span>Pending spec</span><strong>{draft?.specHash || pending?.spec_hash || 'unsaved draft'}</strong></div>
    <div><span>Runtime</span><strong>{soul?.runtime?.target || pending?.runtime?.target || 'unbound'}</strong></div>
  </div>

  <div class="controls">
    <fieldset disabled={disabled || running}>
      <legend>Sections</legend>
      <div class="checkbox-row">
        {#each sectionOptions as section}
          <label class:muted={!methodAdvertised(section.method)} title={methodAdvertised(section.method) ? '' : `Not advertised by the live ${soul?.runtime?.target || 'runtime'} capability`}><input type="checkbox" checked={selectedSections.has(section.id)} disabled={!methodAdvertised(section.method)} onchange={() => toggleSection(section.id)} /> {section.label}</label>
        {/each}
      </div>
    </fieldset>

    <fieldset disabled={disabled || running}>
      <legend>Update mode</legend>
      <label title={reloadAdvertised ? '' : `Not advertised by the live ${soul?.runtime?.target || 'runtime'} capability`}><input type="radio" name="update-mode" value="hot-reload" disabled={!reloadAdvertised} bind:group={updateMode} /> Hot-reload without restart</label>
      <label title={redeployAdvertised ? '' : `Not advertised by the live ${soul?.runtime?.target || 'runtime'} capability`}><input type="radio" name="update-mode" value="restart" disabled={!redeployAdvertised} bind:group={updateMode} /> Full redeploy / restart</label>
    </fieldset>
    {#if unavailableSections.length > 0 || !redeployAdvertised || !reloadAdvertised}
      <p class="capability-note">Some controls are disabled because the live {soul?.runtime?.target || 'runtime'} capability does not advertise their methods{#if unavailableSections.length > 0}: {unavailableSections.map((section) => section.label).join(', ')}{/if}.</p>
    {/if}
  </div>

  {#if validationError}
    <div class="validation-card">{validationError}</div>
  {/if}

  <div class="diff-card">
    <div class="diff-header">
      <strong>Draft diff</strong>
      <span>{visibleChanges.length} selected change{visibleChanges.length === 1 ? '' : 's'}</span>
    </div>

    {#if visibleChanges.length === 0}
      <p class="empty">No selected avatar, voice, memory, or persona changes.</p>
    {:else}
      <div class="diff-table">
        <div class="diff-row heading"><span>Path</span><span>Current</span><span>Pending</span></div>
        {#each visibleChanges as change}
          <div class="diff-row">
            <code>{change.path}</code>
            <pre>{displayValue(change.before)}</pre>
            <pre>{displayValue(change.after)}</pre>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  {#if running || statusMessage}
    <div class="progress-card">
      <div class="progress-bar"><span style={`width: ${progressPercent}%`}></span></div>
      <p>{#if running}<span class="spinner" aria-hidden="true"></span>{/if}{statusMessage || 'Waiting for runtime control events…'}</p>
    </div>
  {/if}

  {#if errorMessage}
    <div class="error-card">
      <strong>Update failed</strong>
      <p>{errorMessage}</p>
      <div class="error-actions"><button type="button" disabled={disabled || running || !canSubmit} onclick={retryUpdate}>Retry</button><button type="button" disabled={disabled || running || (!rollbackAvailable && versions.length === 0)} onclick={rollback}>Rollback</button></div>
    </div>
  {/if}

  <div class="action-row">
    <button type="button" class="btn-primary" disabled={!canSubmit} onclick={applySelectedUpdate}>{#if running}<span class="spinner" aria-hidden="true"></span>{/if}{updateMode === 'restart' ? 'Full redeploy' : 'Hot-reload changes'}</button>

    <label class="version-picker">Rollback version
      <select bind:value={selectedVersion} disabled={disabled || running || versions.length === 0}>
        <option value="">Previous version</option>
        {#each versions as version}
          <option value={version.id || version.ref || version.specHash}>{version.label || version.specHash || version.id || version.ref}</option>
        {/each}
      </select>
    </label>
    <button type="button" disabled={disabled || running || (!rollbackAvailable && versions.length === 0)} onclick={rollback}>Rollback</button>
  </div>
</section>

<style>
  .live-update-panel { display: grid; gap: 1rem; }
  .panel-header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  h3, p { margin: 0; }
  p, .empty { color: var(--text-muted); font-size: 0.85rem; }
  .status-pill { border: 1px solid var(--border-color); border-radius: 999px; padding: 0.25rem 0.65rem; color: var(--text-muted); font-size: 0.8rem; white-space: nowrap; }
  .status-pill.running { color: var(--primary, #6366f1); border-color: currentColor; }
  .status-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.75rem; }
  .status-grid > div, .diff-card, .progress-card, .error-card, .validation-card { border: 1px solid var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  .status-grid span { display: block; color: var(--text-muted); font-size: 0.75rem; margin-bottom: 0.25rem; }
  .status-grid strong { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 0.85rem; }
  .controls { display: grid; grid-template-columns: 1.2fr 1fr; gap: 0.75rem; }
  fieldset { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.75rem; display: grid; gap: 0.55rem; }
  legend { color: var(--text-muted); font-size: 0.8rem; padding: 0 0.25rem; }
  label { color: var(--text-primary); font-size: 0.85rem; }
  .checkbox-row { display: flex; flex-wrap: wrap; gap: 0.75rem; }
  .diff-header, .action-row { display: flex; justify-content: space-between; gap: 0.75rem; align-items: center; }
  .diff-table { display: grid; gap: 0.5rem; margin-top: 0.75rem; }
  .diff-row { display: grid; grid-template-columns: 0.8fr 1fr 1fr; gap: 0.5rem; align-items: stretch; }
  .diff-row.heading { color: var(--text-muted); font-size: 0.75rem; }
  code, pre { margin: 0; border-radius: 8px; background: rgba(0,0,0,0.18); padding: 0.55rem; overflow: auto; font-size: 0.78rem; }
  pre { white-space: pre-wrap; max-height: 12rem; }
  .progress-bar { height: 0.5rem; border-radius: 999px; background: rgba(255,255,255,0.08); overflow: hidden; }
  .progress-bar span { display: block; height: 100%; background: var(--primary, #6366f1); transition: width 160ms ease; }
  .progress-card p, .error-actions { display: flex; align-items: center; gap: 0.5rem; }
  .error-card, .validation-card { border-color: rgba(239, 68, 68, 0.45); }
  .error-card strong, .validation-card { color: #ef4444; }
  button, select { border: 1px solid var(--border-color); border-radius: 8px; background: transparent; color: var(--text-primary); padding: 0.55rem 0.8rem; font: inherit; }
  button { display: inline-flex; align-items: center; justify-content: center; gap: 0.45rem; }
  .btn-primary { background: var(--primary, #6366f1); color: white; border-color: var(--primary, #6366f1); }
  button:disabled, select:disabled, fieldset:disabled { opacity: 0.55; cursor: not-allowed; }
  .spinner { width: 0.9rem; height: 0.9rem; border: 2px solid rgba(255,255,255,0.45); border-top-color: currentColor; border-radius: 50%; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .capability-note { grid-column: 1 / -1; color: var(--text-muted); font-size: 0.8rem; }
  .version-picker { display: inline-flex; gap: 0.5rem; align-items: center; margin-left: auto; }
  @media (max-width: 1024px) { .diff-row { grid-template-columns: 0.7fr 1fr 1fr; } }
  @media (max-width: 820px) { .status-grid, .controls, .diff-row { grid-template-columns: 1fr; } .panel-header, .action-row, .error-actions { flex-direction: column; align-items: stretch; } .version-picker { margin-left: 0; align-items: stretch; flex-direction: column; } }
</style>
