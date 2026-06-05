<script>
  import { untrack } from 'svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DocsCatalog from '$lib/components/docs/DocsCatalog.svelte';
  import { api } from '$lib/api/client.js';

  let initialized = $state(false);
  let loading = $state(true);
  let error = $state('');
  let docsCatalog = $state({ topics: [], groups: [], count: 0 });

  $effect(() => {
    if (initialized) return;
    initialized = true;
    void untrack(() => loadDocsCatalog());
  });

  async function loadDocsCatalog() {
    loading = true;
    error = '';
    try {
      if (!api) throw new Error('API client is not available in this browser session');
      const result = await api.listDocs();
      docsCatalog = {
        topics: Array.isArray(result?.topics) ? result.topics : [],
        groups: Array.isArray(result?.groups) ? result.groups : [],
        count: Number.isFinite(result?.count) ? result.count : 0
      };
    } catch (err) {
      error = err?.message || 'Failed to load documentation catalog';
      docsCatalog = { topics: [], groups: [], count: 0 };
    } finally {
      loading = false;
    }
  }
</script>

<div class="docs-page">
  <header class="page-header">
    <p class="eyebrow">Documentation</p>
    <h1>Bahia User Guide</h1>
    <p class="subtitle">Browse the same central Markdown corpus served to agents through Bahia docs resources.</p>
  </header>

  {#if loading}
    <div class="status-card" role="status">Loading documentation catalog…</div>
  {:else if error}
    <div class="error-card" role="alert">
      <h2>Documentation catalog failed to load</h2>
      <p>{error}</p>
      <button type="button" onclick={loadDocsCatalog}>Retry</button>
    </div>
  {:else if docsCatalog.count === 0}
    <EmptyState title="No documentation topics found" message="The central docs service returned an empty catalog." />
  {:else}
    <div class="catalog-summary" aria-live="polite">{docsCatalog.count} topics available</div>
    <DocsCatalog groups={docsCatalog.groups} topics={docsCatalog.topics} />
  {/if}
</div>

<style>
  .docs-page {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
    padding: 2rem;
  }

  .page-header {
    max-width: 760px;
  }

  .eyebrow {
    margin: 0 0 0.35rem;
    color: var(--text-muted);
    font-size: 0.75rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  h1 {
    margin: 0;
    color: var(--text-primary);
    font-size: 2.25rem;
  }

  .subtitle {
    margin: 0.75rem 0 0;
    color: var(--text-muted);
  }

  .catalog-summary,
  .status-card,
  .error-card {
    border: 1px solid var(--border-color);
    border-radius: 12px;
    background: var(--card-bg);
    padding: 1rem;
  }

  .catalog-summary {
    color: var(--text-muted);
    font-size: 0.9rem;
  }

  .error-card {
    border-color: var(--error-color, #ef4444);
  }

  .error-card h2 {
    margin: 0 0 0.5rem;
    color: var(--text-primary);
  }

  .error-card p {
    margin: 0 0 1rem;
    color: var(--text-muted);
  }

  button {
    border: 1px solid var(--border-color);
    border-radius: 8px;
    background: var(--button-bg, #1f2937);
    color: var(--text-primary);
    cursor: pointer;
    padding: 0.5rem 0.85rem;
  }
</style>
