<script>
  import { createEventDispatcher } from 'svelte';

  export let id = '';
  export let name = '';
  export let value = '';
  export let placeholder = '';
  export let disabled = false;
  export let required = false;
  export let error = '';
  export let rows = 4;

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

<textarea
  {id}
  {name}
  {placeholder}
  {disabled}
  {required}
  {rows}
  {value}
  class="textarea"
  class:error
  on:input={handleInput}
  on:change={handleChange}
  on:blur={handleBlur}
></textarea>

<style>
  .textarea {
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    font-family: inherit;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg);
    color: var(--text-primary);
    outline: none;
    resize: vertical;
  }
  .textarea:focus {
    border-color: var(--primary);
  }
  .textarea:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .textarea.error {
    border-color: var(--error);
  }
</style>
