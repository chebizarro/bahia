<script>
  import { createEventDispatcher } from 'svelte';
  import Badge from '$lib/components/Badge.svelte';

  export let repository;
  export let selected = false;
  export let disabled = false;
  export let disabledReason = '';
  export let showCi = false;

  const dispatch = createEventDispatcher();

  function handleClick() {
    if (!disabled) {
      dispatch('select', repository);
    }
  }

  $: provider = repository?.source === 'nip34' || repository?.repoCoordinate ? 'Nostr' : 'Manual';
  $: name = repository?.displayName || repository?.name || repository?.identifier || 'Unnamed';
  $: description = repository?.description || '';
  $: url = repository?.primaryUrl || '';
  $: hasUrl = !!url;
</script>

<button
  class="repo-card"
  class:selected
  class:disabled
  on:click={handleClick}
  {disabled}
  title={disabled && disabledReason ? disabledReason : undefined}
>
  <div class="repo-icon">📦</div>
  <div class="repo-info">
    <div class="repo-header">
      <h4>{name}</h4>
      <Badge variant="info" size="sm">{provider}</Badge>
    </div>
    {#if description}
      <p class="repo-description">{description}</p>
    {/if}
    {#if url}
      <p class="repo-url">{url}</p>
    {:else if disabled && disabledReason}
      <p class="repo-warning">{disabledReason}</p>
    {/if}
    {#if showCi && repository?.ci}
      <div class="repo-ci">
        <!-- Phase 3: CI badge slot -->
        <slot name="ci" />
      </div>
    {/if}
  </div>
  {#if selected}
    <span class="check">✓</span>
  {/if}
</button>

<style>
  .repo-card {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 1rem;
    text-align: left;
    cursor: pointer;
    transition: all 0.15s;
    width: 100%;
  }

  .repo-card:hover:not(:disabled) {
    border-color: var(--primary);
    background: var(--hover-bg);
  }

  .repo-card.selected {
    border-color: var(--primary);
    background: rgba(99, 102, 241, 0.1);
  }

  .repo-card.disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .repo-icon {
    font-size: 1.5rem;
    flex-shrink: 0;
    width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(99, 102, 241, 0.1);
    border-radius: 8px;
  }

  .repo-info {
    flex: 1;
    min-width: 0;
  }

  .repo-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.25rem;
  }

  .repo-header h4 {
    font-size: 0.9rem;
    font-weight: 600;
    margin: 0;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .repo-description {
    font-size: 0.8rem;
    color: var(--text-muted);
    margin: 0 0 0.25rem 0;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .repo-url {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: monospace;
  }

  .repo-warning {
    font-size: 0.75rem;
    color: var(--warning, #fcd34d);
    margin: 0;
    font-style: italic;
  }

  .repo-ci {
    margin-top: 0.5rem;
  }

  .check {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    background: var(--primary);
    color: white;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.75rem;
    flex-shrink: 0;
  }
</style>
