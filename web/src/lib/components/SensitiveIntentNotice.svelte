<script>
  import { authState } from '$lib/stores/auth.svelte.js';
  import { initializeSensitiveIntents, sensitivePendingState } from '$lib/stores/sensitive-intents.svelte.js';

  let { domain } = $props();
  let rows = $derived(sensitivePendingState.rows.filter(row => row.domain === domain));

  $effect(() => {
    if (authState.status === 'authenticated') void initializeSensitiveIntents().catch(() => {});
  });
</script>

{#if rows.length}
  <div class="intent-notice" role="status" aria-live="polite">
    {#each rows as row (row.intentId)}
      <p>
        {#if row.status === 'pending'}
          Pending {domain} change — awaiting daemon status.
        {:else}
          {domain} change {row.status}: {row.reason || 'Review the canonical state before retrying.'}
        {/if}
      </p>
    {/each}
  </div>
{/if}

<style>
  .intent-notice {
    margin: 1rem 0;
    padding: .75rem 1rem;
    border: 1px solid var(--border-color);
    border-radius: 8px;
    background: var(--card-bg);
  }
  .intent-notice p { margin: .25rem 0; }
</style>
