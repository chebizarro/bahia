<script>
  import { createEventDispatcher } from 'svelte';

  export let id = '';
  export let name = '';
  export let checked = false;
  export let disabled = false;
  export let label = '';

  const dispatch = createEventDispatcher();

  function handleChange(e) {
    checked = e.target.checked;
    dispatch('change', { checked });
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
    on:change={handleChange}
  />
  {#if label}
    <span class="label">{label}</span>
  {:else}
    <slot />
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
