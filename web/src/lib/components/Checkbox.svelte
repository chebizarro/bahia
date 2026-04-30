<script>
  let {
    id = '',
    name = '',
    checked = $bindable(),
    disabled = false,
    label = '',
    children,
    onchange: onChange
  } = $props();

  function handleChange(event) {
    checked = event.currentTarget.checked;
    onChange?.(event);
  }
</script>

<label class="checkbox-wrapper">
  <input
    type="checkbox"
    {id}
    {name}
    {disabled}
    bind:checked
    class="checkbox"
    onchange={handleChange}
  />
  {#if label}
    <span class="label">{label}</span>
  {:else}
    {@render children?.()}
  {/if}
</label>

<style>
  .checkbox-wrapper {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    cursor: pointer;
    user-select: none;
  }
  .checkbox {
    width: 1rem;
    height: 1rem;
    cursor: pointer;
    accent-color: var(--primary);
  }
  .checkbox:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .label {
    font-size: 0.875rem;
    color: var(--text-primary);
  }
</style>
