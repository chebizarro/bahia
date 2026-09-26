<script>
  import {
    assistantConnection,
    assistantUi,
    pendingAssistantRequests,
    publishAssistantPrompt,
    publishAssistantCancellation
  } from '$lib/stores/assistant.svelte.js';
  import {
    ASSISTANT_EXECUTION_CANCELLABLE_PHASES,
    ASSISTANT_EXECUTION_TERMINAL_PHASES,
    describeAssistantRequestError
  } from '$lib/nostr/assistant.js';
  import { mergeAssistantRefs } from './assistant-refs.js';

  let {
    session = null,
    routeContext = null,
    selectedRefs = [],
    defaultSelectedRefs = [],
    panelOpen = false
  } = $props();

  let prompt = $state('');
  // Prompt submission and cancellation are independent: a run can be cancelled
  // while the prompt request that started it is still pending.
  let promptSubmitting = $state(false);
  let cancelSubmitting = $state(''); // '' | 'run' | 'session'
  let workflow = $state('');
  let promptError = $state('');
  let cancelError = $state('');
  let dismissedRefs = $state([]);
  let textarea;

  const WORKFLOW_LABELS = { batch: 'Batch plan', iterative: 'Iterative' };
  const visibleSelectedRefs = $derived(mergeAssistantRefs({ selectedRefs, defaultSelectedRefs, dismissedRefs }));
  const isHistory = $derived(session?.executionVersion === 1);
  const runActive = $derived(session?.executionVersion === 2 && !ASSISTANT_EXECUTION_TERMINAL_PHASES.includes(session?.phase));
  // Cancellation needs only the canonical run identity, never a plan hash.
  const canCancel = $derived(Boolean(session?.authoritative && session?.executionVersion === 2 && session?.currentRunId &&
    ASSISTANT_EXECUTION_CANCELLABLE_PHASES.includes(session?.phase)));
  const pendingPrompt = $derived(Object.values(pendingAssistantRequests).some((request) => request.sessionId === (session?.sessionId || assistantUi.activeSessionId)));
  const disabled = $derived(promptSubmitting || pendingPrompt || runActive || isHistory || assistantConnection.status === 'waiting_auth');
  const workflowLocked = $derived(Boolean(session?.uncertainEffects));
  const blockedReason = $derived(runActive
    ? 'A run is active in this session. Wait for it to finish or cancel it before sending another prompt.'
    : pendingPrompt && !promptSubmitting ? 'A prompt for this session is still pending.' : '');

  $effect(() => {
    if (panelOpen && textarea) textarea.focus();
  });

  async function submitPrompt(event) {
    event.preventDefault();
    const value = prompt.trim();
    if (!value || disabled) return;
    promptSubmitting = true;
    promptError = '';
    prompt = '';
    try {
      await publishAssistantPrompt({
        prompt: value,
        workflow,
        sessionId: session?.sessionId,
        routeContext,
        selectedRefs: visibleSelectedRefs.map((ref) => ref.ref)
      });
    } catch (err) {
      promptError = describeAssistantRequestError(err).message;
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

  async function cancel(scope) {
    if (!canCancel || cancelSubmitting) return;
    cancelSubmitting = scope;
    cancelError = '';
    try {
      await publishAssistantCancellation({ sessionId: session.sessionId, runId: session.currentRunId, scope });
    } catch (err) {
      cancelError = describeAssistantRequestError(err, 'Cancellation request').message;
    } finally {
      cancelSubmitting = '';
    }
  }
</script>

{#if canCancel}
  <div class="run-controls" aria-label="Current run controls">
    <button class="cancel" type="button" disabled={Boolean(cancelSubmitting)} onclick={() => cancel('run')}>{cancelSubmitting === 'run' ? 'Cancelling…' : 'Cancel run'}</button>
    <button class="cancel close-session" type="button" disabled={Boolean(cancelSubmitting)} title="Cancel the current run and close this session to new turns" onclick={() => cancel('session')}>{cancelSubmitting === 'session' ? 'Closing…' : 'Close session'}</button>
  </div>
{/if}

{#if cancelError}
  <p class="error cancel-error">{cancelError}</p>
{/if}
{#if promptError}
  <p class="error prompt-error">{promptError}</p>
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

{#if isHistory}
  <p class="history-note">This v1 session is read-only history. Start a new session to continue.</p>
{:else if !runActive}
  <label class="workflow-selector">Workflow
    <select bind:value={workflow} aria-label="Assistant workflow" disabled={workflowLocked || promptSubmitting}>
      <option value="">{session?.workflow ? `Keep ${WORKFLOW_LABELS[session.workflow] || session.workflow}` : 'Service default'}</option>
      <option value="batch">Batch plan</option>
      <option value="iterative">Iterative</option>
    </select>
  </label>
  {#if workflowLocked}<p class="history-note">Resolve uncertain operations before changing the workflow.</p>{/if}
{/if}
{#if blockedReason}<p class="history-note blocked-reason">{blockedReason}</p>{/if}
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
  .run-controls { display: flex; flex-wrap: wrap; gap: 0.5rem; margin: 0 0.75rem; }
  .cancel { background: var(--warning); color: #111827; }
  .close-session { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); }
  .workflow-selector, .history-note { margin: 0.5rem 0.75rem; color: var(--text-muted); font-size: 0.8rem; }
  select { margin-left: 0.5rem; }
  .error { color: var(--error); font-size: 0.875rem; margin: 0 0.75rem; }
</style>
