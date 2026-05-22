<svelte:options runes={false} />
<script>
  export let id = '';
  export let name = '';
  export let value = '';
  export let options = [];
  export let disabled = false;
  export let required = false;
  export let error = '';
  export let placeholder = 'Select an option';
  export let onchange = null;
  export let onblur = null;

  function handleChange(event) {
    value = event.currentTarget.value;
    onchange?.(event);
  }

  function handleBlur(event) {
    onblur?.(event);
  }
</script>

<select
  {id}
  {name}
  {disabled}
  {required}
  bind:value
  class="select"
  class:error
  onchange={handleChange}
  onblur={handleBlur}
>
  {#if placeholder}
    <option value="" disabled selected={!value}>{placeholder}</option>
  {/if}
  {#each options as option}
    <option value={option.value} disabled={option.disabled || false}>
      {option.label}
    </option>
  {/each}
</select>

<style>
  .select {
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg);
    color: var(--text-primary);
    outline: none;
    cursor: pointer;
  }
  .select:focus {
    border-color: var(--primary);
  }
  .select:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .select.error {
    border-color: var(--error);
  }
</style>
