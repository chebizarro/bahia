<svelte:options runes={false} />
<script>
  export let id = '';
  export let name = '';
  export let type = 'text';
  export let value = '';
  export let placeholder = '';
  export let min = undefined;
  export let max = undefined;
  export let step = undefined;
  export let ariaLabel = undefined;
  export let disabled = false;
  export let required = false;
  export let error = '';
  export let oninput = null;
  export let onchange = null;
  export let onblur = null;
  export let onkeydown = null;

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

<input
  {id}
  {name}
  {type}
  {placeholder}
  {min}
  {max}
  {step}
  aria-label={ariaLabel}
  {disabled}
  {required}
  bind:value
  class="input"
  class:error
  oninput={handleInput}
  onchange={handleChange}
  onblur={handleBlur}
  onkeydown={onkeydown}
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
