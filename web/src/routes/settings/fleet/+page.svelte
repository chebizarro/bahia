<script>
  import { onMount, untrack } from 'svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { toast } from '$lib/components/toast.js';
  import { authState } from '$lib/stores/auth.js';
  import {
    fleetRolloutStore,
    summarizeFleetRollout
  } from '$lib/stores/fleet-rollout.svelte.js';
  import {
    souls,
    subscribeToSoulFactoryUpdates,
    unsubscribeFromSoulUpdates
  } from '$lib/stores/souls.svelte.js';
  import {
    FLEET_CONFIG_ALLOWED_SECTIONS,
    emptyFleetConfigDocument,
    fleetConfigDiff,
    fleetConfigStore,
    validateFleetConfigDocument
  } from '$lib/stores/fleet-config.svelte.js';

  const storeState = fleetConfigStore.state;
  const rolloutState = fleetRolloutStore.state;
  let document = $state(emptyFleetConfigDocument());
  let sectionText = $state({});
  let rawText = $state('');
  let rawMode = $state(false);
  let dirty = $state(false);
  let editorError = $state('');
  let publishing = $state(false);
  let retryingSoulId = $state('');

  const validation = $derived(validateFleetConfigDocument(document));
  const changes = $derived(fleetConfigDiff(storeState.document, document));
  const canPublish = $derived(authState.status === 'authenticated' && validation.valid && !editorError && !publishing && changes.length > 0);
  const rolloutSummary = $derived(summarizeFleetRollout(rolloutState.souls));

  onMount(() => {
    const unsubscribeFleetConfig = fleetConfigStore.subscribe();
    subscribeToSoulFactoryUpdates();
    return () => {
      unsubscribeFleetConfig();
      fleetRolloutStore.stop();
      unsubscribeFromSoulUpdates();
    };
  });

  $effect(() => {
    const revision = storeState.event?.id || '';
    const operatorPubkey = authState.pubkey || '';
    const currentSouls = souls.map((soul) => soul);
    untrack(() => fleetRolloutStore.track({ revision, souls: currentSouls, operatorPubkey }));
  });

  $effect(() => {
    if (!dirty && storeState.document) hydrate(storeState.document);
  });

  $effect(() => {
    if (!dirty && !storeState.document) hydrate(emptyFleetConfigDocument());
  });

  function hydrate(next) {
    document = structuredClone(next);
    rawText = JSON.stringify(document, null, 2);
    sectionText = {
      defaults: JSON.stringify(document.defaults || {}, null, 2),
      ...Object.fromEntries(FLEET_CONFIG_ALLOWED_SECTIONS.map((section) => [
        section,
        document.template?.[section] === undefined ? '' : JSON.stringify(document.template[section], null, 2)
      ]))
    };
    editorError = '';
  }

  function updateSection(section, value) {
    sectionText[section] = value;
    dirty = true;
    editorError = '';
    try {
      const next = structuredClone(document);
      if (section === 'defaults') {
        next.defaults = value.trim() ? JSON.parse(value) : {};
      } else if (value.trim()) {
        next.template[section] = JSON.parse(value);
      } else {
        delete next.template[section];
      }
      document = next;
      rawText = JSON.stringify(next, null, 2);
    } catch (error) {
      editorError = `${section}: ${error?.message || 'invalid JSON'}`;
    }
  }

  function updateRaw(value) {
    rawText = value;
    dirty = true;
    editorError = '';
    try {
      document = JSON.parse(value);
    } catch (error) {
      editorError = error?.message || 'Invalid JSON';
    }
  }

  function toggleRaw() {
    rawMode = !rawMode;
    if (rawMode) rawText = JSON.stringify(document, null, 2);
    else if (!editorError) hydrate(document);
  }

  function resetEditor() {
    dirty = false;
    hydrate(storeState.document || emptyFleetConfigDocument());
  }

  async function retryRollout(soul) {
    if (!storeState.document || soul.status !== 'failed') return;
    retryingSoulId = soul.agentId;
    publishing = true;
    try {
      const result = await fleetConfigStore.publish(storeState.document);
      const accepted = result.publishResults.filter((item) => item.accepted).length;
      toast.success(`Published a new fleet revision to ${accepted} relay${accepted === 1 ? '' : 's'}; reconciliation will run again for unapplied souls.`);
    } catch (error) {
      toast.error(error?.message || 'Failed to re-trigger fleet reconciliation');
    } finally {
      publishing = false;
      retryingSoulId = '';
    }
  }

  function shortRevision(revision) {
    if (!revision) return 'none';
    return revision.length > 16 ? `${revision.slice(0, 8)}…${revision.slice(-8)}` : revision;
  }

  async function publishConfig() {
    if (!validation.valid || editorError) {
      toast.error('Fix fleet configuration validation errors before publishing');
      return;
    }
    publishing = true;
    try {
      const result = await fleetConfigStore.publish(document);
      dirty = false;
      hydrate(document);
      const accepted = result.publishResults.filter((item) => item.accepted).length;
      toast.success(`Fleet configuration published to ${accepted} relay${accepted === 1 ? '' : 's'}`);
    } catch (error) {
      editorError = error?.message || 'Failed to publish fleet configuration';
      toast.error(editorError);
    } finally {
      publishing = false;
    }
  }
