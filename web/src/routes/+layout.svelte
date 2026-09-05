<script>
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import Nav from '$lib/components/Nav.svelte';
  import ErrorBoundary from '$lib/components/ErrorBoundary.svelte';
  import AuthGuard from '$lib/components/AuthGuard.svelte';
  import ToastContainer from '$lib/components/ToastContainer.svelte';
  import AssistantChat from '$lib/components/assistant/AssistantChat.svelte';
  import { currentRouteDocsRef } from '$lib/components/nav-model.js';
  import { loadAll, unsubscribeFromEvents } from '$lib/stores';
  import { eagerRelayConnect } from '$lib/stores/system.svelte.js';
  import { bootstrapAssistant, disconnectAssistant } from '$lib/stores/assistant.svelte.js';
  import { theme } from '$lib/stores/theme.js';
  import { authState, initializeAuth, isAuthenticated } from '$lib/stores/auth.js';
  import { canAccessRoute } from '$lib/auth/route-access.js';
  import { createVersionReloadWatcher } from '$lib/version-reload.js';
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
  const assistantRouteContext = $derived({
    route: page.url.pathname,
    params: page.params || {}
  });
  const assistantDefaultSelectedRefs = $derived(
    currentRouteDocsRef(page.url.pathname) ? [currentRouteDocsRef(page.url.pathname)] : []
  );
  let assistantBootstrappedForPubkey = $state('');

  onMount(() => createVersionReloadWatcher().start());

  $effect(() => {
    let active = true;

    queueMicrotask(() => {
      if (!active) return;
      loadAll();

      initializeAuth().catch((error) => {
        console.error('Auth bootstrap failed before controlplane load:', error);
      });

      eagerRelayConnect().catch((error) => {
        console.error('Eager relay connection failed before controlplane load:', error);
      });
    });

    return () => {
      active = false;
      unsubscribeFromEvents();
      disconnectAssistant();
    };
  });

  $effect(() => {
    const pubkey = authState.status === 'authenticated' ? authState.pubkey : '';
    if (!pubkey || assistantBootstrappedForPubkey === pubkey) return;

    assistantBootstrappedForPubkey = pubkey;
    bootstrapAssistant({ force: true }).catch((error) => {
      assistantBootstrappedForPubkey = '';
      console.error('Assistant bootstrap failed:', error);
    });
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
  <AssistantChat routeContext={assistantRouteContext} defaultSelectedRefs={assistantDefaultSelectedRefs} />
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
    --code-bg: rgba(148, 163, 184, 0.16);
    --code-text: #e5e5e5;
    --code-block-bg: #111827;
    --code-block-text: #f8fafc;
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
    --code-bg: rgba(15, 23, 42, 0.08);
    --code-text: #212529;
    --code-block-bg: #111827;
    --code-block-text: #f8fafc;
  }
  :global(pre),
  :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
  }
  :global(pre) {
    background: var(--code-block-bg);
    color: var(--code-block-text);
  }
  :global(pre code) {
    color: inherit;
    background: transparent;
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
