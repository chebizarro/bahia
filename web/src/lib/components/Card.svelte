<script>
  let {
    title = '',
    titleIcon = null,
    value = '',
    subtitle = '',
    status = 'default',
    children
  } = $props();

  const hasValue = $derived(value !== '' && value !== null && value !== undefined);
</script>

<div class="card {status}">
  {#if title}
    <div class="card-title">
      {#if titleIcon}
        {@const TitleIcon = titleIcon}
        <span class="title-icon" aria-hidden="true">
          <TitleIcon size={16} stroke={1.75} />
        </span>
      {/if}
      <span>{title}</span>
    </div>
  {/if}
  {#if hasValue}
    <div class="card-value">{value}</div>
  {/if}
  {#if subtitle}
    <div class="card-subtitle">{subtitle}</div>
  {/if}
  {@render children?.()}
</div>

<style>
  .card {
    background: var(--card-bg, #1a1a2e);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color, #2a2a4a);
  }
  .card.success { border-left: 4px solid #10b981; }
  .card.warning { border-left: 4px solid #f59e0b; }
  .card.error { border-left: 4px solid #ef4444; }
  .card-title {
    font-size: 0.875rem;
    color: var(--text-muted, #888);
    margin-bottom: 0.5rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .title-icon {
    flex-shrink: 0;
  }
  .card-value {
    font-size: 2rem;
    font-weight: bold;
    color: var(--text-primary, #fff);
  }
  .card-subtitle {
    font-size: 0.75rem;
    color: var(--text-muted, #888);
    margin-top: 0.25rem;
  }
</style>
