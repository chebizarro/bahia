<script>
  import {
    assistantConnection,
    publishAssistantPrompt,
    publishAssistantApproval
  } from '$lib/stores/assistant.svelte.js';

  let { session = null, routeContext = null, panelOpen = false } = $props();

  let prompt = $state('');
  let submitting = $state(false);
  let error = $state('');
  let textarea;

  const canCancel = $derived(session?.state === 'executing' || session?.state === 'blocked');
  const disabled = $derived(submitting || assistantConnection.status === 'waiting_auth');

  $effect(() => {
    if (panelOpen && textarea) textarea.focus();
  });

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

  function handleKeydown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      event.currentTarget.form?.requestSubmit();
    }
  }

  async function cancelSession() {
    if (!session?.sessionId || submitting) return;
    const planHash = session.lastPlanHash || session.currentPlan?.plan_hash || '';
    if (!planHash) return;
    submitting = true;
    error = '';
    try {
      await publishAssistantApproval({ sessionId: session.sessionId, planHash, decision: 'cancel' });
    } catch (err) {
      error = err?.message || String(err);
    } finally {
      submitting = false;
    }
  }
</script>

{#if canCancel}
  <button class="cancel" type="button" disabled={submitting} onclick={cancelSession}>Cancel session</button>
{/if}

{#if error}
  <p class="error">{error}</p>
{/if}

<form class="composer" onsubmit={submitPrompt}>
  <textarea
    bind:this={textarea}
    bind:value={prompt}
    placeholder="Ask the Bahia assistant…"
    rows="1"
    disabled={disabled}
    onkeydown={handleKeydown}
  ></textarea>
  <button type="submit" disabled={!prompt.trim() || submitting}>{submitting ? 'Sending…' : 'Send'}</button>
</form>

<style>
  .composer {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.5rem;
    align-items: end;
    border-top: 1px solid var(--border-color);
    padding: 0.75rem;
  }
  textarea {
    width: 100%;
    min-height: 2.65rem;
    max-height: 7.5rem;
    resize: vertical;
    background: var(--bg);
    color: var(--text-primary);
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 0.7rem 0.75rem;
    font: inherit;
  }
  .composer button, .cancel {
    background: var(--primary);
    color: white;
    border: 0;
    border-radius: 10px;
    padding: 0.75rem 0.9rem;
    cursor: pointer;
    font-weight: 700;
  }
  .composer button:disabled, .cancel:disabled { opacity: 0.5; cursor: not-allowed; }
  .cancel { margin: 0 0.75rem; background: var(--warning); color: #111827; }
  .error { color: var(--error); font-size: 0.875rem; margin: 0 0.75rem; }
</style>
