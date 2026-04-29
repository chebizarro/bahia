<script>
  import { createEventDispatcher } from 'svelte';
  import LoadingButton from './LoadingButton.svelte';

  export let title = 'Error';
  export let message = 'An error occurred.';
  export let details = '';
  export let resetLabel = '';
  export let showIcon = true;

  const dispatch = createEventDispatcher();

  function handleReset() {
    dispatch('reset');
  }
</script>

<div class="error-state">
  {#if showIcon}
    <div class="icon">⚠️</div>
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
      <LoadingButton variant="primary" on:click={handleReset}>
        {resetLabel}
      </LoadingButton>
    </div>
  {/if}
  <slot />
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
    font-size: 3rem;
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
