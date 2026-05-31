<script>
  import { assistantSessions, createAssistantSessionId, setActiveAssistantSession } from '$lib/stores/assistant.svelte.js';

  let { activeSessionId = '' } = $props();

  function selectSession(sessionId) {
    setActiveAssistantSession(sessionId);
  }

  function createSession() {
    setActiveAssistantSession(createAssistantSessionId());
  }
</script>

<nav class="sessions" aria-label="Assistant sessions">
  {#if assistantSessions.length === 0}
    <p class="empty">No assistant sessions yet.</p>
  {:else}
    {#each assistantSessions as session}
      <button
        type="button"
        class:active={session.sessionId === activeSessionId}
        onclick={() => selectSession(session.sessionId)}
      >
        <span>{session.transcriptSummary || session.sessionId}</span>
        <small>{session.state}</small>
      </button>
    {/each}
  {/if}
  <button class="new-session" type="button" aria-label="Start a new assistant session" onclick={createSession}>+</button>
</nav>

<style>
  .sessions {
    min-height: 44px;
    display: flex;
    align-items: stretch;
    gap: 0.5rem;
    overflow-x: auto;
    border-bottom: 1px solid var(--border-color);
    padding: 0.35rem 0.75rem 0;
  }
  .sessions button {
    min-width: 8rem;
    max-width: 11rem;
    text-align: left;
    background: transparent;
    color: var(--text-primary);
    border: 0;
    border-bottom: 2px solid transparent;
    padding: 0.35rem 0.25rem 0.45rem;
    cursor: pointer;
  }
  .sessions button.active { border-bottom-color: var(--primary); }
  .sessions span { display: block; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sessions small { color: var(--text-muted); }
  .new-session { min-width: 2rem; text-align: center; font-weight: 700; }
  .empty { color: var(--text-muted); font-size: 0.875rem; align-self: center; white-space: nowrap; }
</style>
