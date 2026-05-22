<svelte:options runes={false} />
<script>
  export let id = '';
  export let name = '';
  export let value = '';
  export let placeholder = '';
  export let disabled = false;
  export let required = false;
  export let error = '';
  export let rows = 4;
  export let oninput = null;
  export let onchange = null;
  export let onblur = null;

  function handleInput(event) {
    value = event.currentTarget.value;
    oninput?.(event);
  }

  function handleChange(event) {
    onchange?.(event);
  }

  function handleBlur(event) {
    onblur?.(event);
  }
</script>

<textarea
  {id}
  {name}
  {placeholder}
  {disabled}
  {required}
  {rows}
  bind:value
  class="textarea"
  class:error
  oninput={handleInput}
  onchange={handleChange}
  onblur={handleBlur}
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
