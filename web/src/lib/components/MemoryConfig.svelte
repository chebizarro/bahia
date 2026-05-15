<script>
  import {
    createDefaultMemorySpec,
    memoryProviders,
    publishSoulAction,
    trackLifecycleRun
  } from '$lib/stores/souls.svelte.js';
  import { KINDS } from '$lib/nostr/client.js';

  /** @typedef {import('$lib/types/customization').SoulMemorySpec} SoulMemorySpec */

  let {
    value = $bindable(),
    showAdvanced = false,
    soul = null,
    draft = null,
    disabled = false,
    onReindexComplete = null
  } = $props();

  const strategies = [
    { id: 'session-aware', label: 'Session-aware', description: 'Blend recent session context with durable memory for active conversations.' },
    { id: 'long-term', label: 'Long-term', description: 'Favor durable facts and workspace knowledge across many sessions.' },
    { id: 'ephemeral', label: 'Ephemeral', description: 'Prefer short-lived context unless an event explicitly marks memory for retention.' }
  ];
  const rerankModels = ['cohere-rerank-v3', 'rerank-english-v3.0', 'bge-reranker-large', 'local-reranker'];

  let reindexing = $state(false);
  let reindexMessage = $state('');
  let reindexError = $state('');
  let reindexProgress = $state({ current: 0, total: 0 });
  let stopTracking = null;

  const spec = $derived(createDefaultMemorySpec(value || {}));
  const search = $derived(spec.search || createDefaultMemorySpec().search);
  const selectedProvider = $derived(memoryProviders.find((provider) => provider.id === spec.embedding_provider) || memoryProviders[0]);
  const providerModels = $derived(selectedProvider?.models || []);
  const selectedStrategy = $derived(strategies.find((strategy) => strategy.id === spec.strategy) || strategies[0]);
  const progressPercent = $derived(reindexProgress.total > 0 ? Math.min(100, Math.round((reindexProgress.current / reindexProgress.total) * 100)) : reindexing ? 15 : 0);
  const canReindex = $derived(Boolean(soul) && !disabled && !reindexing);

  function patch(updates) {
    value = createDefaultMemorySpec({
      ...(value || {}),
      ...updates
    });
  }

  function patchSearch(updates) {
    patch({
      search: {
        ...createDefaultMemorySpec().search,
        ...(value?.search || {}),
        ...updates
      }
    });
  }

  function handleProviderChange(providerId) {
    const provider = memoryProviders.find((candidate) => candidate.id === providerId);
    patch({
      embedding_provider: providerId,
      embedding_model: provider?.models?.[0] || ''
    });
  }

  function draftRef() {
    return draft?.coordinate || draft?.draftRef || soul?.draftRef || '';
  }

  function draftEventId() {
    return draft?.event?.id || draft?.id || '';
  }

  function startTracking(event) {
    stopTracking?.();
    stopTracking = trackLifecycleRun(event.id, {
      type: 'lifecycle',
      action: 'memory-reindex',
      onProgress: ({ progress, message }) => {
        reindexProgress = progress || reindexProgress;
        reindexMessage = message || 'Memory reindex in progress…';
      },
      onComplete: (result) => {
        reindexing = false;
        reindexProgress = { current: 1, total: 1 };
        reindexMessage = 'Memory reindex completed.';
        reindexError = '';
        onReindexComplete?.(result);
      },
      onError: (message) => {
        reindexing = false;
        reindexError = message || 'Memory reindex failed';
        reindexMessage = '';
      }
    });
  }

  async function triggerReindex() {
    if (!canReindex) return;

    reindexing = true;
    reindexError = '';
    reindexProgress = { current: 0, total: 1 };
    reindexMessage = 'Publishing memory reindex action via Nostr…';

    try {
      const payload = {
        schema: 'soulfactory-action/v1',
        action: 'memory-reindex',
        method: 'soulfactory.memory.reindex',
        draft: draftRef(),
        draft_ref: draftRef(),
        draft_event_id: draftEventId(),
        requested_at: Math.floor(Date.now() / 1000),
        params: {
          memory: spec,
          section: 'memory'
        }
      };

      const extraTags = [
        ['method', 'soulfactory.memory.reindex'],
        ['request-kind', String(KINDS.SOUL_ACTION)],
        ['section', 'memory']
      ];
      if (draftRef()) extraTags.push(['draft', draftRef()]);
      if (draftEventId()) {
        extraTags.push(['draft-event', draftEventId()]);
        extraTags.push(['e', draftEventId(), '', 'draft']);
      }

      await publishSoulAction({
        soul,
        action: 'memory-reindex',
        reason: 'Memory reindex requested from Memory Configuration panel',
        content: payload,
        extraTags,
        beforePublish: startTracking
      });

      reindexMessage = 'Reindex action accepted by relay. Waiting for explicit progress/result events…';
    } catch (err) {
      reindexing = false;
      reindexError = err?.message || 'Failed to publish memory reindex action';
      reindexMessage = '';
    }
  }
