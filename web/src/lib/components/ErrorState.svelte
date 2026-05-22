<script>
  import LoadingButton from './LoadingButton.svelte';
  import { WarningIcon } from '$lib/icons/domain-icons.js';

  let {
    title = 'Error',
    message = 'An error occurred.',
    details = '',
    resetLabel = '',
    showIcon = true,
    onReset,
    children
  } = $props();
</script>

<div class="error-state">
  {#if showIcon}
    <div class="icon" aria-hidden="true">
      <WarningIcon size={48} strokeWidth={1.5} />
    </div>
  {/if}
  <h2 class="title">{title}</h2>
  <p class="message">{message}</p>
  {#if details}
    <details class="details">
      <summary>Technical details</summary>
      <pre>{details}</pre>
    </details>
  {/if}
  {#if resetLabel}
    <div class="actions">
      <LoadingButton variant="primary" onclick={onReset}>
        {resetLabel}
      </LoadingButton>
    </div>
  {/if}
  {@render children?.()}
</div>

<style>
  .error-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    text-align: center;
    padding: 3rem 1.5rem;
    gap: 1rem;
  }
  .icon {
    display: inline-flex;
    color: var(--warning, #f59e0b);
    margin-bottom: 0.5rem;
  }
  .title {
    font-size: 1.5rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }
  .message {
    font-size: 1rem;
    color: var(--text-muted);
    margin: 0;
    max-width: 500px;
  }
  .details {
    margin-top: 1rem;
    text-align: left;
    max-width: 600px;
    width: 100%;
  }
  .details summary {
    cursor: pointer;
    font-size: 0.875rem;
    color: var(--text-muted);
    margin-bottom: 0.5rem;
  }
  .details pre {
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    padding: 1rem;
    overflow-x: auto;
    font-size: 0.75rem;
    color: var(--text-primary);
  }
  .actions {
    margin-top: 1rem;
  }
</style>
