<svelte:options runes={false} />
<script>
  export let type = 'button';
  export let variant = 'primary';
  export let loading = false;
  export let disabled = false;
  export let fullWidth = false;
  export let onclick = null;
</script>

<button
  {type}
  class="btn {variant}"
  class:loading
  class:full-width={fullWidth}
  disabled={disabled || loading}
  {onclick}
>
  {#if loading}
    <span class="spinner"></span>
  {/if}
  <slot />
</button>

<style>
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    transition: opacity 0.2s, background-color 0.2s;
  }
  .btn:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .btn.primary {
    background: var(--primary);
    color: white;
  }
  .btn.primary:hover:not(:disabled) {
    opacity: 0.9;
  }
  .btn.secondary {
    background: var(--card-bg);
    color: var(--text-primary);
    border: 1px solid var(--border-color);
  }
  .btn.secondary:hover:not(:disabled) {
    background: var(--hover-bg);
  }
  .btn.danger {
    background: var(--error);
    color: white;
  }
  .btn.danger:hover:not(:disabled) {
    opacity: 0.9;
  }
  .btn.full-width {
    width: 100%;
  }
  .spinner {
    display: inline-block;
    width: 1rem;
    height: 1rem;
    border: 2px solid rgba(255, 255, 255, 0.3);
    border-top-color: white;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
