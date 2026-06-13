<script>
  import { goto } from '$app/navigation';
  import { renderDocumentationMarkdown } from '$lib/docs/render.js';

  let { markdown = '', links = [] } = $props();

  const renderedHtml = $derived(renderDocumentationMarkdown(markdown, links));

  function handleDocumentClick(event) {
    const anchor = event.target?.closest?.('.markdown-document a');
    if (!anchor) return;

    if (anchor.classList.contains('docs-link-unresolved') || anchor.getAttribute('aria-disabled') === 'true') {
      event.preventDefault();
      return;
    }

    if (anchor.dataset.docTopic) {
      event.preventDefault();
      const href = anchor.getAttribute('href');
      if (href) void goto(href);
    }
  }
</script>

<svelte:window onclick={handleDocumentClick} />

<article class="markdown-document">
  {@html renderedHtml}
</article>

<style>
  .markdown-document {
    max-width: 920px;
    color: var(--text-primary);
    line-height: 1.7;
  }

  :global(.markdown-document h1),
  :global(.markdown-document h2),
  :global(.markdown-document h3),
  :global(.markdown-document h4) {
    margin: 1.75rem 0 0.75rem;
    color: var(--text-primary);
    line-height: 1.25;
  }

  :global(.markdown-document h1) {
    margin-top: 0;
    font-size: 2rem;
  }

  :global(.markdown-document h2) {
    border-bottom: 1px solid var(--border-color);
    padding-bottom: 0.35rem;
    font-size: 1.4rem;
  }

  :global(.markdown-document p),
  :global(.markdown-document ul),
  :global(.markdown-document ol),
  :global(.markdown-document table),
  :global(.markdown-document pre) {
    margin: 0 0 1rem;
  }

  :global(.markdown-document a) {
    color: var(--link-color, #60a5fa);
    text-decoration: underline;
    text-underline-offset: 0.18em;
  }

  :global(.markdown-document a.docs-link-unresolved) {
    color: var(--warning-color, #f59e0b);
    cursor: not-allowed;
    text-decoration-style: dotted;
  }

  :global(.markdown-document code) {
    border-radius: 4px;
    background: var(--code-bg, rgba(148, 163, 184, 0.16));
    color: var(--code-text, #f8fafc);
    padding: 0.1rem 0.3rem;
    font-size: 0.9em;
  }

  :global(.markdown-document pre) {
    overflow-x: auto;
    border: 1px solid var(--border-color);
    border-radius: 10px;
    background: var(--code-block-bg, #111827);
    color: var(--code-block-text, #f8fafc);
    padding: 1rem;
  }

  :global(.markdown-document pre code) {
    background: transparent;
    color: inherit;
    padding: 0;
  }

  :global(.markdown-document table) {
    width: 100%;
    border-collapse: collapse;
  }

  :global(.markdown-document th),
  :global(.markdown-document td) {
    border: 1px solid var(--border-color);
    padding: 0.5rem;
    text-align: left;
    vertical-align: top;
  }

  :global(.markdown-document blockquote) {
    margin: 0 0 1rem;
    border-left: 3px solid var(--border-color);
    padding-left: 1rem;
    color: var(--text-muted);
  }
</style>
