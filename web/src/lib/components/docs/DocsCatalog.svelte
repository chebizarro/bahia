<script>
  let { groups = [], topics = [] } = $props();

  const fallbackGroups = $derived([
    { category: 'guide', label: 'Getting Started & Guides', topics: topics.filter((topic) => topic.category === 'guide') },
    { category: 'feature', label: 'Feature Guides', topics: topics.filter((topic) => topic.category === 'feature') },
    { category: 'reference', label: 'Integration & Reference', topics: topics.filter((topic) => topic.category === 'reference') }
  ]);
  const displayGroups = $derived((Array.isArray(groups) && groups.length > 0 ? groups : fallbackGroups)
    .filter((group) => Array.isArray(group.topics) && group.topics.length > 0));
</script>

<div class="docs-catalog" aria-label="Documentation topics">
  {#each displayGroups as group}
    <section class="docs-group" aria-labelledby={`docs-group-${group.category}`}>
      <div class="group-heading">
        <p class="eyebrow">{group.category}</p>
        <h2 id={`docs-group-${group.category}`}>{group.label}</h2>
      </div>
      <div class="topic-grid">
        {#each group.topics as topic}
          <a class="topic-card" href={topic.href}>
            <span class="topic-title">{topic.title || topic.topic}</span>
            <span class="topic-meta">{topic.topic}</span>
          </a>
        {/each}
      </div>
    </section>
  {/each}
</div>

<style>
  .docs-catalog {
    display: flex;
    flex-direction: column;
    gap: 2rem;
  }

  .docs-group {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .group-heading {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .eyebrow {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  h2 {
    margin: 0;
    color: var(--text-primary);
    font-size: 1.25rem;
  }

  .topic-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 1rem;
  }

  .topic-card {
    display: flex;
    min-height: 6rem;
    flex-direction: column;
    justify-content: space-between;
    gap: 1rem;
    padding: 1rem;
    border: 1px solid var(--border-color);
    border-radius: 12px;
    background: var(--card-bg);
    color: var(--text-primary);
    text-decoration: none;
    transition: border-color 0.2s, transform 0.2s, background 0.2s;
  }

  .topic-card:hover,
  .topic-card:focus-visible {
    border-color: var(--accent-color, #60a5fa);
    background: var(--hover-bg);
    transform: translateY(-1px);
    outline: none;
  }

  .topic-title {
    font-weight: 700;
    line-height: 1.3;
  }

  .topic-meta {
    color: var(--text-muted);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.75rem;
  }
</style>
