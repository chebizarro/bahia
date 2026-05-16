<script>
  import { publishAssistantApproval } from '$lib/stores/assistant.svelte.js';

  let { sessionId = '', plan = null, planHash = '', disabled = false } = $props();

  const riskLevel = $derived(String(plan?.risk_level || plan?.riskLevel || 'low').toLowerCase());
  const steps = $derived(Array.isArray(plan?.steps) ? plan.steps : []);
  let submitting = $state(false);
  let error = $state('');

  function previewArgs(step) {
    const value = step?.args_preview || step?.argsPreview || step?.tool_args || step?.toolArgs || {};
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }

  async function decide(decision) {
    if (!sessionId || !planHash || submitting) return;
    submitting = true;
    error = '';
    try {
      await publishAssistantApproval({ sessionId, planHash, decision });
    } catch (err) {
      error = err?.message || String(err);
    } finally {
      submitting = false;
    }
  }
</script>

{#if plan}
  <section class="plan-card" aria-label="Assistant plan approval">
    <div class="plan-header">
      <div>
        <div class="eyebrow">Plan review</div>
        <h3>{plan.summary || 'Assistant plan'}</h3>
      </div>
      <span class="risk {riskLevel}">{riskLevel}</span>
    </div>

    {#if steps.length > 0}
      <ol class="steps">
        {#each steps as step, index}
          <li>
            <div class="step-title">{step.title || `Step ${index + 1}`}</div>
            {#if step.description}
              <p>{step.description}</p>
            {/if}
            <div class="tool">{step.tool_name || step.toolName || 'tool'}</div>
            <pre>{previewArgs(step)}</pre>
          </li>
        {/each}
      </ol>
    {/if}

    {#if error}
      <p class="error">{error}</p>
    {/if}

    <div class="actions">
      <button type="button" class="approve" disabled={disabled || submitting} onclick={() => decide('approve')}>Approve</button>
      <button type="button" class="reject" disabled={disabled || submitting} onclick={() => decide('reject')}>Reject</button>
    </div>
  </section>
{/if}

<style>
  .plan-card {
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 1rem;
    background: rgba(99, 102, 241, 0.08);
  }
  .plan-header { display: flex; justify-content: space-between; gap: 1rem; align-items: flex-start; }
  .eyebrow { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; }
  h3 { font-size: 1rem; margin: 0.15rem 0 0; color: var(--text-primary); }
  .risk { padding: 0.2rem 0.5rem; border-radius: 999px; font-size: 0.75rem; text-transform: capitalize; border: 1px solid var(--border-color); }
  .risk.low { color: var(--success); border-color: var(--success); }
  .risk.medium { color: var(--warning); border-color: var(--warning); }
  .risk.high { color: var(--error); border-color: var(--error); }
  .steps { margin: 1rem 0 0; padding-left: 1.25rem; display: grid; gap: 0.75rem; }
  .step-title { font-weight: 600; color: var(--text-primary); }
  p { color: var(--text-muted); font-size: 0.875rem; margin: 0.25rem 0; }
  .tool { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8rem; color: var(--primary); margin-top: 0.35rem; }
  pre { white-space: pre-wrap; word-break: break-word; background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem; margin-top: 0.35rem; font-size: 0.75rem; color: var(--text-primary); }
  .actions { display: flex; gap: 0.5rem; margin-top: 1rem; }
  button { border: 0; border-radius: 6px; padding: 0.5rem 0.75rem; cursor: pointer; font-weight: 600; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .approve { background: var(--success); color: white; }
  .reject { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); }
  .error { color: var(--error); }
</style>
