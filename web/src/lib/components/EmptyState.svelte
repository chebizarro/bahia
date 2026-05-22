<script>
  import { EmptyIcon } from '$lib/icons/domain-icons.js';

  let {
    title = 'No data',
    message = '',
    icon = '',
    iconComponent = null,
    actionLabel = '',
    showIcon = true,
    onAction,
    children,
    action
  } = $props();
</script>

<div class="empty-state">
  {#if showIcon}
    <div class="icon" aria-hidden="true">
      {#if iconComponent}
        {@const IconComponent = iconComponent}
        <IconComponent size={48} strokeWidth={1.5} />
      {:else if icon}
        {icon}
      {:else}
        <EmptyIcon size={48} strokeWidth={1.5} />
      {/if}
    </div>
  {/if}
  <h3 class="title">{title}</h3>
  {#if message}
    <p class="message">{message}</p>
  {/if}
  {#if actionLabel || action}
    <div class="actions">
      {#if actionLabel}
        <button class="action-button" onclick={onAction}>
          {actionLabel}
        </button>
      {/if}
      {@render action?.()}
    </div>
  {/if}
  {@render children?.()}
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
    color: var(--text-muted);
    display: flex;
    align-items: center;
    justify-content: center;
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
