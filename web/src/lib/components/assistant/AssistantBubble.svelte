<script>
  import { toggleAssistantPanel } from '$lib/stores/assistant.svelte.js';

  let { panelOpen = false, hasUnread = false, status = 'idle' } = $props();

  const label = $derived(panelOpen ? 'Close assistant chat' : 'Open assistant chat');
  const badgeClass = $derived(status === 'disconnected' || status === 'error' ? 'connection' : 'unread');
  const showBadge = $derived(hasUnread || status === 'disconnected' || status === 'error');

  function attachToggle(node) {
    node.dataset.toggleAttached = 'true';
    node.addEventListener('click', toggleAssistantPanel);
    return {
      destroy() {
        delete node.dataset.toggleAttached;
        node.removeEventListener('click', toggleAssistantPanel);
      }
    };
  }
</script>

<button
  class="assistant-bubble"
  class:open={panelOpen}
  class:has-unread={hasUnread}
  type="button"
  aria-label={label}
  aria-expanded={panelOpen}
  use:attachToggle
>
  {#if panelOpen}
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M18.3 5.71 12 12l6.3 6.29-1.41 1.41L10.59 13.41 4.29 19.7 2.88 18.29 9.17 12 2.88 5.71 4.29 4.3l6.3 6.29 6.3-6.29z" />
    </svg>
  {:else}
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M4 4h16c1.1 0 2 .9 2 2v10c0 1.1-.9 2-2 2H8.83L4 22v-4H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2Zm0 2v10h2v1.83L8.17 16H20V6H4Z" />
    </svg>
  {/if}

  {#if showBadge}
    <span class="badge {badgeClass}" aria-hidden="true"></span>
  {/if}
  <span class="sr-only" aria-live="polite">{hasUnread ? 'New assistant activity' : ''}</span>
</button>

<style>
  .assistant-bubble {
    position: fixed;
    right: 32px;
    bottom: 32px;
    z-index: 9000;
    width: 56px;
    height: 56px;
    border-radius: 50%;
    border: 0;
    background: var(--primary);
    color: white;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25);
    cursor: pointer;
    display: grid;
    place-items: center;
    transition: transform 160ms ease, box-shadow 160ms ease, opacity 160ms ease;
  }
  .assistant-bubble:hover { transform: scale(1.08); box-shadow: 0 6px 20px rgba(0, 0, 0, 0.3); }
  .assistant-bubble:active { transform: scale(0.95); }
  .assistant-bubble.open { opacity: 0.85; }
  .assistant-bubble.has-unread { animation: bubble-pulse 2s ease-in-out infinite; }
  svg { width: 24px; height: 24px; fill: currentColor; }
  .badge { position: absolute; top: 4px; right: 4px; width: 12px; height: 12px; border-radius: 50%; border: 2px solid white; }
  .badge.unread { background: var(--error); }
  .badge.connection { background: var(--warning); }
  .sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }

  @keyframes bubble-pulse {
    0%, 100% { box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25); }
    50% { box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25), 0 0 0 8px rgba(99, 102, 241, 0.15); }
  }

  @media (max-width: 900px) {
    .assistant-bubble { width: 52px; height: 52px; }
  }

  @media (max-width: 640px) {
    .assistant-bubble { right: 16px; bottom: 16px; width: 48px; height: 48px; }
  }

  @media (prefers-reduced-motion: reduce) {
    .assistant-bubble { transition: none; }
    .assistant-bubble.has-unread { animation: none; }
  }
</style>
