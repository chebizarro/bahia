<script>
  let { variant = 'default', size = 'md', children } = $props();
</script>

<span class="badge {variant} {size}">
  {@render children?.()}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-weight: 500;
    font-size: 0.75rem;
  }
  .badge.sm { font-size: 0.625rem; padding: 0.125rem 0.375rem; }
  .badge.default { background: #374151; color: #d1d5db; }
  .badge.primary { background: #1e3a8a; color: #bfdbfe; }
  .badge.success { background: #065f46; color: #6ee7b7; }
  .badge.warning { background: #78350f; color: #fcd34d; }
  .badge.error { background: #7f1d1d; color: #fca5a5; }
  .badge.info { background: #1e3a5f; color: #93c5fd; }
</style>
