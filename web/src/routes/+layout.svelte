<script>
  import { page } from '$app/state';
  import Nav from '$lib/components/Nav.svelte';
  import ErrorBoundary from '$lib/components/ErrorBoundary.svelte';
  import AuthGuard from '$lib/components/AuthGuard.svelte';
  import ToastContainer from '$lib/components/ToastContainer.svelte';
  import { loadAll, loadSystemInfo, unsubscribeFromEvents } from '$lib/stores';
  import { theme } from '$lib/stores/theme.js';
  import { authState, initializeAuth, isAuthenticated } from '$lib/stores/auth.js';
  import { canAccessRoute } from '$lib/auth/route-access.js';
  /**
   * @typedef {Object} Props
   * @property {import('svelte').Snippet} [children]
   */

  /** @type {Props} */
  let { children } = $props();

  const routeAccess = $derived(
    canAccessRoute({
      pathname: page.url.pathname,
      authState,
      isAuthenticated: isAuthenticated()
    })
  );

  const isProtectedRoute = $derived(routeAccess.protectedRoute);

  $effect(() => {
    let active = true;

    queueMicrotask(() => {
      if (!active) return;
      initializeAuth()
        .then(() => loadSystemInfo())
        .catch((error) => {
          console.error('Shared bootstrap failed before controlplane load:', error);
        })
        .finally(() => {
          if (!active) return;
          loadAll();
        });
    });

    return () => {
      active = false;
      unsubscribeFromEvents();
    };
  });
</script>

<div class="app">
  <Nav />
  <main>
    <ErrorBoundary>
      {#if isProtectedRoute}
        <AuthGuard requiredRoles={routeAccess.requiredRoles} requiresRestCompatibility={routeAccess.requiresRestCompatibility}>
          {@render children?.()}
        </AuthGuard>
      {:else}
        {@render children?.()}
      {/if}
    </ErrorBoundary>
  </main>
</div>

<ToastContainer />

<style>
  :global(*) {
    box-sizing: border-box;
    margin: 0;
    padding: 0;
  }
  :global(*:not(body)) {
    transition: background-color 0.2s, border-color 0.2s, color 0.2s;
  }
  :global(body) {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    background: var(--bg);
    color: var(--text-primary);
    line-height: 1.5;
    transition: background-color 0.2s, color 0.2s;
  }
  :global(:root),
  :global([data-theme='dark']) {
    --bg: #0a0a14;
    --nav-bg: #0f0f1a;
    --card-bg: #1a1a2e;
    --hover-bg: #252540;
    --border-color: #2a2a4a;
    --text-primary: #e5e5e5;
    --text-muted: #888;
    --primary: #6366f1;
    --success: #10b981;
    --warning: #f59e0b;
    --error: #ef4444;
  }
  :global([data-theme='light']) {
    --bg: #f8f9fa;
    --nav-bg: #ffffff;
    --card-bg: #ffffff;
    --hover-bg: #e9ecef;
    --border-color: #dee2e6;
    --text-primary: #212529;
    --text-muted: #6c757d;
    --primary: #4f46e5;
    --success: #059669;
    --warning: #d97706;
    --error: #dc2626;
  }
  .app {
    min-height: 100vh;
  }
  main {
    padding: 2rem;
    max-width: 1400px;
    margin: 0 auto;
  }
</style>
