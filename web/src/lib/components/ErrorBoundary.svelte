<script>
  import { createEventDispatcher, onMount } from 'svelte';
  import ErrorState from './ErrorState.svelte';

  export let fallbackTitle = 'Something went wrong';
  export let fallbackMessage = 'An unexpected error occurred. Please try again.';
  export let resetLabel = 'Try Again';

  const dispatch = createEventDispatcher();

  let error = null;
  let errorInfo = null;

  function handleReset() {
    error = null;
    errorInfo = null;
    dispatch('reset');
  }

  // Note: Svelte 4 does not support true component error boundaries like React.
  // The <svelte:boundary> feature requires Svelte 5.
  // This component provides a reusable error display wrapper, but cannot catch
  // render errors automatically. To handle render errors in Svelte 4, use
  // route-level +error.svelte files or wrap async logic with try/catch.
  
  // You can manually trigger the error state by calling: component.showError(err)
  export function showError(err, info = null) {
    error = err;
    errorInfo = info;
    dispatch('error', { error: err, errorInfo: info });
  }
</script>

{#if error}
  <ErrorState
    title={fallbackTitle}
    message={error?.message || fallbackMessage}
    {resetLabel}
    on:reset={handleReset}
  />
{:else}
  <slot />
{/if}
