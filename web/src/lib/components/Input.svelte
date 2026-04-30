<script>
  import { createEventDispatcher } from 'svelte';

  export let id = '';
  export let name = '';
  export let type = 'text';
  export let value = '';
  export let placeholder = '';
  export let disabled = false;
  export let required = false;
  export let error = '';

  const dispatch = createEventDispatcher();

  function handleInput(e) {
    value = e.target.value;
    dispatch('input', { value });
  }

  function handleChange(e) {
    dispatch('change', { value: e.target.value });
  }

  function handleBlur(e) {
    dispatch('blur', { value: e.target.value });
  }
</script>

<input
  {id}
  {name}
  {type}
  {placeholder}
  {disabled}
  {required}
  {value}
  class="input"
  class:error
  on:input={handleInput}
  on:change={handleChange}
  on:blur={handleBlur}
  on:keydown
/>

<style>
  .input {
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg);
    color: var(--text-primary);
    outline: none;
  }
  .input:focus {
    border-color: var(--primary);
  }
  .input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .input.error {
    border-color: var(--error);
  }
</style>
