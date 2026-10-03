<script>
  import { pendingIntentRows } from '$lib/nostr/intent-client.svelte.js';

  let { domain } = $props();
  let rows = $derived(pendingIntentRows.filter(row => row.domain === domain));
</script>

{#if rows.length}
  <aside data-testid={`${domain}-pending-intents`} aria-live="polite">
    {#each rows as row (row.key)}
      <p>
        <strong>{row.status === 'pending' ? 'Pending' : row.status}</strong>
        {row.op} · {row.coordinate} · since {new Date(row.createdAt * 1000).toLocaleString()}
        {#if row.reason}<span> — {row.reason}</span>{/if}
      </p>
    {/each}
  </aside>
{/if}
