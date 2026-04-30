<script>
  let {
    id = '',
    name = '',
    value = $bindable(),
    placeholder = '',
    disabled = false,
    required = false,
    error = '',
    rows = 4,
    oninput: onInput,
    onchange: onChange,
    onblur: onBlur
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
