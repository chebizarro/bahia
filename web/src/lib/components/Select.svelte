<script>
  import { createEventDispatcher } from 'svelte';

  export let id = '';
  export let name = '';
  export let value = '';
  export let options = [];
  export let disabled = false;
  export let required = false;
  export let error = '';
  export let placeholder = 'Select an option';

  const dispatch = createEventDispatcher();

  function handleChange(e) {
    value = e.target.value;
    dispatch('change', { value });
  }

  function handleBlur(e) {
    dispatch('blur', { value: e.target.value });
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
  on:change={handleChange}
  on:blur={handleBlur}
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
