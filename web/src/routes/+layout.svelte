<script>
  import { onMount, untrack } from 'svelte';
  import { page } from '$app/state';
  import Nav from '$lib/components/Nav.svelte';
  import ErrorBoundary from '$lib/components/ErrorBoundary.svelte';
  import AuthGuard from '$lib/components/AuthGuard.svelte';
  import ToastContainer from '$lib/components/ToastContainer.svelte';
  import AssistantChat from '$lib/components/assistant/AssistantChat.svelte';
  import { currentRouteDocsRef } from '$lib/components/nav-model.js';
  import { bootstrapControlplane, disconnectControlplane } from '$lib/stores';
  import { boot, getEventStore, getServicePubkey, prefetchRelayLimits, shutdown } from '$lib/nostr/boot.js';
  import { resumeIntentClient, stopIntentClient } from '$lib/nostr/intent-client.svelte.js';
  import { startRoleDerivation, stopRoleDerivation } from '$lib/stores/auth-roles.svelte.js';
  import { initServiceStoreBinding, teardownServiceStoreBinding } from '$lib/stores/collections/services.svelte.js';
  import { initEnvironmentStoreBinding, teardownEnvironmentStoreBinding } from '$lib/stores/collections/environments.svelte.js';
  import { initCoreDeploymentStoreBindings, teardownCoreDeploymentStoreBindings } from '$lib/stores/collections/deployments.svelte.js';
  import { initWorkerStoreBinding, teardownWorkerStoreBinding } from '$lib/stores/collections/workers.svelte.js';
  import { initOperationStoreBinding, teardownOperationStoreBinding } from '$lib/stores/collections/operations.svelte.js';
  import { initActivityStoreBinding, teardownActivityStoreBinding } from '$lib/stores/collections/activity.svelte.js';
  import { initBackupStoreBinding, teardownBackupStoreBinding } from '$lib/stores/collections/backup.svelte.js';
  import { initMLStoreBinding, teardownMLStoreBinding } from '$lib/stores/collections/ml.svelte.js';
  import { initSBOMStoreBinding, teardownSBOMStoreBinding } from '$lib/stores/collections/sbom.svelte.js';
  import { initContinuityStoreBinding, teardownContinuityStoreBinding } from '$lib/nostr/continuity';
  import { initSoulFactoryStoreBinding, teardownSoulFactoryStoreBinding } from '$lib/stores/souls.svelte.js';
  import { eagerRelayConnect } from '$lib/stores/system.svelte.js';
  import { bootstrapAssistant, disconnectAssistant } from '$lib/stores/assistant.svelte.js';
  import { theme } from '$lib/stores/theme.js';
  import { authState, initializeAuth, isAuthenticated, resolveActiveSigner } from '$lib/stores/auth.js';
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
  let eventStoreReady = $state(false);

  onMount(() => createVersionReloadWatcher().start());

  $effect(() => {
    let active = true;

    queueMicrotask(async () => {
      if (!active) return;

      // Phase 4 W1-S2: Open the event store first so derived stores
      // render from persisted data immediately (before network).
      try {
        await boot();
        eventStoreReady = Boolean(getEventStore());
        initServiceStoreBinding();
        initEnvironmentStoreBinding();
        initCoreDeploymentStoreBindings();
        initWorkerStoreBinding();
        initOperationStoreBinding();
        initActivityStoreBinding();
        initBackupStoreBinding();
        initMLStoreBinding();
        initSBOMStoreBinding();
        initContinuityStoreBinding();
        initSoulFactoryStoreBinding();
      } catch (err) {
        console.warn('[layout] boot() failed:', err);
      }

      // Connect the single pool in the background; EOSE only updates the badge.
      void bootstrapControlplane().then((result) => {
        if (!result.ok) console.error('Nostr controlplane bootstrap failed:', result.reason);
      });

      initializeAuth().catch((error) => {
        console.error('Auth bootstrap failed before controlplane load:', error);
      });

      eagerRelayConnect().catch((error) => {
        console.error('Eager relay connection failed before controlplane load:', error);
      });
    });

    return () => {
      active = false;
      teardownServiceStoreBinding();
      teardownEnvironmentStoreBinding();
      teardownCoreDeploymentStoreBindings();
      teardownWorkerStoreBinding();
      teardownOperationStoreBinding();
      teardownActivityStoreBinding();
      teardownBackupStoreBinding();
      teardownMLStoreBinding();
      teardownSBOMStoreBinding();
      teardownContinuityStoreBinding();
      teardownSoulFactoryStoreBinding();
      stopRoleDerivation();
      disconnectControlplane();
      disconnectAssistant();
    };
  });

  $effect(() => {
    const pubkey = authState.status === 'authenticated' ? authState.pubkey : '';
    if (!eventStoreReady) return;
    untrack(() => { initContinuityStoreBinding(); initSoulFactoryStoreBinding(); });
  });

  $effect(() => {
    const pubkey = authState.status === 'authenticated' ? authState.pubkey : '';
    if (!eventStoreReady || !pubkey) return;
    prefetchRelayLimits();
    void resumeIntentClient().catch(error => console.error('[layout] intent client failed:', error));
    return stopIntentClient;
  });

  $effect(() => {
    const pubkey = authState.status === 'authenticated' ? authState.pubkey : '';
    if (!eventStoreReady || !pubkey) return;
    const signerAvailable = authState.authMethod === 'nip46' ? authState.nip46Available : authState.extensionAvailable;
    if (!signerAvailable) return;
    const store = getEventStore();
    const servicePubkey = getServicePubkey();
    if (!store || !servicePubkey) return;
    untrack(() => {
      let signer;
      try { signer = resolveActiveSigner(); }
      catch (error) {
        console.warn('[layout] role derivation awaits an available signer:', error);
        return;
      }
      void startRoleDerivation({ store, userPubkey: pubkey, servicePubkey, signer });
    });
    return () => untrack(stopRoleDerivation);
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
