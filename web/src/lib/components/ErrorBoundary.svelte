<svelte:options runes={false} />
<script>
  import ErrorState from './ErrorState.svelte';

  export let fallbackTitle = 'Something went wrong';
  export let fallbackMessage = 'An unexpected error occurred. Please try again.';
  export let resetLabel = 'Try Again';
  export let onReset = null;
  export let onError = null;

  let error = null;
  let errorInfo = null;

  function handleReset() {
    error = null;
    errorInfo = null;
    onReset?.();
  }

  export function showError(err, info = null) {
    error = err;
    errorInfo = info;
    onError?.({ error: err, errorInfo: info });
  }
</script>

{#if error}
  <ErrorState
    title={fallbackTitle}
    message={error?.message || fallbackMessage}
    details={errorInfo ? JSON.stringify(errorInfo, null, 2) : ''}
    {resetLabel}
    onReset={handleReset}
  />
{:else}
  <slot />
{/if}
