<script>
  import { publishAssistantApproval, bootstrapAssistant } from '$lib/stores/assistant.svelte.js';
  import {
    ASSISTANT_REQUEST_ERROR_KINDS,
    canonicalAssistantJson,
    describeAssistantRequestError,
    parseAssistantArgumentObjectText
  } from '$lib/nostr/assistant.js';

  let { session = null, disabled = false } = $props();
  let submitting = $state(false);
  // Set once the service acknowledged a decision. The card stays (locked) until
  // the canonical projection records the proposal as consumed.
  let sentDecision = $state('');
  let error = $state('');
  let stale = $state(false);
  let editedPlan = $state(null);
  let originalPlanKey = '';
  let lastIdentity = '';
  let draftIdentity = $state('');
  let previousDraft = $state('');
  let argsTextByStep = $state({});
  let argsErrorsByStep = $state({});
  const proposal = $derived(session?.proposal || null);
  const sessionId = $derived(session?.sessionId || '');
  const riskLevel = $derived(String(editedPlan?.risk_level || 'low').toLowerCase());
  const steps = $derived(Array.isArray(editedPlan?.steps) ? editedPlan.steps : []);
  const isModified = $derived(Boolean(editedPlan) && planKey(editedPlan) !== originalPlanKey);
  const hasInvalidArgs = $derived(Object.keys(argsErrorsByStep).length > 0);
  // The draft is bound to one run + proposal + revision + hash.
  const currentIdentity = $derived(`${session?.currentRunId || ''}:${proposal?.proposal_id || ''}:${proposal?.revision || ''}:${proposal?.hash || ''}`);
  const proposalMoved = $derived(Boolean(draftIdentity) && currentIdentity !== draftIdentity);
  const locked = $derived(disabled || submitting || stale || Boolean(sentDecision));

  function clone(value) { return value ? JSON.parse(JSON.stringify(value)) : null; }
  function stepKey(step, index) { return step?.step_id || String(index); }
  function formatArgs(step) { return JSON.stringify(step?.tool_args || {}, null, 2); }
  // Argument key order is not an edit; compare canonical JSON.
  function planKey(plan) {
    try { return canonicalAssistantJson(clone(plan)); } catch { return JSON.stringify(plan); }
  }
  function draftSnapshot() {
    const draft = clone(editedPlan) || {};
    draft.steps = (draft.steps || []).map((step, index) => {
      const text = argsTextByStep[stepKey(step, index)];
      try { return { ...step, tool_args: parseAssistantArgumentObjectText(text) }; } catch { return { ...step, tool_args_text: text }; }
    });
    return JSON.stringify(draft, null, 2);
  }

  function loadCurrentProposal() {
    const plan = clone(proposal?.plan);
    draftIdentity = currentIdentity;
    originalPlanKey = planKey(plan);
    editedPlan = plan;
    argsTextByStep = Object.fromEntries((plan?.steps || []).map((step, i) => [stepKey(step, i), formatArgs(step)]));
    argsErrorsByStep = {};
    stale = false;
    sentDecision = '';
    error = '';
  }

  // Replacing the draft is always an explicit operator choice; the unsent
  // draft stays readable so edits can be re-applied to the new proposal.
  function reviewCurrentProposal() {
    if (isModified || hasInvalidArgs) previousDraft = draftSnapshot();
    loadCurrentProposal();
  }

  async function refreshCanonicalState() {
    await bootstrapAssistant({ force: true });
  }

  $effect.pre(() => {
    const identity = currentIdentity;
    if (identity === lastIdentity) return;
    lastIdentity = identity;
    // Keep unsent edits when the canonical proposal moves on underneath them.
    if (editedPlan && (isModified || hasInvalidArgs)) {
      stale = true;
      sentDecision = '';
    } else loadCurrentProposal();
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
    if (!sessionId || !proposal || locked || (decision === 'approve' && hasInvalidArgs)) return;
    submitting = true;
    error = '';
    try {
      await publishAssistantApproval({ sessionId, runId: session.currentRunId,
        proposalId: proposal.proposal_id, baseRevision: proposal.revision, basePlanHash: proposal.hash,
        decision, modifiedPlan: decision === 'approve' && isModified ? clone(editedPlan) : null });
      sentDecision = decision;
    } catch (err) {
      const described = describeAssistantRequestError(err, decision === 'approve' ? 'Approval' : 'Rejection');
      if (described.kind === ASSISTANT_REQUEST_ERROR_KINDS.STALE) stale = true;
      error = described.message;
    } finally {
      submitting = false;
    }
  }
</script>

{#if editedPlan && proposal}
  <section class="plan-card" aria-label="Assistant plan approval" data-proposal-id={proposal.proposal_id} data-revision={proposal.revision}>
    <div class="plan-header">
      <div>
        <div class="eyebrow">Plan review · revision {proposal.revision}</div>
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
                <button type="button" class="mini" aria-label="Move step up" disabled={locked || index === 0} onclick={() => moveStep(index, -1)}>↑</button>
                <button type="button" class="mini" aria-label="Move step down" disabled={locked || index === steps.length - 1} onclick={() => moveStep(index, 1)}>↓</button>
                <button type="button" class="mini danger" disabled={locked} onclick={() => removeStep(index)} aria-label="Remove step">×</button>
              </div>
            </div>
            {#if step.description}
              <p>{step.description}</p>
            {/if}
            <div class="tool">{step.tool_name || step.toolName || 'tool'}</div>
            <pre class="args-preview">{argsTextByStep[stepKey(step, index)] ?? '{}'}</pre>
            <label>
              <span>Tool args JSON</span>
              <textarea disabled={disabled || submitting || Boolean(sentDecision)} value={argsTextByStep[stepKey(step, index)] ?? '{}'} oninput={(event) => updateStepArgs(index, event.currentTarget.value)}></textarea>
            </label>
            {#if argsErrorsByStep[stepKey(step, index)]}<p class="error">Invalid JSON: {argsErrorsByStep[stepKey(step, index)]}</p>{/if}
          </li>
        {/each}
      </ol>
    {:else}
      <p class="modified">All steps removed. Approval completes this batch without dispatching anything.</p>
    {/if}

    {#if isModified}
      <p class="modified">Plan edited. Approval submits it as revision {proposal.revision + 1} of this proposal.</p>
    {/if}

    {#if stale}
      <p class="error stale">Proposal changed. Your local edits are preserved and were not submitted. Review the current proposal before deciding.</p>
      <div class="stale-actions">
        <button type="button" class="reload" onclick={reviewCurrentProposal}>{proposalMoved ? 'Review current proposal' : 'Discard edits and review current proposal'}</button>
        {#if !proposalMoved}
          <button type="button" class="refresh" onclick={refreshCanonicalState}>Refresh canonical state</button>
        {/if}
      </div>
    {/if}
    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}
    {#if sentDecision}
      <p class="sent" role="status">Decision sent ({sentDecision}); waiting for the canonical execution state to record it.</p>
    {/if}
    {#if previousDraft}
      <details class="previous-draft">
        <summary>Previous unsent draft</summary>
        <pre>{previousDraft}</pre>
      </details>
    {/if}

    <div class="actions">
      <button type="button" class="approve" disabled={locked || hasInvalidArgs} onclick={() => decide('approve')}>Approve</button>
      <button type="button" class="reject" disabled={locked} onclick={() => decide('reject')}>Reject</button>
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
  .stale-actions { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-top: 0.5rem; }
  .reload, .refresh { background: var(--hover-bg); color: var(--text-primary); border: 1px solid var(--border-color); }
  .sent { color: var(--text-primary); }
  .previous-draft pre { max-height: 10rem; overflow: auto; font: 0.72rem ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
