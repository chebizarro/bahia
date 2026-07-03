<script>
  import { publishAssistantActionDecision } from '$lib/stores/assistant.svelte.js';

  let { sessionId = '', action = null, disabled = false } = $props();

  let submitting = $state(false);
  let error = $state('');
  let reason = $state('');

  const actionId = $derived(action?.actionId || action?.action_id || '');
  const toolName = $derived(action?.toolName || action?.tool_name || 'tool');
  const toolCallId = $derived(action?.toolCallId || action?.tool_call_id || '');
  const approvalPrompt = $derived(action?.approvalPrompt || action?.approval_prompt || 'This assistant action requires approval.');
  const argsPreview = $derived(action?.argsPreview || action?.args_preview || null);
  const risk = $derived(String(action?.permission?.risk || action?.permission?.Risk || '').toLowerCase());

  function formatArgs(value) {
    if (!value) return '';
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }

  async function decide(decision) {
    if (!sessionId || !actionId || submitting) return;
    submitting = true;
    error = '';
    try {
      await publishAssistantActionDecision({
        sessionId,
        actionId,
        decision,
        reason: reason.trim()
      });
    } catch (err) {
      error = err?.message || String(err);
    } finally {
      submitting = false;
    }
  }
</script>

{#if actionId}
  <section class="action-card" aria-label="Assistant action approval">
    <div class="action-header">
      <div>
        <div class="eyebrow">Action approval required</div>
        <h3>{toolName}</h3>
      </div>
      {#if risk}
        <span class="risk {risk}">{risk}</span>
      {/if}
    </div>

    <p>{approvalPrompt}</p>

    {#if toolCallId}
      <div class="meta-row"><span>Tool call</span><code>{toolCallId}</code></div>
    {/if}
    <div class="meta-row"><span>Action</span><code>{actionId}</code></div>

    {#if argsPreview}
      <pre class="args-preview">{formatArgs(argsPreview)}</pre>
    {/if}

    <label>
      <span>Decision reason</span>
      <input bind:value={reason} disabled={disabled || submitting} placeholder="Optional reason for audit" />
    </label>

    {#if error}
      <p class="error">{error}</p>
    {/if}

    <div class="actions">
      <button type="button" class="approve" disabled={disabled || submitting} onclick={() => decide('approve')}>Approve action</button>
      <button type="button" class="reject" disabled={disabled || submitting} onclick={() => decide('reject')}>Reject</button>
    </div>
  </section>
{/if}

<style>
  .action-card {
    border: 1px solid var(--warning);
    border-radius: 10px;
    padding: 1rem;
    background: color-mix(in srgb, var(--warning, #f59e0b) 12%, transparent);
    display: grid;
    gap: 0.65rem;
  }
  .action-header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  .eyebrow { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; }
  h3 { font-size: 1rem; margin: 0.15rem 0 0; color: var(--text-primary); word-break: break-word; }
  p { color: var(--text-muted); font-size: 0.875rem; margin: 0; }
  .risk { padding: 0.2rem 0.5rem; border-radius: 999px; font-size: 0.75rem; text-transform: capitalize; border: 1px solid var(--border-color); }
  .risk.low { color: var(--success); border-color: var(--success); }
  .risk.medium { color: var(--warning); border-color: var(--warning); }
  .risk.high { color: var(--error); border-color: var(--error); }
  .meta-row { display: flex; flex-wrap: wrap; gap: 0.4rem; align-items: baseline; color: var(--text-muted); font-size: 0.75rem; }
  code { font-size: 0.72rem; color: var(--text-muted); word-break: break-all; }
  .args-preview { margin: 0; max-height: 9rem; overflow: auto; background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem; color: var(--text-primary); font: 0.75rem ui-monospace, SFMono-Regular, Menlo, monospace; }
  label { display: grid; gap: 0.25rem; color: var(--text-muted); font-size: 0.75rem; }
  input { background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem; color: var(--text-primary); }
  .actions { display: flex; gap: 0.5rem; flex-wrap: wrap; }
  button { border: 0; border-radius: 6px; padding: 0.5rem 0.75rem; cursor: pointer; font-weight: 600; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .approve { background: var(--success); color: white; }
  .reject { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); }
  .error { color: var(--error); }
</style>
