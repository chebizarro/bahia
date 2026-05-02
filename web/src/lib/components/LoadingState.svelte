<script>
  let {
    title = 'Loading',
    message = 'Please wait while we load data.',
    showSpinner = true,
    children
  } = $props();
</script>

<div class="loading-state" aria-live="polite" aria-busy="true">
  {#if showSpinner}
    <span class="spinner" aria-hidden="true"></span>
  {/if}
  <h3 class="title">{title}</h3>
  {#if message}
    <p class="message">{message}</p>
  {/if}
  {@render children?.()}
</div>

<style>
  .loading-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    text-align: center;
    padding: 3rem 1.5rem;
    gap: 1rem;
  }

  .spinner {
    display: inline-block;
    width: 2rem;
    height: 2rem;
    border: 3px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  .title {
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }

  .message {
    font-size: 0.875rem;
    color: var(--text-muted);
    margin: 0;
    max-width: 400px;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
