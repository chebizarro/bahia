<script>
  import { onMount, onDestroy } from 'svelte';
  import Nav from '$lib/components/Nav.svelte';
  import { loadAll, subscribeToEvents, unsubscribeFromEvents } from '$lib/stores';

  onMount(() => {
    loadAll();
    subscribeToEvents();
  });

  onDestroy(() => {
    unsubscribeFromEvents();
  });
</script>

<div class="app">
  <Nav />
  <main>
    <slot />
  </main>
</div>

<style>
  :global(*) {
    box-sizing: border-box;
    margin: 0;
    padding: 0;
  }
  :global(body) {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    background: var(--bg, #0a0a14);
    color: var(--text-primary, #e5e5e5);
    line-height: 1.5;
  }
  :global(:root) {
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
  .app {
    min-height: 100vh;
  }
  main {
    padding: 2rem;
    max-width: 1400px;
    margin: 0 auto;
  }
</style>
