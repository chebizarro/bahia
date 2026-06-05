<script>
  import { page } from '$app/state';
  import { untrack } from 'svelte';
  import MarkdownDocument from '$lib/components/docs/MarkdownDocument.svelte';
  import { api } from '$lib/api/client.js';

  let loading = $state(true);
  let error = $state('');
  let document = $state(null);
  let loadedTopic = $state('');
  let requestSequence = 0;

  $effect(() => {
    const topic = page.params?.topic || '';
    if (!topic || topic === loadedTopic) return;
    loadedTopic = topic;
    void untrack(() => loadDocument(topic));
  });

  async function loadDocument(topic) {
    const requestId = requestSequence + 1;
    requestSequence = requestId;
    loading = true;
    error = '';
    document = null;
    try {
      if (!api) throw new Error('API client is not available in this browser session');
      const nextDocument = await api.getDoc(topic);
      if (requestId !== requestSequence || topic !== loadedTopic) return;
      document = nextDocument;
    } catch (err) {
      if (requestId !== requestSequence || topic !== loadedTopic) return;
      error = err?.message || `Failed to load documentation topic: ${topic}`;
    } finally {
      if (requestId === requestSequence && topic === loadedTopic) {
        loading = false;
      }
    }
  }
</script>

<div class="docs-reader-page">
  <nav class="docs-nav" aria-label="Documentation navigation">
    <a href="/docs">← Documentation catalog</a>
  </nav>

  {#if loading}
    <div class="status-card" role="status">Loading documentation topic…</div>
  {:else if error}
    <div class="error-card" role="alert">
      <p class="eyebrow">Documentation error</p>
      <h1>Topic unavailable</h1>
      <p>{error}</p>
      <a class="button-link" href="/docs">Back to documentation catalog</a>
    </div>
  {:else if document}
    <header class="topic-header">
      <p class="eyebrow">{document.metadata?.category || 'documentation'}</p>
      <h1>{document.metadata?.title || document.metadata?.topic}</h1>
      <p class="topic-meta">{document.metadata?.topic} · {document.metadata?.sourcePath}</p>
    </header>
    <MarkdownDocument markdown={document.markdown} links={document.links || []} />
  {/if}
</div>

<style>
  .docs-reader-page {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
    padding: 2rem;
  }

  .docs-nav a,
  .button-link {
    color: var(--link-color, #60a5fa);
    text-decoration: none;
  }

  .docs-nav a:hover,
  .button-link:hover {
    text-decoration: underline;
  }

  .topic-header {
    max-width: 920px;
    border-bottom: 1px solid var(--border-color);
    padding-bottom: 1rem;
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

  .topic-meta {
    margin: 0.5rem 0 0;
    color: var(--text-muted);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.8rem;
  }

  .status-card,
  .error-card {
    max-width: 760px;
    border: 1px solid var(--border-color);
    border-radius: 12px;
    background: var(--card-bg);
    padding: 1rem;
  }

  .error-card {
    border-color: var(--error-color, #ef4444);
  }

  .error-card p:not(.eyebrow) {
    color: var(--text-muted);
  }

  .button-link {
    display: inline-flex;
    margin-top: 0.5rem;
  }
</style>
