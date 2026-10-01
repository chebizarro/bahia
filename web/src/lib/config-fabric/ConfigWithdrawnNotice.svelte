<script>
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { WarningIcon } from '$lib/icons/domain-icons.js';
  import { withdrawnGuidance } from './model.js';

  let { row = null, onPublish = null } = $props();

  let guidance = $derived(withdrawnGuidance(row));
</script>

{#if guidance}
  <section class="withdrawn" role="status" aria-label="Withdrawn desired config">
    <h2><WarningIcon size={18} /> Desired config withdrawn</h2>
    <p>{guidance.withdrawn}</p>
    <p><strong>{guidance.kept}</strong> {guidance.why}</p>
    <p class="next">{guidance.next}</p>
    {#if onPublish}
      <LoadingButton variant="primary" onclick={onPublish}>Publish v{guidance.nextVersion}</LoadingButton>
    {/if}
  </section>
{/if}

<style>
  .withdrawn {
    background: rgba(245, 158, 11, 0.08);
    border: 1px solid var(--border-color);
    border-left: 4px solid #f59e0b;
    border-radius: 8px;
    margin-bottom: 1rem;
    padding: 1.25rem;
  }
  h2 { align-items: center; color: #fcd34d; display: flex; font-size: 1rem; gap: 0.5rem; margin: 0 0 0.75rem; }
  p { font-size: 0.875rem; margin: 0 0 0.5rem; }
  .next { color: var(--text-muted); margin-bottom: 0.75rem; }
</style>
