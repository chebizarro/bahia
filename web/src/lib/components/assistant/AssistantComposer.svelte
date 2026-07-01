<script>
  import {
    assistantConnection,
    publishAssistantPrompt,
    publishAssistantApproval
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
  let submitting = $state(false);
  let error = $state('');
  let dismissedRefs = $state([]);
  let textarea;

  const visibleSelectedRefs = $derived(mergeAssistantRefs({ selectedRefs, defaultSelectedRefs, dismissedRefs }));
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
    prompt = '';
    try {
      await publishAssistantPrompt({
        prompt: value,
        sessionId: session?.sessionId,
        routeContext,
        selectedRefs: visibleSelectedRefs.map((ref) => ref.ref)
      });
    } catch (err) {
      error = err?.message || String(err);
    } finally {
      submitting = false;
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
  .error { color: var(--error); font-size: 0.875rem; margin: 0 0.75rem; }
</style>
