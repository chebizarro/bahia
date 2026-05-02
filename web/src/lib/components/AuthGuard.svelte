<script>
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import { authState, isAuthenticated, initializeAuth } from '$lib/stores/auth.js';

  let { children, requiredRoles = [] } = $props();

  let initialized = $state(false);

  $effect(() => {
    if (initialized) return;

    initialized = true;
    if (untrack(() => authState.status) === 'unknown') {
      void untrack(() => initializeAuth());
    }
  });

  const isLoading = $derived(
    !initialized ||
      authState.status === 'unknown' ||
      authState.status === 'checking' ||
      authState.status === 'authenticating'
  );

  const isAuthorized = $derived(authState.backendAuthenticated || isAuthenticated());

  const roleAuthorized = $derived(
    requiredRoles.length === 0 ||
      requiredRoles.some((role) =>
        (Array.isArray(authState.roles) && authState.roles.includes(role)) ||
        (Array.isArray(authState?.capabilities?.roles) && authState.capabilities.roles.includes(role))
      )
  );

  $effect(() => {
    if (!isLoading && !isAuthorized) {
      goto('/');
    }
  });
</script>

{#if isLoading}
  <div class="auth-loading">
    <div class="spinner"></div>
    <p>Checking authentication...</p>
  </div>
{:else if isAuthorized && roleAuthorized}
  {@render children?.()}
{:else if !isAuthorized}
  <div class="auth-redirect">
    <p>Redirecting to login...</p>
  </div>
{:else}
  <div class="auth-redirect">
    <p>You do not have permission to view this page.</p>
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