</script>

<div class="page">
  <header>
    <a class="back-link" href="/settings">← Settings</a>
    <h1>OpenClaw Fleet Configuration</h1>
    <p>Publish the trusted kind 31953 template used for new provisions and automatically reconcile deployed OpenClaw souls.</p>
  </header>

  {#if authState.status !== 'authenticated'}
    <div class="status error">Sign in with a trusted Soul Factory operator key before publishing.</div>
  {/if}
  {#if storeState.loading}<div class="status">Loading the latest operator-authored fleet document…</div>{/if}
  {#if storeState.error}<div class="status error">{storeState.error}</div>{/if}
  {#if storeState.event}
    <div class="status">
      Loaded <code>{storeState.event.id}</code> from {new Date(storeState.event.created_at * 1000).toLocaleString()}.
    </div>
  {/if}

  {#if storeState.event}
    <section class="rollout-card" aria-live="polite">
      <div class="rollout-heading">
        <div>
          <h2>Fleet rollout</h2>
          <p>Revision <code title={rolloutState.revision}>{shortRevision(rolloutState.revision)}</code></p>
        </div>
        <div class="rollout-summary">
          <span class="status-pill pending">{rolloutSummary.pending} pending</span>
          <span class="status-pill reloading">{rolloutSummary.reloading} reloading</span>
          <span class="status-pill ok">{rolloutSummary.ok} ok</span>
          <span class="status-pill failed">{rolloutSummary.failed} failed</span>
        </div>
      </div>

      {#if rolloutState.loading}<p class="muted">Loading retained rollout events and soul read models…</p>{/if}
      {#if rolloutState.error}<div class="rollout-error" role="alert">{rolloutState.error}</div>{/if}
      {#if rolloutState.souls.length === 0}
        <p class="muted">No active deployed OpenClaw souls are eligible for this revision.</p>
      {:else}
        <div class="rollout-list">
          {#each rolloutState.souls as soul (soul.agentId)}
            <div class="rollout-row">
              <div class="soul-name">
                <strong>{soul.name}</strong>
                <code>{soul.agentId}</code>
              </div>
              <span class="status-pill {soul.status}">{soul.status}</span>
              <div class="rollout-detail">
                <span>{soul.message}</span>
                <small>Applied: <code title={soul.appliedRevision}>{shortRevision(soul.appliedRevision)}</code></small>
              </div>
              {#if soul.status === 'failed'}
                <LoadingButton
                  loading={retryingSoulId === soul.agentId}
                  disabled={publishing && retryingSoulId !== soul.agentId}
                  onclick={() => retryRollout(soul)}
                >Retry rollout</LoadingButton>
              {/if}
            </div>
          {/each}
        </div>
        {#if rolloutSummary.failed > 0}
          <p class="muted">Retry publishes a new revision of the current fleet document; the backend re-evaluates every soul that has not applied that revision.</p>
        {/if}
      {/if}
    </section>
  {/if}

  <div class="toolbar">
    <button type="button" class:active={rawMode} onclick={toggleRaw}>{rawMode ? 'Section editor' : 'Raw JSON'}</button>
    <button type="button" onclick={resetEditor} disabled={!dirty}>Reset</button>
    <LoadingButton variant="primary" loading={publishing} disabled={!canPublish} onclick={publishConfig}>Publish Fleet Config</LoadingButton>
  </div>

  {#if rawMode}
    <section class="editor-card">
      <h2>Raw document</h2>
      <p>The envelope requires <code>schema</code>, <code>template</code>, and optional environment-fallback overrides in <code>defaults</code>.</p>
      <textarea rows="28" value={rawText} oninput={(event) => updateRaw(event.currentTarget.value)} spellcheck="false"></textarea>
    </section>
  {:else}
    <section class="editor-card">
      <h2>Environment fallback overrides</h2>
      <p>These replace <code>OPENCLAW_SOULFACTORY_DEFAULT_MODEL</code>, default bindings, and required plugins when populated.</p>
      <textarea rows="8" value={sectionText.defaults || ''} oninput={(event) => updateSection('defaults', event.currentTarget.value)} spellcheck="false"></textarea>
    </section>

    <div class="section-grid">
      {#each FLEET_CONFIG_ALLOWED_SECTIONS as section}
        <details class="editor-card" open={Boolean(sectionText[section])}>
          <summary>{section}</summary>
          <textarea
            rows="10"
            value={sectionText[section] || ''}
            oninput={(event) => updateSection(section, event.currentTarget.value)}
            placeholder="Leave empty to omit this section"
            spellcheck="false"
          ></textarea>
        </details>
      {/each}
    </div>
  {/if}

  {#if editorError}<div class="status error" role="alert">{editorError}</div>{/if}
  {#if !validation.valid}
    <div class="status error" role="alert">
      <strong>Validation</strong>
      <ul>{#each validation.errors as error}<li>{error}</li>{/each}</ul>
    </div>
  {/if}

  <section class="diff-card">
    <h2>Diff preview</h2>
    {#if changes.length === 0}
      <p>No changes from the latest loaded document.</p>
    {:else}
      {#each changes as change}
        <details>
          <summary>{change.section}</summary>
          <div class="diff-columns">
            <div><h3>Published</h3><pre>{JSON.stringify(change.before, null, 2) || '—'}</pre></div>
            <div><h3>Proposed</h3><pre>{JSON.stringify(change.after, null, 2) || '—'}</pre></div>
          </div>
        </details>
      {/each}
    {/if}
  </section>

  {#if storeState.publishResults.length > 0}
    <section class="diff-card">
      <h2>Relay OK outcomes</h2>
      <ul>
        {#each storeState.publishResults as result}
          <li class:accepted={result.accepted}>{result.relay}: {result.accepted ? 'accepted' : result.message || 'rejected'}</li>
        {/each}
      </ul>
    </section>
  {/if}
</div>

<style>
  .page { display: grid; gap: 1rem; }
  header p, .editor-card p, .diff-card p { color: var(--text-muted); }
  h1, h2, h3 { margin: 0; }
  h1 { font-size: 1.75rem; }
  h2 { font-size: 1rem; }
  h3 { font-size: 0.8rem; color: var(--text-muted); }
  .back-link { display: inline-flex; margin-bottom: 0.75rem; color: var(--text-muted); text-decoration: none; }
  .toolbar { display: flex; gap: 0.65rem; justify-content: flex-end; flex-wrap: wrap; }
  button { border: 1px solid var(--border-color); border-radius: 8px; background: var(--card-bg); color: var(--text-primary); padding: 0.55rem 0.8rem; }
  button.active { border-color: var(--primary); }
  button:disabled { opacity: 0.55; }
  .status, .editor-card, .diff-card, .rollout-card { border: 1px solid var(--border-color); border-radius: 10px; background: var(--card-bg); padding: 1rem; }
  .status.error { border-color: #ef4444; color: #ef4444; }
  .section-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.8rem; }
  .editor-card { display: grid; gap: 0.65rem; }
  summary { cursor: pointer; font-weight: 600; }
  textarea, pre { width: 100%; box-sizing: border-box; border: 1px solid var(--border-color); border-radius: 8px; background: var(--bg); color: var(--text-primary); padding: 0.75rem; font: 0.8rem/1.45 ui-monospace, monospace; overflow: auto; }
  textarea { resize: vertical; }
  .diff-card, .rollout-card { display: grid; gap: 0.75rem; }
  .rollout-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .rollout-heading p, .muted { margin: 0.35rem 0 0; color: var(--text-muted); }
  .rollout-summary { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 0.4rem; }
  .status-pill { display: inline-flex; align-items: center; width: fit-content; border-radius: 999px; padding: 0.2rem 0.55rem; font-size: 0.75rem; font-weight: 700; text-transform: capitalize; background: color-mix(in srgb, var(--text-muted) 15%, transparent); }
  .status-pill.pending { color: var(--text-muted); }
  .status-pill.reloading { color: #3b82f6; background: color-mix(in srgb, #3b82f6 14%, transparent); }
  .status-pill.ok { color: #22c55e; background: color-mix(in srgb, #22c55e 14%, transparent); }
  .status-pill.failed { color: #ef4444; background: color-mix(in srgb, #ef4444 14%, transparent); }
  .rollout-list { display: grid; border-top: 1px solid var(--border-color); }
  .rollout-row { display: grid; grid-template-columns: minmax(10rem, 1fr) auto minmax(14rem, 2fr) auto; gap: 0.75rem; align-items: center; padding: 0.75rem 0; border-bottom: 1px solid var(--border-color); }
  .soul-name, .rollout-detail { display: grid; gap: 0.2rem; min-width: 0; }
  .soul-name code, .rollout-detail small { color: var(--text-muted); }
  .rollout-detail span { overflow-wrap: anywhere; }
  .rollout-error { color: #ef4444; }
  .diff-columns { display: grid; grid-template-columns: 1fr 1fr; gap: 0.75rem; margin-top: 0.65rem; }
  .accepted { color: #22c55e; }
  @media (max-width: 800px) {
    .section-grid, .diff-columns { grid-template-columns: 1fr; }
    .rollout-heading { display: grid; }
    .rollout-summary { justify-content: flex-start; }
    .rollout-row { grid-template-columns: 1fr auto; }
    .rollout-detail { grid-column: 1 / -1; }
  }
</style>
