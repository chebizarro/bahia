<script>
  let { value = $bindable(), showAdvanced = false } = $props();

  const providers = ['openai', 'voyage', 'cohere', 'local'];
  const strategies = [
    { id: 'session-aware', label: 'Session-aware', description: 'Blend recent session context with durable memory.' },
    { id: 'long-term', label: 'Long-term', description: 'Optimize for durable recall across conversations.' },
    { id: 'ephemeral', label: 'Ephemeral', description: 'Keep memory short-lived unless explicitly retained.' }
  ];
  const search = $derived(value?.search || {});

  function patch(updates) {
    value = {
      embedding_provider: 'openai',
      embedding_model: 'text-embedding-3-small',
      search: {
        top_k: 10,
        score_threshold: 0.7,
        rerank: false,
        rerank_model: ''
      },
      strategy: 'session-aware',
      auto_index: true,
      retention_days: 90,
      ...(value || {}),
      ...updates
    };
  }

  function patchSearch(updates) {
    patch({
      search: {
        top_k: 10,
        score_threshold: 0.7,
        rerank: false,
        rerank_model: '',
        ...(value?.search || {}),
        ...updates
      }
    });
  }
</script>

<section class="studio-panel">
  <div>
    <h3>Memory Config</h3>
    <p>Set the memory strategy and retrieval defaults that will be applied during provisioning.</p>
  </div>

  <div class="form-stack">
    <div class="two-col">
      <label>Embedding provider<select value={value?.embedding_provider || 'openai'} onchange={(event) => patch({ embedding_provider: event.currentTarget.value })}>{#each providers as provider}<option value={provider}>{provider}</option>{/each}</select></label>
      <label>Embedding model<input value={value?.embedding_model || ''} oninput={(event) => patch({ embedding_model: event.currentTarget.value })} placeholder="text-embedding-3-small" /></label>
    </div>

    <div class="strategy-grid">
      {#each strategies as strategy}
        <button type="button" class="strategy-card" class:selected={(value?.strategy || 'session-aware') === strategy.id} onclick={() => patch({ strategy: strategy.id })}>
          <strong>{strategy.label}</strong>
          <span>{strategy.description}</span>
        </button>
      {/each}
    </div>

    <div class="two-col">
      <label>Top K<input type="number" min="1" max="50" value={search.top_k || 10} oninput={(event) => patchSearch({ top_k: Number(event.currentTarget.value) || 10 })} /></label>
      <label>Score threshold<input type="number" min="0" max="1" step="0.05" value={search.score_threshold ?? 0.7} oninput={(event) => patchSearch({ score_threshold: Number(event.currentTarget.value) })} /></label>
    </div>

    <label class="checkbox-row"><input type="checkbox" checked={value?.auto_index ?? true} onchange={(event) => patch({ auto_index: event.currentTarget.checked })} /> Auto-index new workspace and conversation memory</label>

    {#if showAdvanced}
      <div class="advanced-box">
        <h4>Advanced retrieval</h4>
        <label class="checkbox-row"><input type="checkbox" checked={search.rerank || false} onchange={(event) => patchSearch({ rerank: event.currentTarget.checked })} /> Enable reranking</label>
        <div class="two-col">
          <label>Rerank model<input value={search.rerank_model || ''} oninput={(event) => patchSearch({ rerank_model: event.currentTarget.value })} placeholder="cohere-rerank-v3" /></label>
          <label>Retention days<input type="number" min="0" value={value?.retention_days || 90} oninput={(event) => patch({ retention_days: Number(event.currentTarget.value) || 0 })} /></label>
        </div>
      </div>
    {/if}

    <div class="sample-card">
      <strong>Reindex placeholder</strong>
      <p>Reindex will become an event-driven runtime-control action; no polling or REST is used here.</p>
      <button type="button" disabled>Trigger reindex</button>
    </div>
  </div>
</section>

<style>
  .studio-panel, .form-stack, .advanced-box, .sample-card { display: grid; gap: 0.9rem; }
  h3, h4, p { margin: 0; }
  p { color: var(--text-muted); font-size: 0.85rem; }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; }
  input, select { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .strategy-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.6rem; }
  .strategy-card { text-align: left; display: grid; gap: 0.25rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 10px; padding: 0.75rem; cursor: pointer; }
  .strategy-card span { color: var(--text-muted); font-size: 0.78rem; }
  .strategy-card.selected { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .checkbox-row { display: flex; align-items: center; gap: 0.5rem; }
  .checkbox-row input { width: auto; }
  .advanced-box, .sample-card { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  button:disabled { opacity: 0.55; cursor: not-allowed; }
  .sample-card button { justify-self: start; border: 1px solid var(--border-color); border-radius: 8px; background: transparent; color: var(--text-muted); padding: 0.55rem 0.8rem; }
  @media (max-width: 760px) { .two-col, .strategy-grid { grid-template-columns: 1fr; } }
</style>
