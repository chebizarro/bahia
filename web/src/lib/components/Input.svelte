<script>
  let {
    id = '',
    name = '',
    type = 'text',
    value = $bindable(),
    placeholder = '',
    disabled = false,
    required = false,
    error = '',
    oninput: onInput,
    onchange: onChange,
    onblur: onBlur,
    onkeydown: onKeydown
  } = $props();

  function handleInput(event) {
    value = event.currentTarget.value;
    onInput?.(event);
  }

  function handleChange(event) {
    onChange?.(event);
  }

  function handleBlur(event) {
    onBlur?.(event);
  }
</script>

<input
  {id}
  {name}
  {type}
  {placeholder}
  {disabled}
  {required}
  bind:value
  class="input"
  class:error
  oninput={handleInput}
  onchange={handleChange}
  onblur={handleBlur}
  onkeydown={onKeydown}
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
