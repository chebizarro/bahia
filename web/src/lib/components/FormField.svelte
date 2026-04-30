<script>
  let {
    label = '',
    id = '',
    error = '',
    hint = '',
    required = false,
    children
  } = $props();
</script>

<div class="form-field">
  {#if label}
    <label for={id} class="label">
      {label}
      {#if required}
        <span class="required">*</span>
      {/if}
    </label>
  {/if}
  {@render children?.()}
  {#if error}
    <div class="error">{error}</div>
  {/if}
  {#if hint && !error}
    <div class="hint">{hint}</div>
  {/if}
</div>

<style>
  .form-field {
    margin-bottom: 1rem;
  }
  .label {
    display: block;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
    margin-bottom: 0.5rem;
  }
  .required {
    color: var(--error);
  }
  .error {
    font-size: 0.75rem;
    color: var(--error);
    margin-top: 0.25rem;
  }
  .hint {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-top: 0.25rem;
  }
</style>