</script>

<section class="studio-panel" aria-busy={reindexing}>
  <div class="panel-header">
    <div>
      <h3>Memory Configuration</h3>
      <p>Configure retrieval defaults saved into the soul draft and publish event-driven memory runtime actions.</p>
    </div>
    <span class="mode-pill">{selectedStrategy.label}</span>
  </div>

  <div class="form-stack">
    <div class="two-col">
      <label>
        <span>Embedding provider</span>
        <select value={spec.embedding_provider} disabled={disabled || reindexing} onchange={(event) => handleProviderChange(event.currentTarget.value)}>
          {#each memoryProviders as provider}
            <option value={provider.id}>{provider.label}</option>
          {/each}
        </select>
        <small>{selectedProvider?.description || 'Select the embedding backend for memory indexing.'}</small>
      </label>

      <label>
        <span>Embedding model</span>
        {#if providerModels.length > 0}
          <select value={spec.embedding_model} disabled={disabled || reindexing} onchange={(event) => patch({ embedding_model: event.currentTarget.value })}>
            {#each providerModels as model}
              <option value={model}>{model}</option>
            {/each}
          </select>
        {:else}
          <input value={spec.embedding_model || ''} disabled={disabled || reindexing} oninput={(event) => patch({ embedding_model: event.currentTarget.value })} placeholder="local model id" />
        {/if}
      </label>
    </div>

    <div>
      <h4>Memory strategy</h4>
      <div class="strategy-grid" aria-label="Memory strategies">
        {#each strategies as strategy}
          <button type="button" class="strategy-card" class:selected={spec.strategy === strategy.id} disabled={disabled || reindexing} onclick={() => patch({ strategy: strategy.id })}>
            <strong>{strategy.label}</strong>
            <span>{strategy.description}</span>
          </button>
        {/each}
      </div>
    </div>

    <div class="range-grid">
      <label>
        <span>Top K</span>
        <div class="range-row">
          <input type="range" min="1" max="50" step="1" value={search.top_k || 10} disabled={disabled || reindexing} oninput={(event) => patchSearch({ top_k: Number(event.currentTarget.value) || 10 })} />
          <input class="number-input" type="number" min="1" max="50" value={search.top_k || 10} disabled={disabled || reindexing} oninput={(event) => patchSearch({ top_k: Number(event.currentTarget.value) || 10 })} />
        </div>
      </label>

      <label>
        <span>Score threshold</span>
        <div class="range-row">
          <input type="range" min="0" max="1" step="0.05" value={search.score_threshold ?? 0.7} disabled={disabled || reindexing} oninput={(event) => patchSearch({ score_threshold: Number(event.currentTarget.value) })} />
          <input class="number-input" type="number" min="0" max="1" step="0.05" value={search.score_threshold ?? 0.7} disabled={disabled || reindexing} oninput={(event) => patchSearch({ score_threshold: Number(event.currentTarget.value) })} />
        </div>
      </label>
    </div>

    <label class="checkbox-row">
      <input type="checkbox" checked={spec.auto_index} disabled={disabled || reindexing} onchange={(event) => patch({ auto_index: event.currentTarget.checked })} />
      Auto-index new workspace and conversation memory
    </label>

    <div class="advanced-box">
      <div class="split-header">
        <div>
          <h4>Reranking</h4>
          <p>Optionally rerank retrieved chunks after vector search for higher precision.</p>
        </div>
        <label class="toggle-row"><input type="checkbox" checked={search.rerank || false} disabled={disabled || reindexing} onchange={(event) => patchSearch({ rerank: event.currentTarget.checked })} /> Enabled</label>
      </div>

      {#if search.rerank || showAdvanced}
        <div class="two-col">
          <label>
            <span>Rerank model</span>
            <select value={search.rerank_model || rerankModels[0]} disabled={disabled || reindexing || !search.rerank} onchange={(event) => patchSearch({ rerank_model: event.currentTarget.value })}>
              {#each rerankModels as model}
                <option value={model}>{model}</option>
              {/each}
            </select>
          </label>
          <label>
            <span>Retention days</span>
            <input type="number" min="0" value={spec.retention_days || 90} disabled={disabled || reindexing} oninput={(event) => patch({ retention_days: Number(event.currentTarget.value) || 0 })} />
          </label>
        </div>
      {/if}
    </div>

    <div class="reindex-card">
      <div class="split-header">
        <div>
          <h4>Reindex memory</h4>
          <p>Publishes a SoulFactory runtime control event and tracks progress from explicit result events.</p>
        </div>
        <button type="button" class="btn-primary" disabled={!canReindex} onclick={triggerReindex}>{reindexing ? 'Reindexing…' : 'Trigger reindex'}</button>
      </div>
      <div class="progress-track" aria-label="Reindex progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow={progressPercent} role="progressbar">
        <span style={`width: ${progressPercent}%`}></span>
      </div>
      {#if reindexMessage}<div class="status-message">{reindexMessage}</div>{/if}
      {#if reindexError}<div class="error-message">{reindexError}</div>{/if}
      {#if !soul}<small>Save or open a deployed soul to trigger runtime reindexing.</small>{/if}
    </div>
  </div>
</section>

<style>
  .studio-panel, .form-stack, .advanced-box, .reindex-card { display: grid; gap: 0.9rem; }
  .panel-header, .split-header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  h3, h4, p { margin: 0; }
  p, small { color: var(--text-muted); font-size: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  input[type='range'] { padding: 0; accent-color: var(--primary); }
  input:disabled, select:disabled, button:disabled { opacity: 0.6; cursor: not-allowed; }
  .two-col, .range-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .range-row { display: grid; grid-template-columns: 1fr 5rem; gap: 0.6rem; align-items: center; }
  .number-input { text-align: right; }
  .strategy-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.6rem; margin-top: 0.5rem; }
  .strategy-card { text-align: left; display: grid; gap: 0.25rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 10px; padding: 0.75rem; cursor: pointer; }
  .strategy-card span { color: var(--text-muted); font-size: 0.78rem; }
  .strategy-card.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .checkbox-row, .toggle-row { display: flex; align-items: center; gap: 0.5rem; }
  .checkbox-row input, .toggle-row input { width: auto; }
  .advanced-box, .reindex-card { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  .mode-pill { border: 1px solid var(--border-color); border-radius: 999px; color: var(--text-muted); font-size: 0.78rem; padding: 0.25rem 0.55rem; white-space: nowrap; }
  .btn-primary { border: 1px solid var(--primary); border-radius: 8px; background: var(--primary); color: white; padding: 0.55rem 0.85rem; }
  .progress-track { height: 0.55rem; border-radius: 999px; overflow: hidden; background: var(--bg); border: 1px solid var(--border-color); }
  .progress-track span { display: block; height: 100%; background: var(--primary); transition: width 160ms ease; }
  .status-message { color: var(--success, #42d392); font-size: 0.85rem; }
  .error-message { color: var(--error, #ff6b6b); font-size: 0.85rem; }
  @media (max-width: 760px) { .two-col, .range-grid, .strategy-grid { grid-template-columns: 1fr; } .panel-header, .split-header { display: grid; } }
</style>
