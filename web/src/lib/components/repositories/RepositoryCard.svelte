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

  // CI state
  $: ciState = repository?.ci?.state || 'unsupported';
  $: ciLookup = repository?.ci?.lookup;
  $: latestResult = ciLookup?.latest_result;
  $: latestRun = ciLookup?.latest_run;
  $: linkedServiceCount = ciLookup?.linked_services?.length || 0;
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
    {#if showCi && ciState !== 'unsupported'}
      <div class="ci-status">
        {#if ciState === 'loading'}
          <span class="ci-badge loading">Loading CI...</span>
        {:else if ciState === 'error'}
          <span class="ci-badge error">CI unavailable</span>
        {:else if ciState === 'empty'}
          <span class="ci-badge empty">No CI activity</span>
        {:else if ciState === 'ready'}
          {#if latestResult}
            <span class="ci-badge" class:success={latestResult.status === 'success'} class:failure={latestResult.status === 'failure'}>
              {latestResult.status === 'success' ? '✓' : '✗'} {latestResult.status}
            </span>
          {:else if latestRun}
            <span class="ci-badge pending">⏳ Run observed</span>
          {:else}
            <span class="ci-badge configured">⚙ Configured</span>
          {/if}
          {#if linkedServiceCount > 0}
            <span class="ci-linked">{linkedServiceCount} service{linkedServiceCount > 1 ? 's' : ''}</span>
          {/if}
        {/if}
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

  .ci-status {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.5rem;
    font-size: 0.75rem;
  }

  .ci-badge {
    padding: 0.15rem 0.4rem;
    border-radius: 4px;
    font-weight: 500;
  }

  .ci-badge.loading { background: var(--bg); color: var(--text-muted); }
  .ci-badge.error { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
  .ci-badge.empty { background: var(--bg); color: var(--text-muted); }
  .ci-badge.success { background: rgba(34, 197, 94, 0.15); color: #22c55e; }
  .ci-badge.failure { background: rgba(239, 68, 68, 0.15); color: #ef4444; }
  .ci-badge.pending { background: rgba(234, 179, 8, 0.15); color: #eab308; }
  .ci-badge.configured { background: rgba(99, 102, 241, 0.15); color: var(--primary); }

  .ci-linked {
    color: var(--text-muted);
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
