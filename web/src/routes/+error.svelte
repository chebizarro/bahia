<script>
  import { page } from '$app/stores';
  import ErrorState from '$lib/components/ErrorState.svelte';

  $: status = $page.status || 500;
  $: error = $page.error;

  $: title = getTitle(status);
  $: message = error?.message || getDefaultMessage(status);

  function getTitle(status) {
    switch (status) {
      case 404:
        return 'Page not found';
      case 401:
        return 'Authentication required';
      case 403:
        return 'Access denied';
      default:
        return 'Something went wrong';
    }
  }

  function getDefaultMessage(status) {
    switch (status) {
      case 404:
        return 'The page you are looking for does not exist.';
      case 401:
        return 'You need to log in to access this page.';
      case 403:
        return 'You do not have permission to access this page.';
      default:
        return 'An unexpected error occurred. Please try again.';
    }
  }

  function goBack() {
    if (window.history.length > 1) {
      window.history.back();
    } else {
      window.location.href = '/';
    }
  }
</script>

<ErrorState {title} {message} showIcon={true}>
  <div class="actions">
    <a href="/" class="button button-primary">Go to Dashboard</a>
    <button type="button" class="button button-secondary" on:click={goBack}>
      Go Back
    </button>
  </div>
</ErrorState>

<style>
  .actions {
    display: flex;
    gap: 1rem;
    margin-top: 1.5rem;
  }

  .button {
    padding: 0.625rem 1.25rem;
    border-radius: 6px;
    font-size: 0.875rem;
    font-weight: 500;
    text-decoration: none;
    border: none;
    cursor: pointer;
    transition: all 0.15s;
    display: inline-block;
  }

  .button-primary {
    background: var(--primary);
    color: white;
  }

  .button-primary:hover {
    opacity: 0.9;
  }

  .button-secondary {
    background: var(--card-bg);
    color: var(--text-primary);
    border: 1px solid var(--border-color);
  }

  .button-secondary:hover {
    background: var(--hover-bg);
  }
</style>
