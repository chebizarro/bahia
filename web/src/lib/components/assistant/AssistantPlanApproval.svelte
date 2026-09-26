<script>
  import { publishAssistantApproval, bootstrapAssistant } from '$lib/stores/assistant.svelte.js';
  import { parseAssistantArgumentObjectText } from '$lib/nostr/assistant.js';

  let { session = null, disabled = false } = $props();
  let submitting = $state(false);
  let error = $state('');
  let stale = $state(false);
  let editedPlan = $state(null);
  let originalPlanJSON = 'null';
  let lastIdentity = '';
  let draftIdentity = $state('');
  let argsTextByStep = $state({});
  let argsErrorsByStep = $state({});
  const proposal = $derived(session?.proposal || null);
  const sessionId = $derived(session?.sessionId || '');
  const riskLevel = $derived(String(editedPlan?.risk_level || 'low').toLowerCase());
  const steps = $derived(Array.isArray(editedPlan?.steps) ? editedPlan.steps : []);
  const isModified = $derived(Boolean(editedPlan && JSON.stringify(editedPlan) !== originalPlanJSON));
  const hasInvalidArgs = $derived(Object.keys(argsErrorsByStep).length > 0);
  const currentIdentity = $derived(`${session?.currentRunId || ''}:${proposal?.proposal_id || ''}:${proposal?.revision || ''}:${proposal?.hash || ''}`);

  function clone(value) { return value ? JSON.parse(JSON.stringify(value)) : null; }
  function stepKey(step, index) { return step?.step_id || String(index); }
  function formatArgs(step) { return JSON.stringify(step?.tool_args || {}, null, 2); }
  async function reloadProposal() {
    if (stale && currentIdentity === draftIdentity) {
      await bootstrapAssistant({ force: true });
      return;
    }
    draftIdentity = currentIdentity;
    editedPlan = clone(proposal?.plan);
    originalPlanJSON = JSON.stringify(editedPlan);
    argsTextByStep = Object.fromEntries((editedPlan?.steps || []).map((step, i) => [stepKey(step, i), formatArgs(step)]));
    argsErrorsByStep = {};
    stale = false;
    error = '';
  }

  $effect.pre(() => {
    const identity = currentIdentity;
    if (identity !== lastIdentity) {
      lastIdentity = identity;
      if (editedPlan && (isModified || Object.keys(argsErrorsByStep).length)) stale = true;
      else reloadProposal();
    }
  });

  function removeStep(index) {
    const key = stepKey(steps[index], index);
    editedPlan.steps = editedPlan.steps.filter((_, i) => i !== index);
    const { [key]: _removedText, ...remainingText } = argsTextByStep;
    const { [key]: _removedError, ...remainingErrors } = argsErrorsByStep;
    argsTextByStep = remainingText;
    argsErrorsByStep = remainingErrors;
  }
  function moveStep(index, delta) {
    const nextIndex = index + delta;
    if (nextIndex < 0 || nextIndex >= steps.length) return;
    const reordered = [...steps];
    const [step] = reordered.splice(index, 1);
    reordered.splice(nextIndex, 0, step);
    editedPlan.steps = reordered;
  }
  function updateStepArgs(index, value) {
    const key = stepKey(steps[index], index);
    argsTextByStep = { ...argsTextByStep, [key]: value };
    try {
      const parsed = parseAssistantArgumentObjectText(value);
      editedPlan.steps[index].tool_args = parsed;
      const { [key]: _removed, ...remaining } = argsErrorsByStep;
      argsErrorsByStep = remaining;
    } catch (err) {
      argsErrorsByStep = { ...argsErrorsByStep, [key]: err?.message || String(err) };
    }
  }
  async function decide(decision) {
    if (!sessionId || !proposal || submitting || stale || (decision === 'approve' && hasInvalidArgs)) return;
    submitting = true;
    error = '';
    try {
      await publishAssistantApproval({ sessionId, runId: session.currentRunId,
        proposalId: proposal.proposal_id, baseRevision: proposal.revision, basePlanHash: proposal.hash,
        decision, modifiedPlan: decision === 'approve' && isModified ? clone(editedPlan) : null });
    } catch (err) {
      const detail = err?.message || String(err);
      if (/stale|proposal.changed|revision|requires.review/i.test(detail)) {
        stale = true; error = detail;
      } else error = `Request outcome unknown / reconnecting: ${detail}`;
    } finally {
      submitting = false;
    }
  }
