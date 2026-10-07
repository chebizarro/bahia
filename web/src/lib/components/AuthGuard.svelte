<!---->
  AuthGuard performs the role-based access check described in docs/architecture/web-store-first.md.

  Replaces the previous REST probe with a reactive role check.
  No spinner, no REST probe, no discovery gate.

  Protected routes render immediately from the store for authenticated users.
  Role checks gate mutation affordances, not view rendering.
-->
<script>
  import { goto } from '$app/navigation';
  import { authState, isAuthenticated, initializeAuth } from '$lib/stores/auth.js';
  import { hasAnyRole } from '$lib/stores/auth-roles.svelte.js';

  let { children, requiredRoles = [], requiresRestCompatibility = false } = $props();

  let initialized = $state(false);

  $effect(() => {
    if (initialized) return;
    void (async () => {
      await initializeAuth();
      initialized = true;
    })();
  });

  const isLoading = $derived(
    !initialized ||
      authState.status === 'unknown' ||
      authState.status === 'checking' ||
      authState.status === 'authenticating'
  );

  // §6.2: A persisted signer-verified session is authenticated.
  // No backendAuthenticated flag — roles come from relay membership events.
  const isAuthorized = $derived(isAuthenticated());

  const roleAuthorized = $derived(
    requiredRoles.length === 0 || hasAnyRole(requiredRoles)
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
