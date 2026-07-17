<script>
  import Badge from '$lib/components/Badge.svelte';
  import {
    ErrorIcon,
    RepositoryIcon,
    SuccessIcon
  } from '$lib/icons/domain-icons.js';

  let {
    repository,
    selected = false,
    disabled = false,
    disabledReason = '',
    showCi = false,
    onSelect
  } = $props();

  function handleClick() {
    if (!disabled) {
      onSelect?.(repository);
    }
  }

  const provider = $derived(repository?.source === 'nip34' || repository?.repoCoordinate ? 'Nostr' : 'Manual');
  const name = $derived(repository?.displayName || repository?.name || repository?.identifier || 'Unnamed');
  const description = $derived(repository?.description || '');
  const url = $derived(repository?.primaryUrl || '');

</script>

<button
  class="repo-card"
  class:selected
  class:disabled
  onclick={handleClick}
  {disabled}
  title={disabled && disabledReason ? disabledReason : undefined}
>
  <div class="repo-icon" aria-hidden="true"><RepositoryIcon size={24} strokeWidth={1.75} /></div>
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
    {#if showCi}
      <div class="ci-status">
        <span class="ci-badge unavailable"><ErrorIcon size={14} strokeWidth={2} ariaHidden="true" /> CI status unavailable</span>
      </div>
    {/if}
  </div>
  {#if selected}
    <span class="check" aria-hidden="true"><SuccessIcon size={14} strokeWidth={2} /></span>
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
    line-clamp: 2;
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

  .ci-status {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.5rem;
    font-size: 0.75rem;
  }

  .ci-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.15rem 0.4rem;
    border-radius: 4px;
    font-weight: 500;
  }

  .ci-badge.unavailable { background: var(--bg); color: var(--text-muted); }

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