</script>

{#if editedPlan && proposal}
  <section class="plan-card" aria-label="Assistant plan approval">
    <div class="plan-header">
      <div>
        <div class="eyebrow">Plan review</div>
        <h3>{editedPlan.summary || 'Assistant plan'}</h3>
      </div>
      <span class="risk {riskLevel}">{riskLevel}</span>
    </div>

    {#if steps.length > 0}
      <ol class="steps">
        {#each steps as step, index (stepKey(step, index))}
          <li>
            <div class="step-row">
              <div class="step-title">{step.title || `Step ${index + 1}`}</div>
              <div class="step-actions">
                <button type="button" class="mini" disabled={disabled || submitting || index === 0} onclick={() => moveStep(index, -1)}>↑</button>
                <button type="button" class="mini" disabled={disabled || submitting || index === steps.length - 1} onclick={() => moveStep(index, 1)}>↓</button>
                <button type="button" class="mini danger" disabled={disabled || submitting} onclick={() => removeStep(index)} aria-label="Remove step">×</button>
              </div>
            </div>
            {#if step.description}
              <p>{step.description}</p>
            {/if}
            <div class="tool">{step.tool_name || step.toolName || 'tool'}</div>
            <pre class="args-preview">{argsTextByStep[stepKey(step, index)] ?? '{}'}</pre>
            <label>
              <span>Tool args JSON</span>
              <textarea disabled={disabled || submitting} value={argsTextByStep[stepKey(step, index)] ?? '{}'} oninput={(event) => updateStepArgs(index, event.currentTarget.value)}></textarea>
            </label>
            {#if argsErrorsByStep[stepKey(step, index)]}<p class="error">Invalid JSON: {argsErrorsByStep[stepKey(step, index)]}</p>{/if}
          </li>
        {/each}
      </ol>
    {/if}

    {#if isModified}
      <p class="modified">Plan edited. Approval will submit the modified plan.</p>
    {/if}

    {#if stale}
      <p class="error">Proposal changed. Your local edits are preserved. Reload and review the current proposal before deciding.</p>
      <button type="button" class="reload" onclick={reloadProposal}>{currentIdentity === draftIdentity ? 'Refresh current proposal' : 'Reload and review current proposal'}</button>
    {/if}
    {#if error}
      <p class="error">{error}</p>
    {/if}

    <div class="actions">
      <button type="button" class="approve" disabled={disabled || submitting || stale || hasInvalidArgs} onclick={() => decide('approve')}>Approve</button>
      <button type="button" class="reject" disabled={disabled || submitting || stale} onclick={() => decide('reject')}>Reject</button>
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
  .step-row { display: flex; justify-content: space-between; gap: 0.5rem; align-items: center; }
  .step-title { font-weight: 600; color: var(--text-primary); }
  .step-actions { display: flex; gap: 0.25rem; }
  p { color: var(--text-muted); font-size: 0.875rem; margin: 0.25rem 0; }
  .tool { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8rem; color: var(--primary); margin-top: 0.35rem; }
  label { display: grid; gap: 0.25rem; margin-top: 0.35rem; color: var(--text-muted); font-size: 0.75rem; }
  textarea { min-height: 6rem; resize: vertical; white-space: pre; background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem; font: 0.75rem ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-primary); }
  .actions { display: flex; gap: 0.5rem; margin-top: 1rem; }
  button { border: 0; border-radius: 6px; padding: 0.5rem 0.75rem; cursor: pointer; font-weight: 600; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .mini { padding: 0.2rem 0.45rem; background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); }
  .danger { color: var(--error); }
  .approve { background: var(--success); color: white; }
  .reject { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); }
  .modified { color: var(--warning); }
  .error { color: var(--error); }
  .reload { margin-top: 0.5rem; background: var(--hover-bg); color: var(--text-primary); }
</style>
