<script>
  export let title = 'No data';
  export let message = '';
  export let icon = '📭';
  export let actionLabel = '';
  export let showIcon = true;
</script>

<div class="empty-state">
  {#if showIcon}
    <div class="icon">{icon}</div>
  {/if}
  <h3 class="title">{title}</h3>
  {#if message}
    <p class="message">{message}</p>
  {/if}
  {#if actionLabel || $$slots.action}
    <div class="actions">
      {#if actionLabel}
        <button class="action-button" on:click>
          {actionLabel}
        </button>
      {/if}
      <slot name="action" />
    </div>
  {/if}
  <slot />
</div>

<style>
  .empty-state {
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
    opacity: 0.6;
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
  .actions {
    margin-top: 1rem;
  }
  .action-button {
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--card-bg);
    color: var(--text-primary);
    cursor: pointer;
    transition: background-color 0.2s;
  }
  .action-button:hover {
    background: var(--hover-bg);
  }
</style>
