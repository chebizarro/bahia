<script>
  import { createEventDispatcher } from 'svelte';

  export let id;
  export let type = 'info'; // success | error | warning | info
  export let title = '';
  export let message = '';

  const dispatch = createEventDispatcher();

  function close() {
    dispatch('close', { id });
  }

  const icons = {
    success: '✓',
    error: '✕',
    warning: '⚠',
    info: 'ℹ'
  };
</script>

<div class="toast {type}" role="alert">
  <div class="toast-icon">{icons[type]}</div>
  <div class="toast-content">
    {#if title}
      <div class="toast-title">{title}</div>
    {/if}
    <div class="toast-message">{message}</div>
  </div>
  <button class="toast-close" on:click={close} aria-label="Close">
    ×
  </button>
</div>

<style>
  .toast {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    padding: 1rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
    min-width: 300px;
    max-width: 500px;
    animation: slideIn 0.3s ease-out;
  }
  @keyframes slideIn {
    from {
      transform: translateX(100%);
      opacity: 0;
    }
    to {
      transform: translateX(0);
      opacity: 1;
    }
  }
  .toast.success {
    border-left: 4px solid var(--success);
  }
  .toast.error {
    border-left: 4px solid var(--error);
  }
  .toast.warning {
    border-left: 4px solid var(--warning);
  }
  .toast.info {
    border-left: 4px solid var(--primary);
  }
  .toast-icon {
    font-size: 1.25rem;
    font-weight: bold;
    flex-shrink: 0;
  }
  .toast.success .toast-icon { color: var(--success); }
  .toast.error .toast-icon { color: var(--error); }
  .toast.warning .toast-icon { color: var(--warning); }
  .toast.info .toast-icon { color: var(--primary); }
  
  .toast-content {
    flex: 1;
    min-width: 0;
  }
  .toast-title {
    font-weight: 600;
    font-size: 0.875rem;
    color: var(--text-primary);
    margin-bottom: 0.25rem;
  }
  .toast-message {
    font-size: 0.875rem;
    color: var(--text-primary);
    word-wrap: break-word;
  }
  .toast-close {
    background: none;
    border: none;
    font-size: 1.5rem;
    line-height: 1;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0;
    width: 1.5rem;
    height: 1.5rem;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .toast-close:hover {
    color: var(--text-primary);
  }
</style>
