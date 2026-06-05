<script>
  import {
    assistantConnection,
    assistantUi,
    activeAssistantSession,
    pendingAssistantRequests,
    closeAssistantPanel
  } from '$lib/stores/assistant.svelte.js';
  import AssistantComposer from './AssistantComposer.svelte';
  import AssistantSessionTabs from './AssistantSessionTabs.svelte';
  import AssistantTurn from './AssistantTurn.svelte';

  let { routeContext = null, defaultSelectedRefs = [] } = $props();

  const session = $derived(activeAssistantSession());
  const pendingCount = $derived(Object.keys(pendingAssistantRequests).length);
  const otherParticipants = $derived((session?.participants || []).filter((pubkey) => pubkey && pubkey !== assistantConnection.operatorPubkey));
  const state = $derived(assistantUi.panelOpen ? 'open' : 'closed');

  function shortPubkey(pubkey) {
    if (!pubkey) return '';
    return pubkey.length > 12 ? `${pubkey.slice(0, 6)}…${pubkey.slice(-4)}` : pubkey;
  }

  function handleKeydown(event) {
    if (event.key === 'Escape') closeAssistantPanel();
  }
</script>

<div
  class="assistant-panel"
  data-state={state}
  role="dialog"
  aria-label="Assistant chat"
  aria-hidden={!assistantUi.panelOpen}
  onkeydown={handleKeydown}
>
  <header>
    <div class="title">
      <span class="indicator {assistantConnection.status}" aria-hidden="true"></span>
      <div>
        <strong>Assistant</strong>
        <span>{assistantConnection.status}{pendingCount ? ` · ${pendingCount} still waiting` : ''}</span>
      </div>
    </div>
    <button class="close" type="button" aria-label="Close assistant chat" onclick={closeAssistantPanel}>×</button>
  </header>

  <AssistantSessionTabs activeSessionId={session?.sessionId || assistantUi.activeSessionId} />

  <div class="body">
    {#if otherParticipants.length}
      <div class="participants" aria-label="Other session participants">
        <span>{otherParticipants.length + 1} operators</span>
        {#each otherParticipants as pubkey}
          <span class="participant" title={pubkey}>{shortPubkey(pubkey)}</span>
        {/each}
      </div>
    {/if}

    {#if assistantConnection.status === 'disconnected' || assistantConnection.status === 'error'}
      <div class="connection-banner" role="status">
        {assistantConnection.lastError || 'Assistant connection interrupted.'}
      </div>
    {/if}

    <section class="transcript" aria-label="Assistant transcript">
      {#if session?.transcript?.length}
        {#each session.transcript as item (item.id)}
          <AssistantTurn {item} {session} operatorPubkey={assistantConnection.operatorPubkey} />
        {/each}
      {:else}
        <div class="empty transcript-empty">
          <strong>Ask the Bahia assistant for help</strong>
          <span>Responses remain event-backed and update from relay events.</span>
        </div>
      {/if}
    </section>
  </div>

  <AssistantComposer {session} {routeContext} {defaultSelectedRefs} panelOpen={assistantUi.panelOpen} />
</div>

<style>
  .assistant-panel {
    position: fixed;
    right: 32px;
    bottom: 100px;
    z-index: 8999;
    width: 400px;
    height: min(560px, calc(100vh - 120px));
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 16px;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
    display: flex;
    flex-direction: column;
    overflow: hidden;
    transform-origin: bottom right;
    transition: transform 200ms cubic-bezier(0.32, 0.72, 0, 1), opacity 200ms ease;
  }
  .assistant-panel[data-state='closed'] { transform: scale(0.92) translateY(8px); opacity: 0; pointer-events: none; }
  .assistant-panel[data-state='open'] { transform: scale(1) translateY(0); opacity: 1; }
  header {
    min-height: 48px;
    padding: 0.65rem 0.75rem;
    border-bottom: 1px solid var(--border-color);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .title { display: flex; align-items: center; gap: 0.6rem; min-width: 0; }
  .title div { display: grid; gap: 0.05rem; min-width: 0; }
  .title strong { color: var(--text-primary); }
  .title span:not(.indicator) { color: var(--text-muted); font-size: 0.75rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .indicator { width: 0.65rem; height: 0.65rem; border-radius: 50%; background: var(--text-muted); flex: 0 0 auto; }
  .indicator.live { background: var(--success); }
  .indicator.waiting_auth, .indicator.bootstrapping { background: var(--warning); }
  .indicator.error, .indicator.disconnected { background: var(--error); }
  .close { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 8px; width: 2rem; height: 2rem; cursor: pointer; font-size: 1.2rem; line-height: 1; }
  .body { min-height: 0; flex: 1; display: flex; flex-direction: column; gap: 0.75rem; padding: 0.75rem; }
  .participants { display: flex; align-items: center; gap: 0.35rem; color: var(--text-muted); font-size: 0.75rem; flex-wrap: wrap; }
  .participant { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 999px; padding: 0.15rem 0.4rem; }
  .connection-banner { color: #111827; background: var(--warning); border-radius: 8px; padding: 0.5rem 0.65rem; font-size: 0.875rem; }
  .transcript { min-height: 0; flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 0.75rem; padding-right: 0.2rem; }
  .empty { color: var(--text-muted); font-size: 0.875rem; }
  .transcript-empty { min-height: 100%; border: 1px dashed var(--border-color); border-radius: 12px; padding: 1rem; display: grid; place-content: center; gap: 0.35rem; text-align: center; }
  .transcript-empty strong { color: var(--text-primary); }

  @media (max-width: 900px) {
    .assistant-panel { width: 380px; height: min(520px, calc(100vh - 100px)); }
  }

  @media (max-width: 640px) {
    .assistant-panel { left: 12px; right: 12px; bottom: 76px; width: auto; height: calc(100vh - 100px); border-radius: 12px; }
  }

  @media (prefers-reduced-motion: reduce) {
    .assistant-panel { transition: none; }
  }
</style>
