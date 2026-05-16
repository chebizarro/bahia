<script>
  import {
    assistantConnection,
    assistantUi,
    assistantSessions,
    activeAssistantSession,
    pendingAssistantRequests,
    setAssistantSidebarOpen,
    toggleAssistantCollapsed,
    setActiveAssistantSession,
    publishAssistantPrompt,
    publishAssistantApproval
  } from '$lib/stores/assistant.svelte.js';
  import AssistantTurn from './AssistantTurn.svelte';

  let { routeContext = null } = $props();

  let prompt = $state('');
  let submitting = $state(false);
  let error = $state('');

  const session = $derived(activeAssistantSession());
  const pendingCount = $derived(Object.keys(pendingAssistantRequests).length);
  const sidebarClass = $derived(assistantUi.collapsed ? 'collapsed' : 'expanded');
  const otherParticipants = $derived((session?.participants || []).filter((pubkey) => pubkey && pubkey !== assistantConnection.operatorPubkey));

  function shortPubkey(pubkey) {
    if (!pubkey) return '';
    return pubkey.length > 12 ? `${pubkey.slice(0, 6)}…${pubkey.slice(-4)}` : pubkey;
  }

  async function submitPrompt(event) {
    event.preventDefault();
    const value = prompt.trim();
    if (!value || submitting) return;
    submitting = true;
    error = '';
    try {
      await publishAssistantPrompt({ prompt: value, sessionId: session?.sessionId, routeContext });
      prompt = '';
    } catch (err) {
      error = err?.message || String(err);
    } finally {
      submitting = false;
    }
  }

  async function cancelSession() {
    if (!session?.sessionId) return;
    const planHash = session.lastPlanHash || session.currentPlan?.plan_hash || '';
    if (!planHash) return;
    try {
      await publishAssistantApproval({ sessionId: session.sessionId, planHash, decision: 'cancel' });
    } catch (err) {
      error = err?.message || String(err);
    }
  }
</script>

<aside class="assistant-sidebar {sidebarClass}" aria-label="Assistant sidebar">
  <header>
    <button class="icon" type="button" aria-label="Toggle assistant sidebar" onclick={() => setAssistantSidebarOpen(!assistantUi.open)}>
      {assistantUi.open ? '›' : '‹'}
    </button>
    {#if assistantUi.open}
      <div class="title">
        <strong>Assistant</strong>
        <span class="status {assistantConnection.status}">{assistantConnection.status}{pendingCount ? ` · ${pendingCount} still waiting` : ''}</span>
      </div>
      <button class="icon" type="button" aria-label="Collapse assistant details" onclick={toggleAssistantCollapsed}>{assistantUi.collapsed ? '+' : '−'}</button>
    {/if}
  </header>

  {#if assistantUi.open && !assistantUi.collapsed}
    <div class="body">
      <nav class="sessions" aria-label="Assistant sessions">
        {#if assistantSessions.length === 0}
          <p class="empty">No assistant sessions yet.</p>
        {:else}
          {#each assistantSessions as item}
            <button
              type="button"
              class:active={item.sessionId === session?.sessionId}
              onclick={() => setActiveAssistantSession(item.sessionId)}
            >
              <span>{item.transcriptSummary || item.sessionId}</span>
              <small>{item.state}</small>
            </button>
          {/each}
        {/if}
      </nav>

      {#if otherParticipants.length}
        <div class="participants" aria-label="Other session participants">
          <span>{otherParticipants.length + 1} operators</span>
          {#each otherParticipants as pubkey}
            <span class="participant" title={pubkey}>{shortPubkey(pubkey)}</span>
          {/each}
        </div>
      {/if}

      <section class="transcript" aria-label="Assistant transcript">
        {#if session?.transcript?.length}
          {#each session.transcript as item (item.id)}
            <AssistantTurn {item} {session} operatorPubkey={assistantConnection.operatorPubkey} />
          {/each}
        {:else}
          <div class="empty transcript-empty">
            Ask the assistant for help with this page. Responses remain event-backed; no client timeout marks turns failed.
          </div>
        {/if}
      </section>

      {#if session?.state === 'executing' || session?.state === 'blocked'}
        <button class="cancel" type="button" onclick={cancelSession}>Cancel stuck session</button>
      {/if}

      {#if error}
        <p class="error">{error}</p>
      {/if}

      <form class="composer" onsubmit={submitPrompt}>
        <textarea bind:value={prompt} placeholder="Ask the Bahia assistant…" rows="3" disabled={submitting || assistantConnection.status === 'waiting_auth'}></textarea>
        <button type="submit" disabled={!prompt.trim() || submitting}>{submitting ? 'Sending…' : 'Send'}</button>
      </form>
    </div>
  {/if}
</aside>

<style>
  .assistant-sidebar { position: fixed; top: 0; right: 0; height: 100vh; z-index: 50; background: var(--nav-bg); border-left: 1px solid var(--border-color); display: flex; flex-direction: column; width: var(--assistant-sidebar-width, 360px); overflow: hidden; }
  .assistant-sidebar.collapsed { width: 64px; }
  header { min-height: 56px; padding: 0.75rem; border-bottom: 1px solid var(--border-color); display: flex; align-items: center; gap: 0.75rem; }
  .title { flex: 1; display: grid; gap: 0.15rem; }
  .title strong { color: var(--text-primary); }
  .status { color: var(--text-muted); font-size: 0.75rem; }
  .status.live { color: var(--success); }
  .status.error, .status.disconnected { color: var(--error); }
  .icon { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 6px; min-width: 2rem; height: 2rem; cursor: pointer; }
  .body { min-height: 0; flex: 1; display: grid; grid-template-rows: auto 1fr auto auto; gap: 0.75rem; padding: 0.75rem; }
  .sessions { display: flex; gap: 0.5rem; overflow-x: auto; padding-bottom: 0.25rem; }
  .sessions button { min-width: 9rem; max-width: 12rem; text-align: left; background: var(--card-bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.5rem; cursor: pointer; }
  .sessions button.active { border-color: var(--primary); }
  .sessions span { display: block; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sessions small { color: var(--text-muted); }
  .participants { display: flex; align-items: center; gap: 0.35rem; color: var(--text-muted); font-size: 0.75rem; flex-wrap: wrap; }
  .participant { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 999px; padding: 0.15rem 0.4rem; }
  .transcript { min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 0.75rem; padding-right: 0.2rem; }
  .empty { color: var(--text-muted); font-size: 0.875rem; }
  .transcript-empty { border: 1px dashed var(--border-color); border-radius: 10px; padding: 1rem; }
  .composer { display: grid; gap: 0.5rem; }
  textarea { width: 100%; resize: vertical; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.75rem; font: inherit; }
  .composer button, .cancel { background: var(--primary); color: white; border: 0; border-radius: 8px; padding: 0.65rem 0.85rem; cursor: pointer; font-weight: 700; }
  .composer button:disabled { opacity: 0.5; cursor: not-allowed; }
  .cancel { background: var(--warning); color: #111827; }
  .error { color: var(--error); font-size: 0.875rem; }
  @media (max-width: 900px) { .assistant-sidebar { width: min(100vw, 360px); } }
</style>
