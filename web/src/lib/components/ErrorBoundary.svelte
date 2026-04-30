<script>
  import ErrorState from './ErrorState.svelte';

  let {
    fallbackTitle = 'Something went wrong',
    fallbackMessage = 'An unexpected error occurred. Please try again.',
    resetLabel = 'Try Again',
    onReset,
    onError,
    children
  } = $props();

  let error = $state(null);
  let errorInfo = $state(null);

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
  {@render children?.()}
{/if}
