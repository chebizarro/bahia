<script>
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { authState, initializeAuth } from '$lib/stores/auth.js';
  
  // Track if we've done the initial check
  let initialized = false;
  
  onMount(() => {
    // Ensure auth is initialized
    if ($authState.status === 'unknown') {
      initializeAuth();
    }
    initialized = true;
  });
  
  // Reactive redirect when auth state changes
  $: if (initialized && $authState.status !== 'unknown' && $authState.status !== 'checking' && $authState.status !== 'authenticating') {
    if (!$authState.backendAuthenticated && $authState.status !== 'authenticated') {
      goto('/');
    }
  }
  
  $: isLoading = !initialized || $authState.status === 'unknown' || $authState.status === 'checking' || $authState.status === 'authenticating';
  $: isAuthorized = $authState.backendAuthenticated || $authState.status === 'authenticated';
</script>

{#if isLoading}
  <div class="auth-loading">
    <div class="spinner"></div>
    <p>Checking authentication...</p>
  </div>
{:else if isAuthorized}
  <slot />
{:else}
  <div class="auth-redirect">
    <p>Redirecting to login...</p>
  </div>
{/if}

<style>
  .auth-loading, .auth-redirect {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    min-height: 200px;
    gap: 1rem;
    color: var(--text-muted);
  }
  
  .spinner {
    width: 32px;
    height: 32px;
    border: 3px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
