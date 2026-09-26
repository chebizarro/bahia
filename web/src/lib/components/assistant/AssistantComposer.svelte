<script>
  import {
    assistantConnection,
    assistantUi,
    pendingAssistantRequests,
    publishAssistantPrompt,
    publishAssistantCancellation
  } from '$lib/stores/assistant.svelte.js';
  import { mergeAssistantRefs } from './assistant-refs.js';

  let {
    session = null,
    routeContext = null,
    selectedRefs = [],
    defaultSelectedRefs = [],
    panelOpen = false
  } = $props();

  let prompt = $state('');
  let promptSubmitting = $state(false);
  let cancelSubmitting = $state(false);
  let workflow = $state('');
  let error = $state('');
  let dismissedRefs = $state([]);
  let textarea;

  const visibleSelectedRefs = $derived(mergeAssistantRefs({ selectedRefs, defaultSelectedRefs, dismissedRefs }));
  const runActive = $derived(session?.executionVersion === 2 && !['completed', 'failed', 'cancelled'].includes(session?.phase));
  const canCancel = $derived(session?.authoritative && Boolean(session?.currentRunId) &&
    ['proposing', 'awaiting_approval', 'executing', 'waiting_async', 'blocked'].includes(session?.phase));
  const pendingPrompt = $derived(Object.values(pendingAssistantRequests).some((request) => request.sessionId === (session?.sessionId || assistantUi.activeSessionId)));
  const disabled = $derived(promptSubmitting || pendingPrompt || runActive || session?.executionVersion === 1 || assistantConnection.status === 'waiting_auth');
  const selectedWorkflow = $derived(session?.workflow || workflow);

  $effect(() => {
    if (panelOpen && textarea) textarea.focus();
  });

  async function submitPrompt(event) {
    event.preventDefault();
    const value = prompt.trim();
    if (!value || disabled) return;
    promptSubmitting = true;
    error = '';
    prompt = '';
    try {
      await publishAssistantPrompt({
        prompt: value,
        workflow: selectedWorkflow,
        sessionId: session?.sessionId,
        routeContext,
        selectedRefs: visibleSelectedRefs.map((ref) => ref.ref)
      });
    } catch (err) {
      error = `Request outcome unknown / reconnecting: ${err?.message || String(err)}`;
    } finally {
      promptSubmitting = false;
    }
  }

  function dismissRef(ref) {
    dismissedRefs = Array.from(new Set([...dismissedRefs, ref]));
  }

  function handleKeydown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      event.currentTarget.form?.requestSubmit();
    }
  }

  async function cancelSession() {
    if (!canCancel || cancelSubmitting) return;
    cancelSubmitting = true;
    error = '';
    try {
      await publishAssistantCancellation({ sessionId: session.sessionId, runId: session.currentRunId, scope: 'run' });
    } catch (err) {
      error = `Cancellation request outcome unknown / reconnecting: ${err?.message || String(err)}`;
    } finally {
      cancelSubmitting = false;
    }
  }

</script>

{#if canCancel}
  <button class="cancel" type="button" disabled={cancelSubmitting} onclick={cancelSession}>{cancelSubmitting ? 'Cancelling…' : 'Cancel run'}</button>
{/if}

{#if error}
  <p class="error">{error}</p>
{/if}

{#if visibleSelectedRefs.length}
  <div class="selected-refs" aria-label="Selected assistant references">
    <span class="selected-refs-label">References</span>
    {#each visibleSelectedRefs as item (item.ref)}
      <span class="ref-pill {item.type}">
        {#if item.href}
          <a href={item.href} target={item.href.startsWith('/docs/') ? undefined : '_blank'} rel={item.href.startsWith('/docs/') ? undefined : 'noreferrer'}>{item.label}</a>
        {:else}
          <span>{item.label}</span>
        {/if}
        <code>{item.ref}</code>
        {#if item.dismissible}
          <button type="button" aria-label={`Remove ${item.label} reference`} onclick={() => dismissRef(item.ref)}>×</button>
        {/if}
      </span>
    {/each}
  </div>
{/if}

{#if !session?.workflow && session?.executionVersion !== 1}
  <label class="workflow-selector">Workflow
    <select bind:value={workflow} aria-label="Assistant workflow">
      <option value="">Service default</option>
      <option value="batch">Batch plan</option>
      <option value="iterative">Iterative</option>
    </select>
  </label>
{/if}
{#if session?.executionVersion === 1}<p class="history-note">This v1 session is read-only. Start a new session to continue.</p>{/if}
<form class="composer" onsubmit={submitPrompt}>
  <textarea
    bind:this={textarea}
    bind:value={prompt}
    placeholder="Ask the Bahia assistant…"
    rows="1"
    disabled={disabled}
    onkeydown={handleKeydown}
  ></textarea>
  <button type="submit" disabled={!prompt.trim() || disabled}>{promptSubmitting ? 'Sending…' : 'Send'}</button>
</form>

<style>
  .selected-refs {
    border-top: 1px solid var(--border-color);
    padding: 0.65rem 0.75rem 0;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.45rem;
  }
  .selected-refs-label {
    color: var(--text-muted);
    font-size: 0.75rem;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .ref-pill {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    max-width: 100%;
    border: 1px solid var(--border-color);
    border-radius: 999px;
    background: var(--hover-bg);
    color: var(--text-primary);
    padding: 0.25rem 0.35rem 0.25rem 0.55rem;
    font-size: 0.75rem;
  }
  .ref-pill.docs {
    background: color-mix(in srgb, var(--primary, #6366f1) 16%, transparent);
  }
  .ref-pill a {
    color: inherit;
    font-weight: 700;
    text-decoration: none;
  }
  .ref-pill a:hover,
  .ref-pill a:focus-visible {
    text-decoration: underline;
  }
  .ref-pill code {
    color: var(--text-muted);
    font-size: 0.7rem;
  }
  .ref-pill button {
    width: 1.35rem;
    height: 1.35rem;
    border: 0;
    border-radius: 999px;
    background: var(--card-bg);
    color: var(--text-primary);
    cursor: pointer;
    line-height: 1;
  }
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
  .workflow-selector, .history-note { margin: 0.5rem 0.75rem; color: var(--text-muted); font-size: 0.8rem; }
  select { margin-left: 0.5rem; }
  .error { color: var(--error); font-size: 0.875rem; margin: 0 0.75rem; }
</style>
